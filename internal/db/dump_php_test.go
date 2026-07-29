package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

func TestDumpPHPVerified(t *testing.T) {
	s := &fakeStreamer{payload: gzipped(t, completeDump)}
	var out bytes.Buffer
	stats, err := DumpPHP(context.Background(), s, &Credentials{Name: "wpdb", User: "u", Password: "hunter2"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.FooterOK || stats.Tables != 2 || stats.Bytes != int64(len(completeDump)) {
		t.Errorf("stats = %+v", stats)
	}
	if stats.CompressedBytes != int64(out.Len()) {
		t.Errorf("compressed bytes %d != written %d", stats.CompressedBytes, out.Len())
	}
	if !bytes.Equal(out.Bytes(), s.payload) {
		t.Error("the gzipped payload must reach the writer unmodified")
	}
	if !strings.HasPrefix(s.lastCmd, "php -d display_errors=stderr -d error_reporting=0 -r ") {
		t.Errorf("unexpected command prefix: %s", s.lastCmd)
	}
}

// TestDumpPHPPasswordStaysOffArgv pins the secrecy contract: everything
// before the heredoc marker becomes remote argv, so the password may only
// appear in the JSON line after it.
func TestDumpPHPPasswordStaysOffArgv(t *testing.T) {
	s := &fakeStreamer{payload: gzipped(t, completeDump)}
	creds := &Credentials{Name: "wpdb", User: "u", Password: "s3cr3t-fragment"}
	if _, err := DumpPHP(context.Background(), s, creds, io.Discard); err != nil {
		t.Fatal(err)
	}
	argv, body, found := strings.Cut(s.lastCmd, "<<'REHOST_CREDS'")
	if !found {
		t.Fatalf("command lacks the creds heredoc: %s", s.lastCmd)
	}
	if strings.Contains(argv, "s3cr3t-fragment") {
		t.Errorf("password leaked into argv: %s", argv)
	}
	if !strings.Contains(body, `"s3cr3t-fragment"`) {
		t.Errorf("creds JSON missing from the heredoc body: %s", body)
	}
}

func TestDumpPHPMissingFooterFails(t *testing.T) {
	s := &fakeStreamer{payload: gzipped(t, "SET NAMES utf8mb4;\nCREATE TABLE t (id int);\n")}
	stats, err := DumpPHP(context.Background(), s, &Credentials{Name: "d"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("dump without completion footer must fail, got err=%v stats=%+v", err, stats)
	}
}

func TestDumpPHPRemoteFailure(t *testing.T) {
	s := &fakeStreamer{res: ssh.Result{ExitCode: 1, Stderr: "rehost: dump failed: connect failed for hunter2\n"}}
	_, err := DumpPHP(context.Background(), s, &Credentials{Name: "d", Password: "hunter2"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("remote failure should surface stderr, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaked the password: %v", err)
	}
}

func TestDumpPHPTransportFailure(t *testing.T) {
	s := &fakeStreamer{err: errors.New("connection lost")}
	if _, err := DumpPHP(context.Background(), s, &Credentials{Name: "d"}, io.Discard); err == nil {
		t.Error("transport failure must propagate")
	}
}

func requirePHP(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not installed")
	}
}

// TestPHPDumpScriptSyntax lints the embedded helper with a real php binary.
// The script is written for -r (no opening tag), so the tag is prepended
// for the lint run. Skipped where php is not installed.
func TestPHPDumpScriptSyntax(t *testing.T) {
	requirePHP(t)
	file := filepath.Join(t.TempDir(), "dump.php")
	if err := os.WriteFile(file, []byte("<?php\n"+phpDumpScript), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("php", "-l", file).CombinedOutput()
	if err != nil {
		t.Fatalf("php -l rejected the dump script: %v\n%s", err, out)
	}
}

// phpMysqliShim replaces the real mysqli extension with a userland fake so
// the dump helper can be exercised without a MySQL server. The built-in
// mysqli functions are removed via -d disable_functions (see runPHPDumpFake),
// which frees their names for these hoisted definitions. Each mysqli_query
// looks its SQL up in the JSON dataset at $REHOST_FAKE_DB: a matching entry
// with a non-empty "error" returns false (driving the helper's error path),
// otherwise its "rows" are returned; an unknown query yields an empty result.
const phpMysqliShim = `<?php
if (!defined('MYSQLI_REPORT_OFF')) { define('MYSQLI_REPORT_OFF', 0); }
if (!defined('MYSQLI_USE_RESULT')) { define('MYSQLI_USE_RESULT', 1); }
$GLOBALS['__rehost_db'] = json_decode(file_get_contents(getenv('REHOST_FAKE_DB')), true);
$GLOBALS['__rehost_err'] = '';
class RehostFakeResult { public $rows; public $i = 0; function __construct($r) { $this->rows = $r; } }
function mysqli_report($x = 0) {}
function mysqli_connect($h = null, $u = null, $p = null, $d = null, $port = null, $s = null) { return new stdClass(); }
function mysqli_connect_error() { return 'fake connect error'; }
function mysqli_set_charset($db, $cs) { return true; }
function mysqli_query($db, $sql, $mode = 0) {
  $q = isset($GLOBALS['__rehost_db']['queries'][$sql]) ? $GLOBALS['__rehost_db']['queries'][$sql] : null;
  if ($q === null) { $GLOBALS['__rehost_err'] = ''; return new RehostFakeResult(array()); }
  if (isset($q['error']) && $q['error'] !== '') { $GLOBALS['__rehost_err'] = $q['error']; return false; }
  $GLOBALS['__rehost_err'] = '';
  return new RehostFakeResult(isset($q['rows']) ? $q['rows'] : array());
}
function mysqli_error($db) { return $GLOBALS['__rehost_err']; }
function mysqli_fetch_row($res) {
  if (!($res instanceof RehostFakeResult) || $res->i >= count($res->rows)) { return null; }
  return $res->rows[$res->i++];
}
function mysqli_free_result($res) { return true; }
function mysqli_real_escape_string($db, $s) { return str_replace(array("\\", "'"), array("\\\\", "\\'"), (string)$s); }
`

// runPHPDumpFake runs the real phpDumpScript against the mysqli shim above,
// feeding it the given query dataset, and returns the decompressed SQL. The
// built-in mysqli functions are disabled so the shim's definitions take over.
func runPHPDumpFake(t *testing.T, dataset map[string]any) string {
	t.Helper()
	requirePHP(t)
	dir := t.TempDir()
	data, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(dir, "db.json")
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "dump.php")
	if err := os.WriteFile(script, []byte(phpMysqliShim+phpDumpScript), 0o644); err != nil {
		t.Fatal(err)
	}
	disable := "disable_functions=mysqli_report,mysqli_connect,mysqli_connect_error," +
		"mysqli_set_charset,mysqli_query,mysqli_error,mysqli_fetch_row,mysqli_free_result,mysqli_real_escape_string"
	cmd := exec.Command("php", "-d", disable, script)
	cmd.Env = append(os.Environ(), "REHOST_FAKE_DB="+dataPath)
	cmd.Stdin = strings.NewReader(`{"name":"testdb"}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("php dump helper failed: %v\nstderr: %s", err, stderr.String())
	}
	gz, err := gzip.NewReader(&stdout)
	if err != nil {
		t.Fatalf("output is not gzip (stderr: %s): %v", stderr.String(), err)
	}
	sql, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(sql)
}

func q(rows ...[]any) map[string]any { return map[string]any{"rows": rows} }

func errQ(msg string) map[string]any { return map[string]any{"error": msg} }

// TestPHPDumpEmitsViewsTriggersRoutines exercises the helper end to end
// against the mysqli shim, covering the mysqldump-parity additions: a view is
// emitted after the base tables, a trigger is emitted in a DELIMITER block,
// and a routine whose definition the account cannot read degrades to a
// "-- skipped" comment while the completion footer is still written.
func TestPHPDumpEmitsViewsTriggersRoutines(t *testing.T) {
	dataset := map[string]any{"queries": map[string]any{
		"SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'": q([]any{"t1"}),
		"SHOW CREATE TABLE `t1`":                           q([]any{"t1", "CREATE TABLE `t1` (\n  `id` int NOT NULL\n)"}),
		"SELECT * FROM `t1`":                               q([]any{"1"}),
		"SHOW FULL TABLES WHERE Table_type = 'VIEW'":       q([]any{"v1"}),
		"SHOW CREATE VIEW `v1`": q([]any{
			"v1",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v1` AS select `t1`.`id` AS `id` from `t1`",
			"utf8mb4", "utf8mb4_general_ci",
		}),
		"SHOW TRIGGERS": q([]any{"trg1", "INSERT", "t1", "SET @x = 1", "BEFORE"}),
		"SHOW CREATE TRIGGER `trg1`": q([]any{
			"trg1", "STRICT_ALL_TABLES",
			"CREATE DEFINER=`root`@`localhost` TRIGGER `trg1` BEFORE INSERT ON `t1` FOR EACH ROW SET @x = 1",
			"utf8mb4", "utf8mb4_general_ci", "utf8mb4_general_ci",
		}),
		"SHOW PROCEDURE STATUS WHERE Db = DATABASE()": q([]any{"testdb", "p1", "PROCEDURE"}),
		// Column 2 (the CREATE body) is NULL — the privilege-denied case.
		"SHOW CREATE PROCEDURE `p1`":                 q([]any{"p1", "STRICT_ALL_TABLES", nil}),
		"SHOW FUNCTION STATUS WHERE Db = DATABASE()": q(),
	}}
	sql := runPHPDumpFake(t, dataset)

	mustContain := func(needle string) int {
		t.Helper()
		i := strings.Index(sql, needle)
		if i < 0 {
			t.Errorf("dump missing %q\n---\n%s\n---", needle, sql)
		}
		return i
	}

	table := mustContain("CREATE TABLE `t1`")
	mustContain("INSERT INTO `t1`")
	dropView := mustContain("DROP VIEW IF EXISTS `v1`")
	view := mustContain("VIEW `v1` AS select")
	dropTrig := mustContain("DROP TRIGGER IF EXISTS `trg1`")
	trig := mustContain("TRIGGER `trg1` BEFORE INSERT")
	mustContain("DELIMITER ;;")
	skip := mustContain("-- rehost: skipped PROCEDURE p1:")
	footer := mustContain("-- Dump completed")

	// Views come after every base table; triggers after views; the skipped
	// routine and the footer come last.
	if table >= dropView || dropView >= view || view >= dropTrig || dropTrig >= trig || trig >= skip || skip >= footer {
		t.Errorf("wrong object order: table=%d dropView=%d view=%d dropTrig=%d trig=%d skip=%d footer=%d",
			table, dropView, view, dropTrig, trig, skip, footer)
	}
	// The skip note must be a single SQL comment line, not truncated SQL.
	if strings.Contains(sql[skip:footer], "CREATE PROCEDURE `p1`") && !strings.Contains(sql[skip:footer], "-- ") {
		t.Error("procedure body leaked despite the privilege gap")
	}
}

