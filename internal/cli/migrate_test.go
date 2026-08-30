package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/go-transfer"
	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// scriptRunner is a fake stateRunner that answers the two commands
// destStateResults issues — the guarded `ls -A` docroot stat and the history
// `cat` — so the destination-state policy is exercised without a real SSH
// connection. A docroot listed in nonEmpty comes back with entries; one listed
// in unreadable exists but fails to list; every other docroot is absent.
// history is the raw history.jsonl content (empty = no file).
type scriptRunner struct {
	nonEmpty   map[string]bool
	unreadable map[string]bool
	history    string
}

func (s scriptRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	switch {
	case strings.Contains(cmd, "ls -A"):
		for root, ne := range s.nonEmpty {
			if ne && strings.Contains(cmd, root) {
				return ssh.Result{Stdout: "index.php\nwp-content\n"}, nil
			}
		}
		for root, u := range s.unreadable {
			if u && strings.Contains(cmd, root) {
				return ssh.Result{ExitCode: 2, Stderr: "ls: cannot open directory: Permission denied\n"}, nil
			}
		}
		return ssh.Result{ExitCode: docrootAbsentExit}, nil
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
	got, err := destStateResults(context.Background(), r, sites("/home/d/public_html"), nil, false)
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
	got, err := destStateResults(context.Background(), r, sites("/home/d/public_html"), nil, false)
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
	r := scriptRunner{nonEmpty: map[string]bool{"/home/d/public_html": true}}
	migrated := map[string]bool{"/home/d/public_html": true}
	got, err := destStateResults(context.Background(), r, sites("/home/d/public_html"), migrated, false)
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
	got, err := destStateResults(context.Background(), r, sites("/home/d/public_html"), nil, true)
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

// fakeConn is a transfer.Conn (and, via Run, a state.runner) that records the
// commands it receives — the history writes runSync issues land here — and can
// be told to fail every Run to simulate a host that cannot record state.
type fakeConn struct {
	runs        []string
	runErr      error
	catStdout   string // response to `cat -- <path>` reads
	crontab     string // response to `crontab -l`
	crontabExit int
}

func (f *fakeConn) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.runs = append(f.runs, cmd)
	if f.runErr != nil {
		return ssh.Result{}, f.runErr
	}
	switch {
	case strings.HasPrefix(cmd, "cat -- "):
		return ssh.Result{Stdout: f.catStdout}, nil
	case strings.HasPrefix(cmd, "crontab -l"):
		return ssh.Result{Stdout: f.crontab, ExitCode: f.crontabExit}, nil
	}
	return ssh.Result{ExitCode: 0}, nil
}

func (f *fakeConn) StreamPipe(_ context.Context, _ string, _ io.Reader, _ io.Writer) (ssh.Result, error) {
	return ssh.Result{ExitCode: 0}, nil
}

func testUI(mode tui.Mode, w io.Writer) ui {
	return ui{mode: mode, renderer: tui.New(mode, w), progress: func(string, ...any) {}}
}

// stubSync swaps the sync primitive for one that records every call and returns
// canned stats/errors, restoring the real engine when the test ends.
type syncCall struct {
	src, dst transfer.Endpoint
	excludes []string
	opts     transfer.Options
}

type syncOutcome struct {
	stats *transfer.SyncStats
	err   error
}

func stubSync(t *testing.T, results ...syncOutcome) *[]syncCall {
	t.Helper()
	prev := syncFn
	t.Cleanup(func() { syncFn = prev })
	var calls []syncCall
	syncFn = func(_ context.Context, src, dst transfer.Endpoint, excludes []string, opts transfer.Options, _ func(string)) (*transfer.SyncStats, error) {
		i := len(calls)
		calls = append(calls, syncCall{src: src, dst: dst, excludes: excludes, opts: opts})
		if i < len(results) {
			return results[i].stats, results[i].err
		}
		return &transfer.SyncStats{}, nil
	}
	return &calls
}

func twoSitePlan(source, dest transfer.Conn, stateDir string) migratePlan {
	return migratePlan{
		source: source,
		dest:   dest,
		sites: []siteDest{
			{install: detect.Install{Framework: "wordpress", Root: "/home/u/public_html"}, destRoot: "/home/d/www"},
			{install: detect.Install{Framework: "drupal", Root: "/home/u/drupal"}, destRoot: "/home/d/drupal"},
		},
		delete:     true,
		compress:   true,
		nullList:   true,
		srcTarget:  "u@src",
		destTarget: "d@dst",
		srcHome:    "/home/u",
		destHome:   "/home/d",
		stateDir:   stateDir,
	}
}

func TestSyncOptionsGatingBothWays(t *testing.T) {
	gnuTar := map[string]ssh.Tool{"tar": {Found: true, Version: "tar (GNU tar) 1.34"}, "gzip": {Found: true}}
	bsdTar := map[string]ssh.Tool{"tar": {Found: true, Version: "bsdtar 3.5.1"}, "gzip": {Found: true}}
	noGzip := map[string]ssh.Tool{"tar": {Found: true, Version: "tar (GNU tar) 1.34"}}

	bareTar := map[string]ssh.Tool{"tar": {Found: true}, "gzip": {Found: true}}

	cases := []struct {
		name           string
		src, dst       map[string]ssh.Tool
		compress, null bool
	}{
		{"both gzip + gnu source", gnuTar, gnuTar, true, true},
		{"dest lacks gzip", gnuTar, noGzip, false, true},
		{"source lacks gzip", noGzip, gnuTar, false, true},
		{"non-gnu source tar", bsdTar, gnuTar, true, false},
		{"tar without version banner is non-gnu", bareTar, gnuTar, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			compress, null := syncOptions(&ssh.Capabilities{Tools: c.src}, &ssh.Capabilities{Tools: c.dst})
			if compress != c.compress || null != c.null {
				t.Errorf("syncOptions = (compress %v, null %v), want (%v, %v)",
					compress, null, c.compress, c.null)
			}
		})
	}
}

