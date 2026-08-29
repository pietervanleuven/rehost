package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// unlockRunner scripts the source host for unlockSites: it answers the history
// `cat`, then per-site maintenance probes/disables by command substring. Any
// command with no scripted answer comes back as exit 127 — the "tool/probe
// absent" signal the recipes read.
type unlockRunner struct {
	history string
	results map[string]ssh.Result
	err     error
	calls   []string
}

func (u *unlockRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	u.calls = append(u.calls, cmd)
	if u.err != nil {
		return ssh.Result{}, u.err
	}
	if strings.HasPrefix(cmd, "cat ") && strings.Contains(cmd, "history.jsonl") {
		if u.history == "" {
			return ssh.Result{ExitCode: 1}, nil // no history file yet
		}
		return ssh.Result{Stdout: u.history}, nil
	}
	for sub, res := range u.results {
		if strings.Contains(cmd, sub) {
			return res, nil
		}
	}
	return ssh.Result{ExitCode: 127}, nil
}

func (u *unlockRunner) ran(substr string) bool {
	for _, c := range u.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// lockHistory builds a history.jsonl body recording each root as maintenance-on.
func lockHistory(roots ...string) string {
	var b strings.Builder
	for _, root := range roots {
		line, _ := json.Marshal(state.MaintenanceEntry(root, true))
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func unlockFile(sites ...project.Site) *project.File {
	return &project.File{
		Version: project.SchemaVersion,
		Source:  project.Host{Host: "src.example.com", User: "u"},
		Sites:   sites,
	}
}

func siteRow(v tui.UnlockView, root string) *tui.UnlockSite {
	for i := range v.Sites {
		if v.Sites[i].Site == root {
			return &v.Sites[i]
		}
	}
	return nil
}

func TestUnlockClearsLockedSite(t *testing.T) {
	// Live probe says on; disable via wp-cli then the file cleanup clears it.
	r := &unlockRunner{results: map[string]ssh.Result{
		"test -e":                        {ExitCode: 0}, // .maintenance present → on
		"wp maintenance-mode deactivate": {ExitCode: 0},
		"rm -f":                          {ExitCode: 0},
	}}
	f := unlockFile(project.Site{Framework: "wordpress", Root: "/home/u/pub"})
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("a cleared site must not be in failed: %v", failed)
	}
	row := siteRow(view, "/home/u/pub")
	if row == nil || row.Status != tui.UnlockCleared || row.Method != "wp-cli" {
		t.Fatalf("row = %+v, want cleared via wp-cli", row)
	}
}

func TestUnlockToolFailureDoesNotAbortRun(t *testing.T) {
	// A locked WordPress site whose .maintenance cannot be removed is a
	// per-site failure, not a transport abort: the second site still clears.
	r := &unlockRunner{results: map[string]ssh.Result{
		"test -e":                             {ExitCode: 0},
		"rm -f '/home/u/broken/.maintenance'": {ExitCode: 1, Stderr: "rm: permission denied"},
		"rm -f '/home/u/ok/.maintenance'":     {ExitCode: 0},
	}}
	f := unlockFile(
		project.Site{Framework: "wordpress", Root: "/home/u/broken"},
		project.Site{Framework: "wordpress", Root: "/home/u/ok"},
	)
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("a tool failure must not abort the run: %v", err)
	}
	if len(failed) != 1 || failed[0] != "/home/u/broken" {
		t.Fatalf("failed = %v, want only the broken site", failed)
	}
	broken := siteRow(view, "/home/u/broken")
	if broken == nil || broken.Status != tui.UnlockFailed || !strings.Contains(broken.Detail, "permission denied") {
		t.Fatalf("broken row = %+v, want failed with the rm error", broken)
	}
	ok := siteRow(view, "/home/u/ok")
	if ok == nil || ok.Status != tui.UnlockCleared {
		t.Fatalf("ok row = %+v, want cleared despite the earlier failure", ok)
	}
}

func TestUnlockNothingLocked(t *testing.T) {
	// No history, live probe off: every site reports not-locked and no disable
	// is attempted.
	r := &unlockRunner{results: map[string]ssh.Result{"test -e": {ExitCode: 1}}}
	f := unlockFile(project.Site{Framework: "wordpress", Root: "/home/u/pub"})
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("nothing locked → no failures, got %v", failed)
	}
	row := siteRow(view, "/home/u/pub")
	if row == nil || row.Status != tui.UnlockNotLocked {
		t.Fatalf("row = %+v, want not-locked", row)
	}
	if r.ran("rm -f") || r.ran("maintenance-mode") {
		t.Errorf("no disable should be attempted when nothing is locked: %v", r.calls)
	}
}

