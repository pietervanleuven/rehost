// Package dns takes a snapshot of a domain's public records — A/AAAA, MX,
// NS, TXT with their TTLs — for the check gate now and the cutover report in
// a later phase. rehost never changes DNS; it only reads and reports
// (MVP scope guard: mail/DNS are a report, not a migration target).
package dns

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

// Record is one DNS resource record of the snapshot.
type Record struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	TTL      uint32 `json:"ttl"`
	Priority uint16 `json:"priority,omitempty"` // MX preference
}

// Snapshot is everything read about a domain at one point in time.
type Snapshot struct {
	Domain  string   `json:"domain"`
	Records []Record `json:"records"`
	// MailHosts maps each MX target to the IPs it resolves to, for the
	// "mail points at the source" warning.
	MailHosts map[string][]string `json:"mail_hosts,omitempty"`
}

// exchangeFunc is the seam tests replace; production uses miekg/dns.
type exchangeFunc func(ctx context.Context, m *mdns.Msg, server string) (*mdns.Msg, error)

// Client queries the system's resolvers (fallback: well-known public ones).
type Client struct {
	servers  []string
	exchange exchangeFunc
}

// resolvConf is the standard resolver configuration path; macOS and Linux
// both maintain it.
const resolvConf = "/etc/resolv.conf"

// NewClient builds a client using the system resolvers, falling back to
// public ones when none can be read.
func NewClient() *Client {
	c := &mdns.Client{Timeout: 5 * time.Second}
	client := &Client{
		exchange: func(ctx context.Context, m *mdns.Msg, server string) (*mdns.Msg, error) {
			r, _, err := c.ExchangeContext(ctx, m, server)
			return r, err
		},
	}
	if conf, err := mdns.ClientConfigFromFile(resolvConf); err == nil && len(conf.Servers) > 0 {
		for _, s := range conf.Servers {
			client.servers = append(client.servers, joinServerPort(s, conf.Port))
		}
	} else {
		client.servers = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	return client
}

func joinServerPort(server, port string) string {
	if port == "" {
		port = "53"
	}
	if strings.Contains(server, ":") { // bare IPv6
		return "[" + server + "]:" + port
	}
	return server + ":" + port
}

var queryTypes = []uint16{mdns.TypeA, mdns.TypeAAAA, mdns.TypeCNAME, mdns.TypeMX, mdns.TypeNS, mdns.TypeTXT}

// Snapshot reads the domain's records. Individual query failures are
// tolerated; an error is returned only when nothing could be read at all.
func (c *Client) Snapshot(ctx context.Context, domain string) (*Snapshot, error) {
	snap := &Snapshot{Domain: domain}
	var lastErr error
	seen := map[string]bool{}
	for _, qt := range queryTypes {
		rrs, err := c.query(ctx, domain, qt)
		if err != nil {
			lastErr = err
			continue
		}
		for _, rec := range recordsFrom(rrs) {
			key := rec.Type + "\x00" + rec.Value
			if !seen[key] {
				seen[key] = true
				snap.Records = append(snap.Records, rec)
			}
		}
	}
	if len(snap.Records) == 0 && lastErr != nil {
		return nil, fmt.Errorf("looking up %s: %w", domain, lastErr)
	}
	sortRecords(snap.Records)
	c.resolveMailHosts(ctx, snap)
	return snap, nil
}

// resolveMailHosts resolves each MX target to its IPs (best-effort).
func (c *Client) resolveMailHosts(ctx context.Context, snap *Snapshot) {
	for _, rec := range snap.Records {
		if rec.Type != "MX" {
			continue
		}
		var ips []string
		for _, qt := range []uint16{mdns.TypeA, mdns.TypeAAAA} {
			rrs, err := c.query(ctx, rec.Value, qt)
			if err != nil {
				continue
			}
			for _, r := range recordsFrom(rrs) {
				if r.Type == "A" || r.Type == "AAAA" {
					ips = append(ips, r.Value)
				}
			}
		}
		if len(ips) > 0 {
			if snap.MailHosts == nil {
				snap.MailHosts = map[string][]string{}
			}
			sort.Strings(ips)
			snap.MailHosts[rec.Value] = ips
		}
	}
}

// query asks each configured server until one answers.
func (c *Client) query(ctx context.Context, name string, qtype uint16) ([]mdns.RR, error) {
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(name), qtype)
	m.RecursionDesired = true
	var lastErr error
	for _, server := range c.servers {
		r, err := c.exchange(ctx, m, server)
		if err != nil {
			lastErr = err
			continue
		}
		// NXDOMAIN and friends are honest absences, not failures.
		return r.Answer, nil
	}
	return nil, lastErr
}

// recordsFrom converts answer RRs to Records; unknown types are skipped.
func recordsFrom(rrs []mdns.RR) []Record {
	var out []Record
	for _, rr := range rrs {
		ttl := rr.Header().Ttl
		switch r := rr.(type) {
		case *mdns.A:
			out = append(out, Record{Type: "A", Value: r.A.String(), TTL: ttl})
		case *mdns.AAAA:
			out = append(out, Record{Type: "AAAA", Value: r.AAAA.String(), TTL: ttl})
		case *mdns.CNAME:
			out = append(out, Record{Type: "CNAME", Value: unfqdn(r.Target), TTL: ttl})
		case *mdns.MX:
			out = append(out, Record{Type: "MX", Value: unfqdn(r.Mx), TTL: ttl, Priority: r.Preference})
		case *mdns.NS:
			out = append(out, Record{Type: "NS", Value: unfqdn(r.Ns), TTL: ttl})
		case *mdns.TXT:
			out = append(out, Record{Type: "TXT", Value: strings.Join(r.Txt, ""), TTL: ttl})
		}
	}
	return out
}

// typeOrder keeps snapshots readable: address records, then mail, then the rest.
var typeOrder = map[string]int{"A": 0, "AAAA": 1, "CNAME": 2, "MX": 3, "NS": 4, "TXT": 5}

func sortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Type != records[j].Type {
			return typeOrder[records[i].Type] < typeOrder[records[j].Type]
		}
		if records[i].Priority != records[j].Priority {
			return records[i].Priority < records[j].Priority
		}
		return records[i].Value < records[j].Value
	})
}

func unfqdn(s string) string { return strings.TrimSuffix(s, ".") }

// Addresses returns the domain's A/AAAA record values.
func (s *Snapshot) Addresses() []string {
	var out []string
	for _, r := range s.Records {
		if r.Type == "A" || r.Type == "AAAA" {
			out = append(out, r.Value)
		}
	}
	return out
}

// MailPointsAt reports whether any MX target resolves to one of the given
// IPs — the "mail is hosted on the source" signal that makes a naive DNS
// cutover break email.
func (s *Snapshot) MailPointsAt(ips []string) bool {
	set := map[string]bool{}
	for _, ip := range ips {
		set[ip] = true
	}
	for _, hostIPs := range s.MailHosts {
		for _, ip := range hostIPs {
			if set[ip] {
				return true
			}
		}
	}
	return false
}

// HasMX reports whether the domain has any MX record.
func (s *Snapshot) HasMX() bool {
	for _, r := range s.Records {
		if r.Type == "MX" {
			return true
		}
	}
	return false
}