// TestPHPDumpTriggerQueryDenied covers the coarser degradation path: SHOW
// TRIGGERS itself erroring (no privilege to list triggers) must still yield a
// note and a complete dump rather than aborting.
func TestPHPDumpTriggerQueryDenied(t *testing.T) {
	dataset := map[string]any{"queries": map[string]any{
		"SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'": q([]any{"t1"}),
		"SHOW CREATE TABLE `t1`":                           q([]any{"t1", "CREATE TABLE `t1` (\n  `id` int\n)"}),
		"SELECT * FROM `t1`":                               q(),
		"SHOW FULL TABLES WHERE Table_type = 'VIEW'":       q(),
		"SHOW TRIGGERS":                                    errQ("SELECT command denied to user 'u'@'%' for table 'triggers'"),
		"SHOW PROCEDURE STATUS WHERE Db = DATABASE()":      q(),
		"SHOW FUNCTION STATUS WHERE Db = DATABASE()":       q(),
	}}
	sql := runPHPDumpFake(t, dataset)
	if !strings.Contains(sql, "-- rehost: skipped triggers:") {
		t.Errorf("expected a skipped-triggers note, got:\n%s", sql)
	}
	if !strings.Contains(sql, "-- Dump completed") {
		t.Errorf("footer must still be written after graceful trigger skip:\n%s", sql)
	}
}

// TestPHPDumpCommandThroughShell runs the exact command line dumpPHPCmd
// builds — quoting, heredoc and stdin plumbing included — through a real
// shell and php binary. An empty database name makes the script bail out
// before it would touch any MySQL server. Skipped where php is not
// installed.
func TestPHPDumpCommandThroughShell(t *testing.T) {
	requirePHP(t)
	cmd := exec.Command("sh", "-c", dumpPHPCmd(&Credentials{}))
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got err=%v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "rehost: no database config") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}