func TestUnlockLiveProbeOverridesHistory(t *testing.T) {
	// History says locked but the live probe says off — the probe is trusted and
	// nothing is disabled.
	r := &unlockRunner{
		history: lockHistory("/home/u/pub"),
		results: map[string]ssh.Result{"test -e": {ExitCode: 1}}, // off
	}
	f := unlockFile(project.Site{Framework: "wordpress", Root: "/home/u/pub"})
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("probe-off should win over stale history, got failures %v", failed)
	}
	row := siteRow(view, "/home/u/pub")
	if row == nil || row.Status != tui.UnlockNotLocked {
		t.Fatalf("row = %+v, want not-locked", row)
	}
	if r.ran("rm -f") || r.ran("maintenance-mode") {
		t.Errorf("trusted-off probe must not disable: %v", r.calls)
	}
}

func TestUnlockProbeUnknownWithHistoryDisables(t *testing.T) {
	// Drush cannot read the flag (probe Unknown) but history recorded the site
	// locked, so a disable is still attempted — and here it succeeds via drush.
	r := &unlockRunner{
		history: lockHistory("/home/u/drupal"),
		results: map[string]ssh.Result{
			// state:get is left unscripted (exit 127) → Unknown; the set clears it.
			"state:set system.maintenance_mode 0": {ExitCode: 0},
		},
	}
	f := unlockFile(project.Site{Framework: "drupal", Root: "/home/u/drupal", Version: "10.3.1"})
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("a cleared site must not fail: %v", failed)
	}
	if !r.ran("state:set system.maintenance_mode 0") {
		t.Errorf("unknown probe + history-locked should attempt a disable: %v", r.calls)
	}
	row := siteRow(view, "/home/u/drupal")
	if row == nil || row.Status != tui.UnlockCleared || row.Method != "drush" {
		t.Fatalf("row = %+v, want cleared via drush", row)
	}
}

func TestUnlockUnsupportedSiteFails(t *testing.T) {
	// Drupal history-locked but no drush: the disable is unsupported, so the site
	// is a failed row carrying the note, and it lands in the failed list (the
	// command's non-zero exit).
	r := &unlockRunner{
		history: lockHistory("/home/u/drupal"),
		results: map[string]ssh.Result{},
	}
	f := unlockFile(project.Site{Framework: "drupal", Root: "/home/u/drupal", Version: "10.3.1"})
	view, failed, err := unlockSites(context.Background(),
		recipe.Host{Run: r, Caps: &ssh.Capabilities{Tools: map[string]ssh.Tool{}}}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 1 || failed[0] != "/home/u/drupal" {
		t.Fatalf("unsupported disable should be listed as failed, got %v", failed)
	}
	row := siteRow(view, "/home/u/drupal")
	if row == nil || row.Status != tui.UnlockFailed || !strings.Contains(row.Detail, "drush") {
		t.Fatalf("row = %+v, want failed with a drush note", row)
	}
}

func TestUnlockHistoryRootMissingFromProject(t *testing.T) {
	// A root recorded as locked but absent from the project file cannot be
	// cleared — it becomes a failed row with the recover-by-hand guidance.
	r := &unlockRunner{history: lockHistory("/home/u/ghost")}
	f := unlockFile() // no sites
	view, failed, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}
	if len(failed) != 1 || failed[0] != "/home/u/ghost" {
		t.Fatalf("orphaned locked root should fail, got %v", failed)
	}
	row := siteRow(view, "/home/u/ghost")
	if row == nil || row.Status != tui.UnlockFailed || !strings.Contains(row.Detail, "project file") {
		t.Fatalf("row = %+v, want failed with project-file guidance", row)
	}
}

func TestUnlockTransportFailureAborts(t *testing.T) {
	boom := errors.New("connection lost")
	r := &unlockRunner{err: boom}
	f := unlockFile(project.Site{Framework: "wordpress", Root: "/home/u/pub"})
	_, _, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err == nil || !strings.Contains(err.Error(), "run history") {
		t.Fatalf("a transport failure reading history should abort, got %v", err)
	}
}

func TestUnlockJSONShape(t *testing.T) {
	r := &unlockRunner{results: map[string]ssh.Result{
		"test -e":                        {ExitCode: 0},
		"wp maintenance-mode deactivate": {ExitCode: 0},
		"rm -f":                          {ExitCode: 0},
	}}
	f := unlockFile(project.Site{Framework: "wordpress", Root: "/home/u/pub"})
	view, _, err := unlockSites(context.Background(), recipe.Host{Run: r}, "/home/u", "u@src.example.com", f)
	if err != nil {
		t.Fatalf("unlockSites: %v", err)
	}

	var buf bytes.Buffer
	if err := tui.New(tui.ModeJSON, &buf).UnlockReport(view); err != nil {
		t.Fatalf("UnlockReport: %v", err)
	}
	var env tui.UnlockEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.unlock.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if env.Source != "u@src.example.com" {
		t.Errorf("source = %q", env.Source)
	}
	if env.Locked != 1 || env.Cleared != 1 || env.Failed != 0 {
		t.Errorf("tally wrong: locked=%d cleared=%d failed=%d", env.Locked, env.Cleared, env.Failed)
	}
	if len(env.Sites) != 1 || env.Sites[0].Status != tui.UnlockCleared {
		t.Errorf("sites = %+v", env.Sites)
	}
}
