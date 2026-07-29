package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func TestDestDBCredentialsPromptsOncePerIdentity(t *testing.T) {
	shared := &project.SiteDB{Name: "u1_db", User: "u1"}
	sites := []siteDest{
		{install: wpInstall("/home/u/a"), destDB: shared},
		{install: wpInstall("/home/u/b"), destDB: shared},
		{install: wpInstall("/home/u/c")}, // no dest_db
	}
	var prompts []string
	creds, err := destDBCredentials(sites, func(p string) (string, error) {
		prompts = append(prompts, p)
		return "hunter2", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "u1@u1_db") {
		t.Errorf("one prompt per identity, naming it: %v", prompts)
	}
	if creds["/home/u/a"].Password != "hunter2" || creds["/home/u/b"].Password != "hunter2" {
		t.Errorf("both sites should carry the prompted password: %+v", creds)
	}
	if _, ok := creds["/home/u/c"]; ok {
		t.Error("a site without dest_db must not get credentials")
	}
}

func TestDestDBCredentialsPromptFailureGuides(t *testing.T) {
	sites := []siteDest{{install: wpInstall("/home/u/a"), destDB: &project.SiteDB{Name: "d"}}}
	_, err := destDBCredentials(sites, func(string) (string, error) {
		return "", errors.New("no terminal")
	})
	if err == nil || !strings.Contains(err.Error(), "interactively") {
		t.Errorf("prompt failure should guide toward an interactive run, got %v", err)
	}
}

func TestDestDBResultsPolicy(t *testing.T) {
	stubInspect(t, map[string]*db.Inspection{
		"reachable": {Connected: true, ServerVersion: "8.0.36"},
		"occupied":  {Connected: true, Tables: 12},
		"broken":    {Connected: false, Reason: "Access denied"},
	})
	cases := []struct {
		name     string
		dbName   string
		migrated bool
		onto     bool
		severity check.Severity
		detail   string
	}{
		{"no dest_db", "", false, false, check.Warning, "NOT be migrated"},
		{"reachable empty", "reachable", false, false, check.Ok, "reachable"},
		{"unreachable", "broken", false, false, check.Blocker, "Access denied"},
		{"occupied unclaimed", "occupied", false, false, check.Blocker, "--onto-existing"},
		{"occupied onto-existing", "occupied", false, true, check.Warning, "no rollback"},
		{"occupied rerun", "occupied", true, false, check.Ok, "re-converges"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := siteDest{install: wpInstall("/home/u/site"), destRoot: "/home/d/site"}
			creds := map[string]*db.Credentials{}
			if c.dbName != "" {
				s.destDB = &project.SiteDB{Name: c.dbName}
				creds["/home/u/site"] = &db.Credentials{Name: c.dbName}
			}
			migrated := map[string]bool{}
			if c.migrated {
				migrated["/home/d/site"] = true
			}
			rows, err := destDBResults(context.Background(), &fakeConn{}, []siteDest{s}, creds, migrated, c.onto)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Severity != c.severity || !strings.Contains(rows[0].Detail, c.detail) {
				t.Errorf("row = %+v, want severity %v containing %q", rows, c.severity, c.detail)
			}
		})
	}
}

func TestRunSyncDatabaseChoreography(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	stateDir := t.TempDir()
	p := dbPlan(source, dest, stateDir)
	syncCalls := stubSync(t)
	stubDump(t, "INSERT INTO `opts` VALUES ('/home/u/public_html/uploads');\n-- Dump completed on 2026-07-29\n")
	imported := stubImport(t, &db.ImportResult{SourceTables: 3, DestTables: 3}, nil)

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, p)
	if !errors.Is(err, errMigrateIncomplete) {
		t.Fatalf("got %v", err)
	}
	// Bulk + delta pass per site.
	if len(*syncCalls) != 2 {
		t.Errorf("expected bulk+delta sync calls, got %d", len(*syncCalls))
	}
	// The imported dump was rewritten: source docroot → destination docroot.
	if len(*imported) != 1 {
		t.Fatalf("expected 1 import, got %d", len(*imported))
	}
	sql := gunzipFile(t, (*imported)[0])
	if !strings.Contains(sql, "'/home/d/www/uploads'") || strings.Contains(sql, "/home/u/public_html") {
		t.Errorf("dump should have been rewritten before import:\n%s", sql)
	}
	if !strings.Contains(sql, "-- Dump completed") {
		t.Errorf("rewrite must preserve the completion footer:\n%s", sql)
	}
	// Maintenance window on the source: lock records + toggles, then off.
	srcCmds := recordsFor(source.runs)
	if !strings.Contains(srcCmds, `"event":"maintenance"`) {
		t.Errorf("maintenance records missing on the source:\n%s", srcCmds)
	}
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("bad JSON: %v", jerr)
	}
	d := env.Sites[0].DB
	if d == nil || d.Name != "u1_wp" || d.SourceTables != 3 || d.DestTables != 3 || d.Err != "" {
		t.Errorf("db result = %+v", d)
	}
	if d.Replacements == 0 {
		t.Errorf("docroot rewrite should be reported: %+v", d)
	}
}