func TestRunSyncInvokesSyncWithEndpointsAndOptions(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	stateDir := filepath.Join(t.TempDir(), ".rehost")
	calls := stubSync(t) // default: succeed with empty stats

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(source, dest, stateDir))
	if err != nil {
		t.Fatalf("a converged sync should exit clean, got %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 sync calls, got %d", len(*calls))
	}
	c0 := (*calls)[0]
	if c0.src.Root != "/home/u/public_html" || c0.dst.Root != "/home/d/www" {
		t.Errorf("site 1 endpoints wrong: src %q dst %q", c0.src.Root, c0.dst.Root)
	}
	if c0.src.Conn != source || c0.dst.Conn != dest {
		t.Error("endpoints must carry the source/destination connections from the plan")
	}
	if !c0.opts.Delete || !c0.opts.Compress || !c0.opts.NullList {
		t.Errorf("options not passed through: %+v", c0.opts)
	}
	wantManifest := filepath.Join(stateDir, "manifests", transfer.DestManifestFilename("d@dst", "/home/d/www"))
	if c0.opts.DestManifestPath != wantManifest {
		t.Errorf("DestManifestPath = %q, want %q", c0.opts.DestManifestPath, wantManifest)
	}
	// Excludes must match dry-run's per-site computation so deltas line up.
	want := recipe.ExcludeSuggestionsFor(detect.Install{Framework: "wordpress"})
	if strings.Join(c0.excludes, ",") != strings.Join(want, ",") {
		t.Errorf("site 1 excludes = %v, want %v", c0.excludes, want)
	}
}

// recordsFor joins the commands a fakeConn received; state.Record embeds the
// JSON history line in the command, so the joined text is searchable.
func recordsFor(runs []string) string { return strings.Join(runs, "\n") }

