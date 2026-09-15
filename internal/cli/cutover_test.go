package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/dns"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/transfer"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func TestCutoverStepsOrderAndContent(t *testing.T) {
	v := tui.CutoverView{
		Domain:       "example.com",
		SourceIP:     "203.0.113.1",
		DestIP:       "198.51.100.7",
		DNS:          &dns.Snapshot{Records: []dns.Record{{Type: "A", Value: "203.0.113.1", TTL: 86400}}},
		MailAtSource: true,
		Smoke:        &tui.SmokeResult{Scheme: "https", Status: 200},
		Crontab:      []string{"*/5 * * * * php cron.php"},
	}
	steps := cutoverSteps(v)
	joined := strings.Join(steps, "\n")

	for _, want := range []string{
		"hosts-file entry: 198.51.100.7 example.com",
		"move mail BEFORE the DNS flip",
		"point the domain's A/AAAA records at 198.51.100.7",
		"current TTL 86400s",
		"SSL certificate for example.com",
		"recreate the 1 source cron entry",
		"run 'rehost migrate' once more",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("steps missing %q:\n%s", want, joined)
		}
	}
	// Mail must be handled before the DNS flip step.
	if strings.Index(joined, "move mail") > strings.Index(joined, "point the domain's") {
		t.Error("the mail step must precede the DNS flip step")
	}
}

func TestCutoverStepsFailedSmokeLeadsWithFix(t *testing.T) {
	v := tui.CutoverView{
		Domain: "example.com", DestIP: "198.51.100.7",
		Smoke: &tui.SmokeResult{Status: 500},
	}
	steps := cutoverSteps(v)
	if len(steps) == 0 || !strings.HasPrefix(steps[0], "FIX FIRST") {
		t.Errorf("a failed smoke test must lead the checklist: %v", steps)
	}
}

func TestCutoverStepsWithoutDomainStillUsable(t *testing.T) {
	steps := cutoverSteps(tui.CutoverView{DestIP: "198.51.100.7"})
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "point the domain's A/AAAA records at 198.51.100.7") ||
		!strings.Contains(joined, "add domain: to migrate.yaml") {
		t.Errorf("no-domain checklist should still instruct and suggest domain:\n%s", joined)
	}
}

func TestSourceCrontabFiltersNoiseAndDegrades(t *testing.T) {
	r := &fakeConn{catStdout: ""}
	r.crontab = "# comment\n\n*/5 * * * * php cron.php\nMAILTO=admin@example.com\n"
	lines := sourceCrontab(context.Background(), r, testUI(tui.ModeJSON, &bytes.Buffer{}))
	if len(lines) != 2 || lines[0] != "*/5 * * * * php cron.php" {
		t.Errorf("crontab lines = %v", lines)
	}

	r2 := &fakeConn{crontabExit: 1} // no crontab for this user
	if got := sourceCrontab(context.Background(), r2, testUI(tui.ModeJSON, &bytes.Buffer{})); got != nil {
		t.Errorf("missing crontab should degrade to none, got %v", got)
	}
}

func TestCutoverSitesReadsManifestCounts(t *testing.T) {
	stateDir := t.TempDir()
	destRoot := "/home/d/www"
	m := &transfer.Manifest{Root: destRoot, Files: []transfer.FileEntry{{Path: "a"}, {Path: "b"}}}
	path := filepath.Join(stateDir, "manifests", transfer.DestManifestFilename("d@dst", destRoot))
	if err := transfer.SaveManifest(m, path); err != nil {
		t.Fatal(err)
	}
	f := &project.File{Sites: []project.Site{
		{Framework: "wordpress", Root: "/home/u/www"},
		{Framework: "drupal", Root: "/home/u/blog"},
	}}
	sites := cutoverSites(f, "/home/u", "/home/d", "d@dst", stateDir)
	if len(sites) != 2 {
		t.Fatalf("sites = %+v", sites)
	}
	if sites[0].DestRoot != destRoot || sites[0].Files != 2 {
		t.Errorf("site 0 should carry the manifest count: %+v", sites[0])
	}
	if sites[1].Files != -1 {
		t.Errorf("site without a manifest should report -1: %+v", sites[1])
	}
}

func TestCutoverReportJSONEnvelope(t *testing.T) {
	v := tui.CutoverView{
		Domain: "example.com", DestIP: "198.51.100.7",
		Smoke: &tui.SmokeResult{Scheme: "https", Status: 200},
		Sites: []tui.CutoverSite{{Site: "/home/u/www", DestRoot: "/home/d/www", Files: 10}},
	}
	v.Steps = cutoverSteps(v)
	var buf bytes.Buffer
	if err := tui.New(tui.ModeJSON, &buf).CutoverReport(v); err != nil {
		t.Fatal(err)
	}
	var env tui.CutoverEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.cutover.v1" || env.DestIP != "198.51.100.7" || len(env.Steps) == 0 {
		t.Errorf("envelope = %+v", env)
	}
	if !env.Smoke.OK() {
		t.Errorf("smoke 200 should be OK: %+v", env.Smoke)
	}
}

// A 404 is what an unconfigured vhost serves — the checklist must lead with
// FIX FIRST, not spot-check advice.
func TestCutoverSteps404IsNotServing(t *testing.T) {
	v := tui.CutoverView{
		Domain: "example.com",
		DestIP: "198.51.100.7",
		Smoke:  &tui.SmokeResult{Scheme: "https", Status: 404},
	}
	steps := cutoverSteps(v)
	if len(steps) == 0 || !strings.Contains(steps[0], "FIX FIRST") {
		t.Errorf("404 should demand a fix before the flip: %v", steps)
	}
}

// Low TTLs that were only read from a resolver cache must carry the hedge in
// the DNS step.
func TestCutoverStepsCachedTTLHedge(t *testing.T) {
	v := tui.CutoverView{
		Domain: "example.com",
		DestIP: "198.51.100.7",
		DNS: &dns.Snapshot{Records: []dns.Record{
			{Type: "A", Value: "192.0.2.10", TTL: 120},
		}},
	}
	found := false
	for _, s := range cutoverSteps(v) {
		if strings.Contains(s, "resolver cache") {
			found = true
		}
	}
	if !found {
		t.Errorf("cached TTLs should be hedged in the steps: %v", cutoverSteps(v))
	}
}
