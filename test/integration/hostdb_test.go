//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
	searchreplace "github.com/pietervanleuven/go-searchreplace"
)

// tableCount reads the table count of a database straight from the server,
// out of band from the code under test — the independent measurement a
// restore is compared against.
func (r *rig) tableCount(database string) int {
	r.t.Helper()
	// Unfiltered by TABLE_TYPE on purpose: Inspect counts every row
	// information_schema.TABLES holds for the schema, views included, and an
	// independent measurement is only useful if it counts the same things.
	// baseTableCount is the other definition — the one Dump and Import use.
	var sql string
	if r.env == "pgsql" {
		sql = "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"
	} else {
		sql = "SELECT COUNT(*) FROM information_schema.TABLES WHERE table_schema='" + database + "'"
	}
	out := strings.TrimSpace(r.query(database, sql))
	n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(out, "\n")))
	if err != nil {
		r.t.Fatalf("parsing table count from %q: %v", out, err)
	}
	return n
}

// baseTableCount counts only base tables — the definition Dump's CREATE TABLE
// tally and Import's post-import verification both use. Views make the two
// counts differ, so mixing them up produces a test that fails for no reason.
func (r *rig) baseTableCount(database string) int {
	r.t.Helper()
	var sql string
	if r.env == "pgsql" {
		sql = "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'"
	} else {
		sql = "SELECT COUNT(*) FROM information_schema.TABLES WHERE table_schema='" + database +
			"' AND TABLE_TYPE='BASE TABLE'"
	}
	out := strings.TrimSpace(r.query(database, sql))
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		r.t.Fatalf("parsing base table count from %q: %v", out, err)
	}
	return n
}

