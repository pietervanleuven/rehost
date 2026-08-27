package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// fakeStateRunner returns one canned result for the history read, standing in
// for an SSH client so these commands are exercised without a real connection.
type fakeStateRunner struct {
	res ssh.Result
	err error
}

func (f fakeStateRunner) Run(context.Context, string) (ssh.Result, error) {
	return f.res, f.err
}

// twoRuns is a history file (oldest-first, as the source stores it) with two
// dry-run entries.
const twoRuns = `{"time":"2026-07-26T10:00:00Z","event":"dry-run","details":{"sites":"1","warnings":"0"}}
{"time":"2026-07-27T11:30:00Z","event":"dry-run","details":{"sites":"2","warnings":"1"}}
`

func TestHistoryAndStatusNeedProjectFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "migrate.yaml")
	for _, cmd := range []string{"history", "status"} {
		_, _, err := run(t, cmd, "-f", missing)
		if err == nil || !strings.Contains(err.Error(), "rehost init") {
			t.Errorf("%s without a project file should point at init, got: %v", cmd, err)
		}
	}
}

func TestHistoryReportJSONNewestFirst(t *testing.T) {
	r := fakeStateRunner{res: ssh.Result{Stdout: twoRuns}}
	var buf bytes.Buffer
	if err := historyReport(context.Background(), r, "/home/u", tui.New(tui.ModeJSON, &buf)); err != nil {
		t.Fatalf("historyReport: %v", err)
	}
	var env tui.HistoryEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.history.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if env.Count != 2 || len(env.Entries) != 2 {
		t.Fatalf("want 2 entries, got count=%d len=%d", env.Count, len(env.Entries))
	}
	// Newest-first: the 07-27 entry must lead.
	if !env.Entries[0].Time.Equal(time.Date(2026, 7, 27, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("entries not newest-first: %+v", env.Entries)
	}
	if env.Entries[0].Details["warnings"] != "1" {
		t.Errorf("details lost in round trip: %+v", env.Entries[0].Details)
	}
}

func TestHistoryReportEmptyIsCleanExit(t *testing.T) {
	// cat on a missing file: non-zero exit, stderr swallowed — the empty case.
	r := fakeStateRunner{res: ssh.Result{ExitCode: 1}}

	var jbuf bytes.Buffer
	if err := historyReport(context.Background(), r, "/home/u", tui.New(tui.ModeJSON, &jbuf)); err != nil {
		t.Fatalf("empty history must not error: %v", err)
	}
	var env tui.HistoryEnvelope
	if err := json.Unmarshal(jbuf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jbuf.String())
	}
	if env.Count != 0 || len(env.Entries) != 0 {
		t.Errorf("empty history should report zero entries, got %+v", env)
	}
	// entries must serialize as [] so consumers can index without a null check.
	if !strings.Contains(jbuf.String(), `"entries": []`) {
		t.Errorf("empty entries should render as [], got:\n%s", jbuf.String())
	}

	var pbuf bytes.Buffer
	if err := historyReport(context.Background(), r, "/home/u", tui.New(tui.ModePlain, &pbuf)); err != nil {
		t.Fatalf("empty history (plain): %v", err)
	}
	if !strings.Contains(pbuf.String(), "no runs recorded yet") {
		t.Errorf("plain empty history should say so, got:\n%s", pbuf.String())
	}
}

func TestHistoryReportSkipsCorruptLine(t *testing.T) {
	stdout := `{"time":"2026-07-26T10:00:00Z","event":"dry-run"}
{"time":"2026-07-26T11:00:00Z","event":"dry-r
{"time":"2026-07-27T11:30:00Z","event":"dry-run"}
`
	r := fakeStateRunner{res: ssh.Result{Stdout: stdout}}
	var buf bytes.Buffer
	if err := historyReport(context.Background(), r, "/home/u", tui.New(tui.ModeJSON, &buf)); err != nil {
		t.Fatalf("historyReport: %v", err)
	}
	var env tui.HistoryEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if env.Count != 2 {
		t.Fatalf("corrupt line should be skipped, not fatal: got %d entries", env.Count)
	}
}

