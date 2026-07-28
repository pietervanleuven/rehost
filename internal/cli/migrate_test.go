package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// scriptRunner is a fake stateRunner that answers the two commands
// destStateResults issues — the `ls -A` docroot stat and the history `cat` —
// so the destination-state policy is exercised without a real SSH connection.
// A docroot listed in nonEmpty comes back with entries; every other docroot is
// empty/absent. history is the raw history.jsonl content (empty = no file).
type scriptRunner struct {
	nonEmpty map[string]bool
	history  string
}

func (s scriptRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	switch {
	case strings.HasPrefix(cmd, "ls -A"):
		for root, ne := range s.nonEmpty {
			if ne && strings.Contains(cmd, root) {
				return ssh.Result{Stdout: "index.php\nwp-content\n"}, nil
			}
		}
		return ssh.Result{}, nil // empty or absent: nothing listed
	case strings.HasPrefix(cmd, "cat "):
		if s.history == "" {
			return ssh.Result{ExitCode: 1}, nil // no history file yet
		}
		return ssh.Result{Stdout: s.history}, nil
	default:
		return ssh.Result{}, nil
	}
}

func sites(roots ...string) []siteDest {
	out := make([]siteDest, 0, len(roots))
	for _, r := range roots {
		out = append(out, siteDest{install: detect.Install{Framework: "wordpress", Root: r}, destRoot: r})
	}
	return out
}

func byID(results []check.Result, id string) *check.Result {
	for i := range results {
		if results[i].ID == id {
			return &results[i]
		}
	}
	return nil
}

func TestMigrateRequiresDestination(t *testing.T) {
	file := filepath.Join(t.TempDir(), "migrate.yaml")
	yaml := "version: 1\nsource:\n  host: source.example.com\n"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "migrate", "-f", file)
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Errorf("migrate without a destination should say so, got: %v", err)
	}
}

func TestDestStateEmptyDocrootIsOK(t *testing.T) {
	r := scriptRunner{} // nothing non-empty, no history
	got, err := destStateResults(context.Background(), r, "/home/d", sites("/home/d/public_html"), false)
	if err != nil {
		t.Fatalf("destStateResults: %v", err)
	}
	res := byID(got, destIDPrefix+"/home/d/public_html")
	if res == nil || res.Severity != check.Ok {
		t.Fatalf("empty docroot should be OK, got %+v", got)
	}
	if !strings.Contains(res.Detail, "empty or absent") {
		t.Errorf("detail should explain it will be created, got %q", res.Detail)
	}
}

func TestDestStateNonEmptyWithoutHistoryRefuses(t *testing.T) {
	r := scriptRunner{nonEmpty: map[string]bool{"/home/d/public_html": true}}
	got, err := destStateResults(context.Background(), r, "/home/d", sites("/home/d/public_html"), false)
	if err != nil {
		t.Fatalf("destStateResults: %v", err)
	}
	res := byID(got, destIDPrefix+"/home/d/public_html")
	if res == nil || res.Severity != check.Blocker {
		t.Fatalf("non-empty docroot rehost did not create should block, got %+v", got)
	}
	if !strings.Contains(res.Detail, "--onto-existing") {
		t.Errorf("blocker should name the override flag, got %q", res.Detail)
	}
}

func TestDestStateNonEmptyWithMigrateRecordAllowed(t *testing.T) {
	history := `{"time":"2026-07-28T10:00:00Z","event":"migrate","site":"/home/d/public_html"}` + "\n"
	r := scriptRunner{nonEmpty: map[string]bool{"/home/d/public_html": true}, history: history}
	got, err := destStateResults(context.Background(), r, "/home/d", sites("/home/d/public_html"), false)
	if err != nil {
		t.Fatalf("destStateResults: %v", err)
	}
	res := byID(got, destIDPrefix+"/home/d/public_html")
	if res == nil || res.Severity != check.Ok {
		t.Fatalf("a docroot rehost migrated before should be an OK rerun, got %+v", got)
	}
	if !strings.Contains(res.Detail, "idempotent rerun") {
		t.Errorf("detail should call it an idempotent rerun, got %q", res.Detail)
	}
}