func TestRunSyncImportFailureStopsAndLiftsMaintenance(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	p := dbPlan(source, dest, t.TempDir())
	stubSync(t)
	stubDump(t, "-- Dump completed\n")
	stubImport(t, nil, errors.New("mysql import of u1_wp failed: table is full"))

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, p)
	if err == nil || !strings.Contains(err.Error(), "table is full") {
		t.Fatalf("import failure should surface, got %v", err)
	}
	// Maintenance was lifted anyway (wp-cli deactivate or file removal ran
	// after the failure) and the unlock record was written.
	srcCmds := recordsFor(source.runs)
	if !strings.Contains(srcCmds, "maintenance-mode deactivate") && !strings.Contains(srcCmds, "rm -f") {
		t.Errorf("maintenance must be lifted on the failure path:\n%s", srcCmds)
	}
	if !strings.Contains(srcCmds, `"state":"off"`) {
		t.Errorf("the unlock record should be written:\n%s", srcCmds)
	}
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("bad JSON: %v", jerr)
	}
	if d := env.Sites[0].DB; d == nil || d.Err == "" {
		t.Errorf("the site row should carry the db error: %+v", env.Sites[0])
	}
}

func TestRunSyncNoDestDBIsSkippedNotFailed(t *testing.T) {
	source, dest := &fakeConn{}, &fakeConn{}
	p := dbPlan(source, dest, t.TempDir())
	p.destCreds = nil // dest_db never configured
	stubSync(t)

	var buf bytes.Buffer
	err := runSync(context.Background(), testUI(tui.ModeJSON, &buf),
		tui.MigratePreflightView{Passed: true}, p)
	if !errors.Is(err, errMigrateIncomplete) {
		t.Fatalf("files-only migration should still converge, got %v", err)
	}
	var env tui.MigrateEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("bad JSON: %v", jerr)
	}
	if d := env.Sites[0].DB; d == nil || !strings.Contains(d.Skipped, "no dest_db") {
		t.Errorf("the row should say the database was skipped: %+v", env.Sites[0].DB)
	}
}

// --- fixtures ---

func wpInstall(root string) detect.Install {
	return detect.Install{Framework: "wordpress", Root: root}
}

// dbPlan is a one-site plan with the database seams populated.
func dbPlan(source, dest *fakeConn, stateDir string) migratePlan {
	root := "/home/u/public_html"
	return migratePlan{
		source:    source,
		dest:      dest,
		srcStream: nopStreamer{},
		destConn:  dest,
		srcHost:   db.Host{Run: source},
		srcCreds:  map[string]*db.Credentials{root: {Name: "wp_src", User: "u", Password: "pw"}},
		destCreds: map[string]*db.Credentials{root: {Name: "u1_wp", User: "u1", Password: "pw2"}},
		sites: []siteDest{
			{install: wpInstall(root), destRoot: "/home/d/www", destDB: &project.SiteDB{Name: "u1_wp"}},
		},
		srcMysqldump: true,
		srcTarget:    "u@src",
		destTarget:   "d@dst",
		srcHome:      "/home/u",
		destHome:     "/home/d",
		stateDir:     stateDir,
	}
}

type nopStreamer struct{}

func (nopStreamer) Stream(context.Context, string, io.Writer) (ssh.Result, error) {
	return ssh.Result{}, nil
}

// stubDump replaces both dump paths with one writing gzipped sql.
func stubDump(t *testing.T, sql string) {
	t.Helper()
	prevDump, prevPHP := dumpFn, dumpPHPFn
	t.Cleanup(func() { dumpFn, dumpPHPFn = prevDump, prevPHP })
	fake := func(_ context.Context, _ db.Streamer, _ *db.Credentials, w io.Writer) (*db.DumpStats, error) {
		gz := gzip.NewWriter(w)
		if _, err := gz.Write([]byte(sql)); err != nil {
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
		return &db.DumpStats{Bytes: int64(len(sql)), FooterOK: true}, nil
	}
	dumpFn, dumpPHPFn = fake, fake
}

// stubImport captures dump paths handed to the import and returns canned
// results.
func stubImport(t *testing.T, res *db.ImportResult, err error) *[]string {
	t.Helper()
	prev := importFn
	t.Cleanup(func() { importFn = prev })
	var paths []string
	importFn = func(_ context.Context, _ db.Conn, _ *db.Credentials, dumpPath string, _ db.ImportOptions) (*db.ImportResult, error) {
		paths = append(paths, dumpPath)
		return res, err
	}
	return &paths
}

func stubInspect(t *testing.T, byName map[string]*db.Inspection) {
	t.Helper()
	prev := inspectFn
	t.Cleanup(func() { inspectFn = prev })
	inspectFn = func(_ context.Context, _ db.Runner, c *db.Credentials) (*db.Inspection, error) {
		if insp, ok := byName[c.Name]; ok {
			return insp, nil
		}
		return nil, fmt.Errorf("unexpected inspect of %s", c.Name)
	}
}

func gunzipFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