func TestRunSyncPerSiteStatsInReport(t *testing.T) {
	stats0 := &transfer.SyncStats{Compressed: true, FilesSent: 42, BytesSent: 1000, WireBytes: 400, FilesDeleted: 3, Duration: time.Second}
	stats1 := &transfer.SyncStats{FilesSent: 7, BytesSent: 200, DestOnlyRemaining: 1, UnsafePaths: 1}
	stubSync(t, syncOutcome{stats0, nil}, syncOutcome{stats1, nil})

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(&fakeConn{}, &fakeConn{}, t.TempDir()))
	if err != nil {
		t.Fatalf("a converged run should exit clean, got %v", err)
	}
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", jerr, buf.String())
	}
	if env.Schema != "rehost.migrate.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if !env.Complete {
		t.Error("a converged run should report complete")
	}
	if env.Notice == "" || !strings.Contains(env.Notice, "rehost cutover") {
		t.Errorf("envelope should point at the cutover checklist, got %q", env.Notice)
	}
	if !env.Preflight.Passed {
		t.Error("preflight section should report passed")
	}
	if len(env.Sites) != 2 {
		t.Fatalf("expected 2 site rows, got %d", len(env.Sites))
	}
	if env.Sites[0].FilesSent != 42 || env.Sites[0].WireBytes != 400 || !env.Sites[0].Compressed || env.Sites[0].FilesDeleted != 3 {
		t.Errorf("site 0 stats not carried: %+v", env.Sites[0])
	}
	if env.Sites[1].DestOnlyRemaining != 1 || env.Sites[1].UnsafePaths != 1 {
		t.Errorf("site 1 stats not carried: %+v", env.Sites[1])
	}
}

func TestRunSyncRecordsHistoryAfterSuccess(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	stubSync(t)
	var buf bytes.Buffer
	if err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(source, dest, t.TempDir())); err != nil {
		t.Fatalf("got %v", err)
	}
	// One EventMigrate per site on the destination, each naming its dest root —
	// the record the next run's destination-state policy matches on.
	destRecords := recordsFor(dest.runs)
	if !strings.Contains(destRecords, "/home/d/www") || !strings.Contains(destRecords, "/home/d/drupal") {
		t.Errorf("destination history missing per-site migrate records:\n%s", destRecords)
	}
	if !strings.Contains(destRecords, `"event":"migrate"`) {
		t.Errorf("destination records should be migrate events:\n%s", destRecords)
	}
	// A summary record on the source.
	if src := recordsFor(source.runs); !strings.Contains(src, `"event":"migrate"`) || !strings.Contains(src, "files_sent") {
		t.Errorf("source should carry a summary migrate record:\n%s", src)
	}
	var env tui.MigrateEnvelope
	_ = json.Unmarshal(buf.Bytes(), &env)
	if len(env.Warnings) != 0 {
		t.Errorf("a clean run should have no warnings, got %v", env.Warnings)
	}
}

func TestRunSyncDestHistoryFailureIsProminentWarning(t *testing.T) {
	source := &fakeConn{}
	dest := &fakeConn{runErr: errors.New("read-only home")}
	stubSync(t)
	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(source, dest, t.TempDir()))
	if err != nil {
		t.Fatalf("a failed history write must not fail the migration, got %v", err)
	}
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("bad JSON: %v", jerr)
	}
	if len(env.Warnings) == 0 {
		t.Fatal("a failed destination record should surface a warning")
	}
	joined := strings.Join(env.Warnings, "\n")
	if !strings.Contains(joined, "--onto-existing") || !strings.Contains(joined, "/home/d/www") {
		t.Errorf("warning should name the docroot and warn the next run needs --onto-existing:\n%s", joined)
	}
}

func TestRunSyncStopsOnFirstSiteFailure(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	boom := errors.New("destination extract failed")
	calls := stubSync(t, syncOutcome{&transfer.SyncStats{FilesSent: 5}, boom})

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(source, dest, t.TempDir()))
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("a sync failure should surface, got %v", err)
	}
	if len(*calls) != 1 {
		t.Errorf("sync must stop before site 2, got %d calls", len(*calls))
	}
	if len(dest.runs) != 0 || len(source.runs) != 0 {
		t.Error("a failed run must not record history on either host")
	}
	// The partial report still names the failed site.
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("bad JSON: %v", jerr)
	}
	if len(env.Sites) != 1 || env.Sites[0].Err == "" {
		t.Errorf("failed site should be reported with its error: %+v", env.Sites)
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
		{Framework: "static", Root: "/home/u/orphan"}, // no project entry: rebased onto the dest home
	}
	got := migrateSites(f, installs, "/home/u", "/home/d")
	want := map[string]string{
		"/home/u/public_html": "/home/d/www",    // explicit dest_root wins
		"/home/u/drupal":      "/home/d/drupal", // project entry without dest_root: rebased
		"/home/u/orphan":      "/home/d/orphan", // no project entry: rebased
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

func TestMapDestRoot(t *testing.T) {
	cases := []struct {
		name, srcRoot, srcHome, destHome, want string
	}{
		{"under home", "/home/alice/public_html", "/home/alice", "/home/bob", "/home/bob/public_html"},
		{"nested under home", "/home/alice/sites/blog", "/home/alice", "/home/bob", "/home/bob/sites/blog"},
		{"home itself", "/home/alice", "/home/alice", "/home/bob", "/home/bob"},
		{"outside home keeps its path", "/var/www/site", "/home/alice", "/home/bob", "/var/www/site"},
		{"same account", "/home/alice/public_html", "/home/alice", "/home/alice", "/home/alice/public_html"},
		{"trailing slashes", "/home/alice/www", "/home/alice/", "/home/bob/", "/home/bob/www"},
		{"unknown homes", "/home/alice/www", "", "", "/home/alice/www"},
		{"prefix but not path boundary", "/home/alicedata/www", "/home/alice", "/home/bob", "/home/alicedata/www"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mapDestRoot(c.srcRoot, c.srcHome, c.destHome); got != c.want {
				t.Errorf("mapDestRoot(%q, %q, %q) = %q, want %q", c.srcRoot, c.srcHome, c.destHome, got, c.want)
			}
		})
	}
}