func TestMySQLEnvironment(t *testing.T) {
	r := startRig(t, "mysql")
	caps := r.probe()

	t.Run("probe finds the shared-hosting toolchain", func(t *testing.T) {
		for _, tool := range []string{"mysql", "mysqldump", "php", "tar", "gzip", "find"} {
			if !caps.Has(tool) {
				t.Errorf("probe did not find %s (a noisy MOTD is configured on purpose — parsing must survive it)", tool)
			}
		}
		tools := hostdb.ResolveClientTools("mysql", caps.Has)
		if tools.Client != "mysql" || tools.Dump != "mysqldump" {
			t.Errorf("client tools = %+v, want the mysql-named pair", tools)
		}
	})

	t.Run("inspect reads the live server", func(t *testing.T) {
		insp, err := hostdb.Inspect(context.Background(), r.client, r.creds(dbName, false))
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if !insp.Connected {
			t.Fatalf("not connected: %s", insp.Reason)
		}
		if insp.ServerVersion == "" {
			t.Error("no server version")
		}
		if insp.Tables != r.tableCount(dbName) {
			t.Errorf("Inspect reported %d tables, server says %d", insp.Tables, r.tableCount(dbName))
		}
		if insp.UTF8MB4Tables == 0 {
			t.Error("the fixture is utf8mb4 throughout but none was reported")
		}
	})

	t.Run("an awkward password survives the defaults file", func(t *testing.T) {
		// The seeded password holds a space, a single quote and a '#' — the
		// my.cnf comment character. A plain-password account is checked too,
		// so a quoting regression is distinguishable from a broken rig.
		for _, plain := range []bool{true, false} {
			insp, err := hostdb.Inspect(context.Background(), r.client, r.creds(dbName, plain))
			if err != nil {
				t.Fatalf("Inspect(plain=%v): %v", plain, err)
			}
			if !insp.Connected {
				t.Errorf("plain=%v did not connect: %s", plain, insp.Reason)
			}
		}
	})

	dumpPath := filepath.Join(t.TempDir(), "dump.sql.gz")

	t.Run("dump is verified and complete", func(t *testing.T) {
		var buf bytes.Buffer
		stats, err := hostdb.Dump(context.Background(), r.client, r.creds(dbName, false), &buf)
		if err != nil {
			t.Fatalf("Dump: %v", err)
		}
		if !stats.FooterOK {
			t.Error("dump footer missing — the truncation guard did not see a complete dump")
		}
		if stats.Tables != r.baseTableCount(dbName) {
			t.Errorf("dump counted %d tables, server has %d base tables", stats.Tables, r.baseTableCount(dbName))
		}
		if err := os.WriteFile(dumpPath, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}

		sql := gunzipAll(t, buf.Bytes())
		// --routines --triggers are on the dump command; their output is the
		// only proof the flags survived into the real invocation.
		for _, want := range []string{
			"CREATE TABLE `options`",
			"wide_chars_audit",       // trigger
			"fill_bulk_rows",         // procedure
			"site_url_of",            // function
			"recent_options",         // view
			"rocket \xf0\x9f\x9a\x80", // the four-byte emoji, byte-exact
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("dump is missing %q", want)
			}
		}
	})

	t.Run("dump restores into a scratch database", func(t *testing.T) {
		// TODO §1: "the dump restores ... compare table counts".
		scratch := r.creds(dbName+"_scratch", false)
		res, err := hostdb.Import(context.Background(), r.client, scratch, dumpPath, hostdb.ImportOptions{
			RemoteGunzip: caps.Has("gzip"),
		})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		got, want := r.baseTableCount(dbName+"_scratch"), r.baseTableCount(dbName)
		if got != want {
			t.Errorf("scratch has %d base tables, source has %d", got, want)
		}
		if r.tableCount(dbName+"_scratch") != r.tableCount(dbName) {
			t.Errorf("view count differs: scratch %d, source %d",
				r.tableCount(dbName+"_scratch"), r.tableCount(dbName))
		}
		if res != nil && res.DestTables != want {
			t.Errorf("Import reported %d destination tables, server says %d", res.DestTables, want)
		}
		if res != nil && res.SourceTables != res.DestTables {
			t.Errorf("dump held %d tables but %d landed", res.SourceTables, res.DestTables)
		}
		// Row-level equality on the biggest table: a dump that restores but
		// loses rows would otherwise pass a table count.
		src := strings.TrimSpace(r.query(dbName, "SELECT COUNT(*) FROM bulk_rows"))
		dst := strings.TrimSpace(r.query(dbName+"_scratch", "SELECT COUNT(*) FROM bulk_rows"))
		if src != dst {
			t.Errorf("bulk_rows: source %s rows, restored %s", src, dst)
		}
	})

	t.Run("a partial dump is caught, not silently accepted", func(t *testing.T) {
		// The remote pipeline is `mysqldump ... | gzip`, so the shell reports
		// gzip's exit status — mysqldump can fail mid-dump and still look
		// successful. Here it genuinely does: the database holds a routine
		// the site account may not SHOW CREATE, which is a real shared-hosting
		// privilege shape. Only the completion footer reveals the truncation.
		var buf bytes.Buffer
		stats, err := hostdb.Dump(context.Background(), r.client, r.creds(dbName+"_restricted", false), &buf)
		if err == nil {
			t.Fatal("a truncated dump was accepted as complete")
		}
		if stats == nil || stats.FooterOK {
			t.Errorf("stats should report the missing footer: %+v", stats)
		}
		if !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("error should name the incompleteness: %v", err)
		}
		// Partial output still arrived — proof the failure was silent at the
		// shell level and the footer check is what caught it.
		if buf.Len() == 0 {
			t.Error("expected the partial dump bytes to have streamed through")
		}
	})

	t.Run("serialized data survives the URL rewrite", func(t *testing.T) {
		// The highest-value assertion in the rig: a serialized PHP string
		// whose byte-length prefix must be recomputed when the host name
		// changes length. Getting this wrong is the classic "migrated site
		// loads blank" failure, and it cannot be caught without a real
		// dump and a real PHP to unserialize the result.
		plain := gunzipAll(t, mustReadAll(t, dumpPath))
		var rewritten bytes.Buffer
		pairs := searchreplace.Pairs(searchreplace.PlanInput{SourceURL: oldURL, DestURL: newURL})
		stats, err := searchreplace.RewriteDump(strings.NewReader(plain), &rewritten, pairs)
		if err != nil {
			t.Fatalf("RewriteDump: %v", err)
		}
		if stats.SerializedFixups == 0 {
			t.Error("no serialized fixups — the length prefixes were never recomputed")
		}

		// Import validates a gzipped dump, and rehost re-compresses after the
		// rewrite for exactly that reason — mirror it rather than handing
		// Import plain SQL it will refuse.
		rewrittenPath := filepath.Join(t.TempDir(), "rewritten.sql.gz")
		writeGzip(t, rewrittenPath, rewritten.Bytes())
		target := r.creds(dbName+"_rewrite", false)
		if _, err := hostdb.Import(context.Background(), r.client, target, rewrittenPath,
			hostdb.ImportOptions{RemoteGunzip: caps.Has("gzip")}); err != nil {
			t.Fatalf("importing the rewritten dump: %v", err)
		}

		value := strings.TrimSpace(r.query(dbName+"_rewrite",
			"SELECT option_value FROM options WHERE option_name='urls_serialized'"))
		if strings.Contains(value, oldURL) {
			t.Errorf("the old URL survived: %s", value)
		}
		if !strings.Contains(value, "s:"+strconv.Itoa(len(newURL))+":\""+newURL+"\"") {
			t.Errorf("length prefix was not recomputed for %d-byte URL: %s", len(newURL), value)
		}
		// The real proof: PHP itself must accept the structure.
		out := r.exec("php", "-r",
			`$v = unserialize($argv[1]); echo is_array($v) && $v["home"] === $argv[2] ? "OK" : "BROKEN";`,
			value, newURL)
		if !strings.Contains(out, "OK") {
			t.Errorf("PHP could not unserialize the rewritten value (%s): %s", out, value)
		}
	})
}

