package tui

import (
	"fmt"
	"io"

	"github.com/pietervanleuven/go-dns"
)

// cutoverSchema versions the cutover JSON envelope.
const cutoverSchema = "rehost.cutover.v1"

// CutoverView is the go-live report: what rehost verified about the migrated
// destination and the ordered instructions for the steps rehost deliberately
// does not perform — DNS, mail, SSL, cron (the MVP scope guard: those are a
// report, not migration targets). Pure data assembled by the caller.
type CutoverView struct {
	Domain   string `json:"domain,omitempty"`
	SourceIP string `json:"source_ip,omitempty"`
	DestIP   string `json:"dest_ip"`
	// DNS is the live snapshot; nil when migrate.yaml has no domain.
	DNS *dns.Snapshot `json:"dns,omitempty"`
	// MailAtSource warns that MX targets resolve to the source host.
	MailAtSource bool `json:"mail_at_source"`
	// Smoke is the HTTP probe of the destination through a dial override
	// (the hosts-file trick, without editing hosts); nil when no domain.
	Smoke *SmokeResult `json:"smoke,omitempty"`
	// Sites lists each site's destination docroot with the file count from
	// the persisted post-sync manifest (-1 when no manifest exists yet).
	Sites []CutoverSite `json:"sites"`
	// Crontab is the source account's crontab, entry lines only; empty
	// means no crontab (nothing to recreate).
	Crontab []string `json:"crontab,omitempty"`
	// Steps is the ordered go-live checklist.
	Steps []string `json:"steps"`
}

// SmokeResult is the destination HTTP probe outcome.
type SmokeResult struct {
	Scheme string `json:"scheme,omitempty"` // https or http, whichever answered
	Status int    `json:"status,omitempty"`
	Err    string `json:"error,omitempty"`
}

// OK reports whether the destination served a plausible response: success
// and redirects (2xx/3xx) and auth walls (401/403) prove the vhost answers.
// A 404 is exactly what an unconfigured vhost's default page returns, so it
// must not read as "serving correctly" — this feeds the one irreversible
// manual step (the DNS flip).
func (s SmokeResult) OK() bool {
	if s.Err != "" || s.Status <= 0 {
		return false
	}
	return s.Status < 400 || s.Status == 401 || s.Status == 403
}

// CutoverSite is one migrated site in the go-live picture.
type CutoverSite struct {
	Site     string `json:"site"`
	DestRoot string `json:"dest_root"`
	Files    int    `json:"files"` // from the persisted post-sync manifest; -1 unknown
}

// --- styled ---

func (r styledRenderer) CutoverReport(v CutoverView) error {
	fprintln(r.out, titleStyle.Render("Cutover — go-live checklist"))
	fprintln(r.out)
	renderCutoverFacts(r.out, v, func(s string) string { return dimStyle.Render(s) },
		func(s string) string { return warnStyle.Render(s) })
	fprintln(r.out)
	fprintln(r.out, titleStyle.Render("Steps"))
	for i, step := range v.Steps {
		fprintf(r.out, "  %d. %s\n", i+1, step)
	}
	return nil
}

// --- plain ---

func (r plainRenderer) CutoverReport(v CutoverView) error {
	fprintln(r.out, "cutover — go-live checklist")
	renderCutoverFacts(r.out, v, func(s string) string { return s }, func(s string) string { return "! " + s })
	fprintln(r.out, "steps:")
	for i, step := range v.Steps {
		fprintf(r.out, "  %d. %s\n", i+1, step)
	}
	return nil
}

// renderCutoverFacts writes the verified-facts section shared by the styled
// and plain renderers; dim and warn decorate lines per mode.
func renderCutoverFacts(out io.Writer, v CutoverView, dim, warn func(string) string) {
	fprintf(out, "  destination IP: %s\n", v.DestIP)
	for _, s := range v.Sites {
		line := fmt.Sprintf("  site %s → %s", s.Site, s.DestRoot)
		if s.Files >= 0 {
			line += fmt.Sprintf(" (%d files per last sync)", s.Files)
		}
		fprintln(out, dim(line))
	}
	switch {
	case v.Smoke == nil:
		fprintln(out, dim("  smoke test: skipped — no domain: in migrate.yaml"))
	case v.Smoke.OK():
		fprintf(out, "  smoke test: destination serves %s over %s (HTTP %d)\n", v.Domain, v.Smoke.Scheme, v.Smoke.Status)
	case v.Smoke.Err != "":
		fprintln(out, warn(fmt.Sprintf("smoke test FAILED: %s — verify the destination before touching DNS", v.Smoke.Err)))
	default:
		fprintln(out, warn(fmt.Sprintf("smoke test: HTTP %d from the destination — investigate before touching DNS", v.Smoke.Status)))
	}
	if v.MailAtSource {
		fprintln(out, warn("mail (MX) points at the source — move mail first or it dies with the old hosting"))
	}
}

// --- JSON ---

// CutoverEnvelope is the versioned JSON shape of the cutover report.
type CutoverEnvelope struct {
	Schema string `json:"schema"`
	CutoverView
}

func (r jsonRenderer) CutoverReport(v CutoverView) error {
	if v.Sites == nil {
		v.Sites = []CutoverSite{}
	}
	if v.Steps == nil {
		v.Steps = []string{}
	}
	return encodeJSON(r.out, CutoverEnvelope{Schema: cutoverSchema, CutoverView: v})
}