func TestDestStateNonEmptyOntoExistingWarns(t *testing.T) {
	r := scriptRunner{nonEmpty: map[string]bool{"/home/d/public_html": true}}
	got, err := destStateResults(context.Background(), r, "/home/d", sites("/home/d/public_html"), true)
	if err != nil {
		t.Fatalf("destStateResults: %v", err)
	}
	res := byID(got, destIDPrefix+"/home/d/public_html")
	if res == nil || res.Severity != check.Warning {
		t.Fatalf("--onto-existing should downgrade the refusal to a warning, got %+v", got)
	}
	if !strings.Contains(res.Detail, "rollback") {
		t.Errorf("warning should mention the missing rollback, got %q", res.Detail)
	}
}

func TestBuildPreflightBlockersPointAtCheck(t *testing.T) {
	checks := []check.Result{{ID: "php.version", Title: "PHP", Severity: check.Blocker, Detail: "too old"}}
	view, err := buildPreflight(checks, nil)
	if err == nil || !strings.Contains(err.Error(), "rehost check") {
		t.Fatalf("compatibility blockers should point at rehost check, got: %v", err)
	}
	if view.Passed {
		t.Error("view must not be marked passed when a blocker exists")
	}
}

func TestBuildPreflightRefusalNamesRootAndFlag(t *testing.T) {
	destState := []check.Result{{
		ID: destIDPrefix + "/home/d/public_html", Title: "Destination docroot",
		Severity: check.Blocker, Detail: "not empty",
	}}
	view, err := buildPreflight(nil, destState)
	if err == nil {
		t.Fatal("a destination-state blocker should refuse")
	}
	if !strings.Contains(err.Error(), "/home/d/public_html") || !strings.Contains(err.Error(), "--onto-existing") {
		t.Errorf("refusal should name the docroot and the flag, got: %v", err)
	}
	if view.Passed {
		t.Error("view must not be marked passed on a refusal")
	}
}

func TestBuildPreflightGreenStopsHonestly(t *testing.T) {
	checks := []check.Result{{ID: "php.version", Severity: check.Ok}}
	destState := []check.Result{{ID: destIDPrefix + "/home/d/pub", Severity: check.Ok}}
	view, err := buildPreflight(checks, destState)
	if err != nil {
		t.Fatalf("a green pre-flight should not error inside buildPreflight: %v", err)
	}
	if !view.Passed || view.Notice == "" {
		t.Errorf("green pre-flight should be passed with an honest-stop notice, got %+v", view)
	}
	if len(view.Results) != 2 {
		t.Errorf("view should carry all results, got %d", len(view.Results))
	}
}

func TestMigratePreflightJSONEnvelope(t *testing.T) {
	view := tui.MigratePreflightView{
		Results: []check.Result{
			{ID: "php.version", Title: "PHP", Severity: check.Ok, Detail: "8.3"},
			{ID: destIDPrefix + "/home/d/pub", Title: "Destination docroot", Severity: check.Warning, Detail: "onto-existing"},
		},
		Passed: true,
		Notice: preflightNotice,
	}
	var buf bytes.Buffer
	if err := tui.New(tui.ModeJSON, &buf).MigratePreflight(view); err != nil {
		t.Fatalf("MigratePreflight: %v", err)
	}
	var env tui.MigratePreflightEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.migrate-preflight.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if !env.Passed || env.Blockers != 0 || env.Warnings != 1 || len(env.Results) != 2 {
		t.Errorf("envelope summary wrong: %+v", env)
	}
	if env.Notice == "" {
		t.Error("passed envelope should carry the honest-stop notice")
	}
}

func TestMigrateSitesMapsDestRoots(t *testing.T) {
	f := &project.File{
		Version: project.SchemaVersion,
		Source:  project.Host{Host: "src.example.com"},
		Sites: []project.Site{
			{Framework: "wordpress", Root: "/home/u/public_html", DestRoot: "/home/d/www"},
			{Framework: "drupal", Root: "/home/u/drupal"}, // no dest_root: defaults to Root
		},
	}
	installs := []detect.Install{
		{Framework: "wordpress", Root: "/home/u/public_html"},
		{Framework: "drupal", Root: "/home/u/drupal"},
		{Framework: "static", Root: "/home/u/orphan"}, // no project entry: defaults to its own root
	}
	got := migrateSites(f, installs)
	want := map[string]string{
		"/home/u/public_html": "/home/d/www",
		"/home/u/drupal":      "/home/u/drupal",
		"/home/u/orphan":      "/home/u/orphan",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sites, want %d", len(got), len(want))
	}
	for _, s := range got {
		if want[s.install.Root] != s.destRoot {
			t.Errorf("site %s → dest %q, want %q", s.install.Root, s.destRoot, want[s.install.Root])
		}
	}
}