func TestMariaDBWithoutMySQLSymlinks(t *testing.T) {
	// A host shipping MariaDB without the mysql-named compatibility symlinks.
	// ResolveClientTools has to notice and the dump has to work anyway.
	r := startRig(t, "mariadb")
	caps := r.probe()

	if caps.Has("mysql") || caps.Has("mysqldump") {
		t.Fatal("the rig should present no mysql-named binaries")
	}
	if !caps.Has("mariadb") || !caps.Has("mariadb-dump") {
		t.Fatal("the rig should present the mariadb-named binaries")
	}
	tools := hostdb.ResolveClientTools("mysql", caps.Has)
	if tools.Client != "mariadb" || tools.Dump != "mariadb-dump" {
		t.Fatalf("client tools = %+v, want the mariadb-named pair", tools)
	}

	creds := r.creds(dbName, false)
	creds.Tools = tools
	insp, err := hostdb.Inspect(context.Background(), r.client, creds)
	if err != nil || !insp.Connected {
		t.Fatalf("Inspect: %v (%v)", err, insp)
	}
	if !strings.Contains(insp.ServerVersion, "MariaDB") {
		t.Errorf("server version %q should identify MariaDB", insp.ServerVersion)
	}

	var buf bytes.Buffer
	stats, err := hostdb.Dump(context.Background(), r.client, creds, &buf)
	if err != nil {
		t.Fatalf("Dump via mariadb-dump: %v", err)
	}
	if !stats.FooterOK {
		t.Error("dump footer missing")
	}
}

