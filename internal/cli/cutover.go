package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/go-dns"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/transfer"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func newCutoverCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "cutover",
		Short: "Go-live checklist: verify the destination, then DNS/mail/SSL/cron instructions",
		Long: `cutover produces the go-live report after 'rehost migrate' converged. It
verifies what it can — an HTTP probe of the destination through a dial
override (the hosts-file trick without editing hosts), the per-site file
counts from the last sync — and prints the ordered instructions for the
steps rehost deliberately leaves to you: pointing DNS at the destination
(with the current records and TTLs), moving mail first if MX still points
at the source, issuing an SSL certificate, and recreating the source's
crontab. It changes nothing anywhere.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCutover(cmd, opts)
		},
	}
}

// smokeFn is the HTTP probe seam; tests substitute a fake.
var smokeFn = smokeTest

func runCutover(cmd *cobra.Command, opts *options) error {
	u := newUI(cmd, opts)
	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	if err := requireDestination(f, opts.projectFile, "cutover"); err != nil {
		return err
	}

	srcClient, srcCaps, err := dialProbe(cmd.Context(), f.Source.SSHConfig(), "source", u)
	if err != nil {
		return u.fail(err)
	}
	defer func() { _ = srcClient.Close() }()
	dstClient, dstCaps, err := dialProbe(cmd.Context(), f.Destination.SSHConfig(), "destination", u)
	if err != nil {
		return u.fail(err)
	}
	defer func() { _ = dstClient.Close() }()

	view := tui.CutoverView{
		Domain:   f.Domain,
		SourceIP: srcClient.RemoteIP(),
		DestIP:   dstClient.RemoteIP(),
	}

	if f.Domain != "" {
		u.progress("dns: looking up %s…", f.Domain)
		if snap, err := dns.NewClient().Snapshot(cmd.Context(), f.Domain); err != nil {
			u.progress("dns: %v", err)
		} else {
			view.DNS = snap
			view.MailAtSource = snap.MailPointsAt([]string{view.SourceIP})
		}
		u.progress("probing the destination as %s…", f.Domain)
		smoke := smokeFn(cmd.Context(), f.Domain, view.DestIP)
		view.Smoke = &smoke
	}

	view.Sites = cutoverSites(f, srcCaps.Home, dstCaps.Home, dstCaps.Target(),
		filepath.Join(filepath.Dir(opts.projectFile), ".rehost"))
	view.Crontab = sourceCrontab(cmd.Context(), srcClient, u)
	view.Steps = cutoverSteps(view)

	return u.renderer.CutoverReport(view)
}

// cutoverSites maps the persisted sites to their destination docroots and
// reads each post-sync manifest's file count (-1 when none exists yet).
func cutoverSites(f *project.File, srcHome, destHome, destTarget, stateDir string) []tui.CutoverSite {
	var sites []tui.CutoverSite
	for _, s := range f.Sites {
		destRoot := s.DestRoot
		if destRoot == "" {
			destRoot = mapDestRoot(s.Root, srcHome, destHome)
		}
		files := -1
		path := filepath.Join(stateDir, "manifests", transfer.DestManifestFilename(destTarget, destRoot))
		if m, err := transfer.LoadManifest(path); err == nil && m != nil {
			files = len(m.Files)
		}
		sites = append(sites, tui.CutoverSite{Site: s.Root, DestRoot: destRoot, Files: files})
	}
	return sites
}

// sourceCrontab reads the source account's crontab entries; a missing crontab
// (exit non-zero) or a transport hiccup degrade to none — the report simply
// has nothing to recreate.
func sourceCrontab(ctx context.Context, r stateRunner, u ui) []string {
	res, err := r.Run(ctx, "crontab -l 2>/dev/null")
	if err != nil || res.ExitCode != 0 {
		if err != nil {
			u.progress("crontab: %v", err)
		}
		return nil
	}
	var lines []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		lines = append(lines, t)
	}
	return lines
}

// cutoverSteps turns the verified facts into the ordered go-live checklist.
// Pure, so the wording and ordering are unit-tested directly.
func cutoverSteps(v tui.CutoverView) []string {
	var steps []string

	if v.Smoke != nil && !v.Smoke.OK() {
		steps = append(steps, "FIX FIRST: the destination did not serve "+v.Domain+
			" — test it yourself with a hosts-file entry ("+v.DestIP+" "+v.Domain+") and do not continue until it works")
	} else if v.Domain != "" {
		steps = append(steps, "spot-check the site yourself with a hosts-file entry: "+v.DestIP+" "+v.Domain)
	}

	if v.MailAtSource {
		steps = append(steps, "move mail BEFORE the DNS flip: MX still resolves to the source ("+v.SourceIP+
			") — set up mailboxes at the new provider or keep an external mail service, then update MX")
	}

	if v.DNS != nil {
		var ttlNote string
		for _, r := range v.DNS.Records {
			if (r.Type == "A" || r.Type == "AAAA") && r.TTL > 3600 {
				ttlNote = fmt.Sprintf(" (current TTL %ds — lower it to 300 now and wait one old-TTL period before the flip)", r.TTL)
				break
			}
		}
		if ttlNote == "" && !v.DNS.AuthoritativeTTLs {
			// Low-looking TTLs from a resolver cache are decaying remainders;
			// the flip plan must not be built on them.
			ttlNote = " (TTLs could only be read from a resolver cache — confirm the real TTL at the DNS provider before planning the flip)"
		}
		steps = append(steps, "point the domain's A/AAAA records at "+v.DestIP+ttlNote)
	} else {
		steps = append(steps, "point the domain's A/AAAA records at "+v.DestIP+
			" (add domain: to migrate.yaml and rerun cutover for live records and TTLs)")
	}

	name := v.Domain
	if name == "" {
		name = "the domain"
	}
	steps = append(steps, "issue an SSL certificate for "+name+
		" on the destination (panel / Let's Encrypt) as soon as DNS resolves there — or pre-issue via DNS validation")

	if len(v.Crontab) > 0 {
		steps = append(steps, fmt.Sprintf("recreate the %d source cron entr%s on the destination (listed below; paths may need the new home)",
			len(v.Crontab), plural(len(v.Crontab), "y", "ies")))
	}

	steps = append(steps,
		"right before the flip: run 'rehost migrate' once more — the final delta is small and fast",
		"after the flip: watch the site on the destination, then keep the source account until DNS has fully propagated (old TTL) and mail/cron are confirmed")
	return steps
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// smokeTest probes the destination as if DNS already pointed there: every
// connection dials destIP while the request carries the real domain, so name-
// based vhosts route correctly. TLS verification is off on purpose — the
// destination's certificate is typically issued only after cutover; serving
// is what is being proven here, the checklist handles the certificate.
func smokeTest(ctx context.Context, domain, destIP string) tui.SmokeResult {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(destIP, port))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // pre-cutover: cert not issued yet, see doc comment
		},
	}

	var lastErr error
	for _, scheme := range []string{"https", "http"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+domain+"/", nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		return tui.SmokeResult{Scheme: scheme, Status: resp.StatusCode}
	}
	return tui.SmokeResult{Err: fmt.Sprintf("%v", lastErr)}
}