func TestHistoryReportTransportErrorPropagates(t *testing.T) {
	boom := errors.New("connection lost")
	r := fakeStateRunner{err: boom}
	err := historyReport(context.Background(), r, "/home/u", tui.New(tui.ModeJSON, &bytes.Buffer{}))
	if !errors.Is(err, boom) {
		t.Errorf("transport error should propagate, got %v", err)
	}
}

func TestStatusReportJSON(t *testing.T) {
	f := &project.File{
		Version:     project.SchemaVersion,
		Source:      project.Host{Host: "src.example.com", User: "deploy"},
		Destination: &project.Host{Host: "dst.example.com", User: "deploy"},
		Sites: []project.Site{
			{Framework: "wordpress", Root: "/home/u/public_html", Version: "6.5.2"},
		},
	}
	r := fakeStateRunner{res: ssh.Result{Stdout: twoRuns}}
	var buf bytes.Buffer
	if err := statusReport(context.Background(), r, "/home/u", "migrate.yaml", f, tui.New(tui.ModeJSON, &buf)); err != nil {
		t.Fatalf("statusReport: %v", err)
	}
	var env tui.StatusEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.status.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if env.Source != "deploy@src.example.com" || env.Destination != "deploy@dst.example.com" {
		t.Errorf("hosts = %q / %q", env.Source, env.Destination)
	}
	if len(env.Sites) != 1 || env.Sites[0].Framework != "wordpress" {
		t.Errorf("sites = %+v", env.Sites)
	}
	if env.LastDryRun == nil || env.LastDryRun.Details["sites"] != "2" {
		t.Errorf("last_dry_run should be the newest run, got %+v", env.LastDryRun)
	}
	if len(env.RecentRuns) != 2 {
		t.Errorf("recent_runs = %+v", env.RecentRuns)
	}
}

func TestStatusReportEmptyHistoryJSON(t *testing.T) {
	f := &project.File{Version: project.SchemaVersion, Source: project.Host{Host: "src.example.com"}}
	r := fakeStateRunner{res: ssh.Result{ExitCode: 1}} // no history file yet
	var buf bytes.Buffer
	if err := statusReport(context.Background(), r, "/home/u", "migrate.yaml", f, tui.New(tui.ModeJSON, &buf)); err != nil {
		t.Fatalf("statusReport: %v", err)
	}
	var env tui.StatusEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if env.LastDryRun != nil {
		t.Errorf("no runs recorded: last_dry_run should be nil, got %+v", env.LastDryRun)
	}
	if env.Sites == nil || env.RecentRuns == nil {
		t.Error("sites and recent_runs must serialize as [], never null")
	}
	if !strings.Contains(buf.String(), `"sites": []`) || !strings.Contains(buf.String(), `"recent_runs": []`) {
		t.Errorf("empty slices should render as [], got:\n%s", buf.String())
	}
}

func TestBuildStatusViewCapsRecentAndMapsSites(t *testing.T) {
	f := &project.File{
		Version: project.SchemaVersion,
		Source:  project.Host{Host: "src.example.com", User: "deploy"},
		Sites: []project.Site{
			{Framework: "drupal", Root: "/home/u/d", Version: "10.3.1"},
		},
	}
	// More runs than the cap; buildStatusView keeps only the newest maxRecentRuns.
	var recent []state.Entry
	for i := 0; i < maxRecentRuns+3; i++ {
		recent = append(recent, state.Entry{Event: "dry-run", Time: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	v := buildStatusView("migrate.yaml", f, recent)

	if v.Source != "deploy@src.example.com" {
		t.Errorf("source = %q", v.Source)
	}
	if v.Destination != "" {
		t.Errorf("no destination configured, want empty, got %q", v.Destination)
	}
	if len(v.Sites) != 1 || v.Sites[0].Framework != "drupal" || v.Sites[0].Version != "10.3.1" {
		t.Errorf("sites = %+v", v.Sites)
	}
	if len(v.Recent) != maxRecentRuns {
		t.Errorf("recent runs should be capped at %d, got %d", maxRecentRuns, len(v.Recent))
	}
}

func TestNewestFirstReverses(t *testing.T) {
	in := []state.Entry{
		{Event: "a", Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Event: "b", Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{Event: "c", Time: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}
	got := newestFirst(in)
	if got[0].Event != "c" || got[2].Event != "a" {
		t.Errorf("newestFirst did not reverse: %+v", got)
	}
}