func TestPHPDumpFallback(t *testing.T) {
	// No dump binary of any name: the PHP helper is the only way out, and it
	// has to produce something the same importer accepts.
	r := startRig(t, "nodump")
	caps := r.probe()

	if caps.Has("mysqldump") || caps.Has("mariadb-dump") {
		t.Fatal("the rig should present no dump binary")
	}
	if !caps.Has("php") {
		t.Fatal("the PHP fallback needs php")
	}

	creds := r.creds(dbName, false)
	creds.Tools = hostdb.ResolveClientTools("mysql", caps.Has)

	var buf bytes.Buffer
	stats, err := hostdb.DumpPHP(context.Background(), r.client, creds, &buf)
	if err != nil {
		t.Fatalf("DumpPHP: %v", err)
	}
	if !stats.FooterOK {
		t.Error("PHP dump footer missing — the truncation guard rejected it")
	}

	sql := gunzipAll(t, buf.Bytes())
	for _, want := range []string{"CREATE TABLE", "options", "bulk_rows"} {
		if !strings.Contains(sql, want) {
			t.Errorf("PHP dump is missing %q", want)
		}
	}

	dumpPath := filepath.Join(t.TempDir(), "php-dump.sql.gz")
	if err := os.WriteFile(dumpPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := r.creds(dbName+"_scratch", false)
	scratch.Tools = creds.Tools
	if _, err := hostdb.Import(context.Background(), r.client, scratch, dumpPath,
		hostdb.ImportOptions{RemoteGunzip: caps.Has("gzip")}); err != nil {
		t.Fatalf("importing the PHP dump: %v", err)
	}
	got, want := r.baseTableCount(dbName+"_scratch"), r.baseTableCount(dbName)
	if got != want {
		t.Errorf("PHP-dump restore has %d base tables, source has %d", got, want)
	}
	src := strings.TrimSpace(r.query(dbName, "SELECT COUNT(*) FROM bulk_rows"))
	dst := strings.TrimSpace(r.query(dbName+"_scratch", "SELECT COUNT(*) FROM bulk_rows"))
	if src != dst {
		t.Errorf("bulk_rows: source %s rows, restored %s", src, dst)
	}
}

func TestPostgresEnvironment(t *testing.T) {
	r := startRig(t, "pgsql")
	caps := r.probe()

	if !caps.Has("psql") || !caps.Has("pg_dump") {
		t.Fatal("the rig should present psql and pg_dump")
	}
	creds := r.creds(dbName, false)
	creds.Tools = hostdb.ResolveClientTools(hostdb.DriverPostgres, caps.Has)

	insp, err := hostdb.Inspect(context.Background(), r.client, creds)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !insp.Connected {
		t.Fatalf("not connected: %s", insp.Reason)
	}
	// inspectPG counts BASE TABLEs, unlike the MySQL inspection which counts
	// views too — the independent measurement has to match the one under test.
	if insp.Tables != r.baseTableCount(dbName) {
		t.Errorf("Inspect reported %d tables, server says %d base tables", insp.Tables, r.baseTableCount(dbName))
	}

	// pg_dump with the password staged in a umask-077 pgpass file — libpq
	// refuses the FIFO the mysql path uses, so this is a distinct mechanism.
	var buf bytes.Buffer
	stats, err := hostdb.Dump(context.Background(), r.client, creds, &buf)
	if err != nil {
		t.Fatalf("Dump (pg): %v", err)
	}
	if !stats.FooterOK {
		t.Error("pg dump footer missing")
	}

	dumpPath := filepath.Join(t.TempDir(), "pg.sql.gz")
	if err := os.WriteFile(dumpPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := r.creds(dbName+"_scratch", false)
	scratch.Tools = creds.Tools
	if _, err := hostdb.Import(context.Background(), r.client, scratch, dumpPath,
		hostdb.ImportOptions{RemoteGunzip: caps.Has("gzip")}); err != nil {
		t.Fatalf("Import (pg): %v", err)
	}
	src := strings.TrimSpace(r.query(dbName, "SELECT COUNT(*) FROM bulk_rows"))
	dst := strings.TrimSpace(r.query(dbName+"_scratch", "SELECT COUNT(*) FROM bulk_rows"))
	if src != dst {
		t.Errorf("bulk_rows: source %s rows, restored %s", src, dst)
	}
	// Staged credential material must not outlive the command that used it.
	leftovers := r.exec("sh", "-c", "ls -a /home/"+sshUser+"/"+hostdb.StageDir+" 2>/dev/null | grep -v '^\\.$\\|^\\.\\.$' || true")
	if strings.TrimSpace(leftovers) != "" {
		t.Errorf("credential staging left files behind: %q", leftovers)
	}
}

// writeGzip compresses data into path, the shape Import expects.
func writeGzip(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustReadAll(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