func TestDestStateUnlistableDocrootIsAnError(t *testing.T) {
	// An existing docroot whose listing fails must abort, not read as empty:
	// "empty" would wave a live-but-unreadable site past the refusal policy.
	r := scriptRunner{unreadable: map[string]bool{"/home/d/www": true}}
	_, err := destStateResults(context.Background(), r, sites("/home/d/www"), nil, false)
	if err == nil || !strings.Contains(err.Error(), "cannot inspect /home/d/www") {
		t.Errorf("unlistable docroot should be an error, got %v", err)
	}
}

func TestRunSyncRecordsConvergedSitesOnPartialFailure(t *testing.T) {
	// Site 1 converges, site 2 fails: site 1's destination record must exist
	// anyway, or the rerun's destination-state policy refuses rehost's own
	// work and the promised resume-by-rerun breaks.
	source, dest := &fakeConn{}, &fakeConn{}
	boom := errors.New("relay died")
	stubSync(t,
		syncOutcome{&transfer.SyncStats{FilesSent: 3}, nil},
		syncOutcome{nil, boom})

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, twoSitePlan(source, dest, t.TempDir()))
	if !errors.Is(err, boom) {
		t.Fatalf("the site-2 failure should surface, got %v", err)
	}
	destRecords := recordsFor(dest.runs)
	if !strings.Contains(destRecords, "/home/d/www") {
		t.Errorf("converged site 1 must be recorded on the destination:\n%s", destRecords)
	}
	if strings.Contains(destRecords, "/home/d/drupal") {
		t.Errorf("failed site 2 must not be recorded:\n%s", destRecords)
	}
	if src := recordsFor(source.runs); !strings.Contains(src, `"sites":"1"`) {
		t.Errorf("source summary should cover the one converged site:\n%s", src)
	}
}

func TestCheckDestCollisions(t *testing.T) {
	site := func(root, dest string, db *project.SiteDB) siteDest {
		return siteDest{install: detect.Install{Root: root}, destRoot: dest, destDB: db}
	}
	tests := []struct {
		name  string
		sites []siteDest
		want  string
	}{
		{
			name: "distinct destinations pass",
			sites: []siteDest{
				site("/home/u/a", "/home/d/a", &project.SiteDB{Name: "db1"}),
				site("/home/u/b", "/home/d/b", &project.SiteDB{Name: "db2"}),
				site("/home/u/c", "/home/d/c", nil),
				site("/home/u/d", "/home/d/d", nil),
			},
		},
		{
			// The case project.Validate cannot see: an explicit dest_root
			// landing exactly where another site's default rebase goes.
			name: "explicit dest_root collides with a rebased default",
			sites: []siteDest{
				site("/home/u/public_html", "/home/d/public_html", nil),
				site("/home/u/other", "/home/d/public_html", nil),
			},
			want: "both migrate into /home/d/public_html",
		},
		{
			name: "shared destination database",
			sites: []siteDest{
				site("/home/u/a", "/home/d/a", &project.SiteDB{Name: "db123"}),
				site("/home/u/b", "/home/d/b", &project.SiteDB{Name: "db123"}),
			},
			want: "both import into database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkDestCollisions(tt.sites)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}
