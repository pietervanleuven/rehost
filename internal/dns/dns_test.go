package dns

import (
	"context"
	"errors"
	"net"
	"testing"

	mdns "github.com/miekg/dns"
)

// fakeExchange serves canned RRs per question name+type.
func fakeExchange(answers map[string][]mdns.RR) exchangeFunc {
	return func(_ context.Context, m *mdns.Msg, _ string) (*mdns.Msg, error) {
		q := m.Question[0]
		r := new(mdns.Msg)
		r.SetReply(m)
		r.Answer = answers[q.Name+"/"+mdns.TypeToString[q.Qtype]]
		return r, nil
	}
}

func rrA(name, ip string, ttl uint32) *mdns.A {
	return &mdns.A{Hdr: mdns.RR_Header{Name: name, Rrtype: mdns.TypeA, Ttl: ttl}, A: net.ParseIP(ip)}
}

func rrMX(name, target string, pref uint16, ttl uint32) *mdns.MX {
	return &mdns.MX{Hdr: mdns.RR_Header{Name: name, Rrtype: mdns.TypeMX, Ttl: ttl}, Preference: pref, Mx: target}
}

func TestSnapshot(t *testing.T) {
	c := &Client{servers: []string{"fake:53"}, exchange: fakeExchange(map[string][]mdns.RR{
		"example.com./A":      {rrA("example.com.", "192.0.2.10", 3600)},
		"example.com./MX":     {rrMX("example.com.", "mail.example.com.", 10, 7200)},
		"example.com./NS":     {&mdns.NS{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeNS, Ttl: 86400}, Ns: "ns1.example.net."}},
		"example.com./TXT":    {&mdns.TXT{Hdr: mdns.RR_Header{Name: "example.com.", Rrtype: mdns.TypeTXT, Ttl: 300}, Txt: []string{"v=spf1 ", "mx -all"}}},
		"mail.example.com./A": {rrA("mail.example.com.", "192.0.2.10", 3600)},
	})}

	snap, err := c.Snapshot(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Records) != 4 {
		t.Fatalf("got %d records: %+v", len(snap.Records), snap.Records)
	}
	if snap.Records[0].Type != "A" || snap.Records[0].Value != "192.0.2.10" || snap.Records[0].TTL != 3600 {
		t.Errorf("first record should be the A: %+v", snap.Records[0])
	}
	var mx *Record
	for i := range snap.Records {
		if snap.Records[i].Type == "MX" {
			mx = &snap.Records[i]
		}
	}
	if mx == nil || mx.Value != "mail.example.com" || mx.Priority != 10 || mx.TTL != 7200 {
		t.Errorf("MX record wrong: %+v", mx)
	}
	var txt string
	for _, r := range snap.Records {
		if r.Type == "TXT" {
			txt = r.Value
		}
	}
	if txt != "v=spf1 mx -all" {
		t.Errorf("TXT chunks should be joined: %q", txt)
	}

	if got := snap.MailHosts["mail.example.com"]; len(got) != 1 || got[0] != "192.0.2.10" {
		t.Errorf("MailHosts = %+v", snap.MailHosts)
	}
	if !snap.HasMX() {
		t.Error("HasMX should be true")
	}
	if addrs := snap.Addresses(); len(addrs) != 1 || addrs[0] != "192.0.2.10" {
		t.Errorf("Addresses = %v", addrs)
	}
}

func TestMailPointsAt(t *testing.T) {
	snap := &Snapshot{MailHosts: map[string][]string{"mail.example.com": {"192.0.2.10", "2001:db8::1"}}}
	if !snap.MailPointsAt([]string{"192.0.2.10"}) {
		t.Error("mail on the source IP should be detected")
	}
	if snap.MailPointsAt([]string{"198.51.100.7"}) {
		t.Error("unrelated IP must not match")
	}
	if (&Snapshot{}).MailPointsAt([]string{"192.0.2.10"}) {
		t.Error("no MX → mail cannot point at the source")
	}
}

func TestSnapshotAllQueriesFail(t *testing.T) {
	c := &Client{servers: []string{"fake:53"}, exchange: func(context.Context, *mdns.Msg, string) (*mdns.Msg, error) {
		return nil, errors.New("network unreachable")
	}}
	if _, err := c.Snapshot(context.Background(), "example.com"); err == nil {
		t.Error("total failure must return an error")
	}
}

func TestSnapshotPartialFailureTolerated(t *testing.T) {
	c := &Client{servers: []string{"fake:53"}, exchange: func(_ context.Context, m *mdns.Msg, _ string) (*mdns.Msg, error) {
		if m.Question[0].Qtype == mdns.TypeTXT {
			return nil, errors.New("timeout")
		}
		r := new(mdns.Msg)
		r.SetReply(m)
		if m.Question[0].Qtype == mdns.TypeA {
			r.Answer = []mdns.RR{rrA("example.com.", "192.0.2.10", 60)}
		}
		return r, nil
	}}
	snap, err := c.Snapshot(context.Background(), "example.com")
	if err != nil || len(snap.Records) != 1 {
		t.Fatalf("partial failure should still snapshot: %v, %+v", err, snap)
	}
}
