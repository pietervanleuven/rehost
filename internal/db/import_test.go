package db

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/pietervanleuven/go-ssh"
)

// fakeConn implements Conn. It drains every stdin it is handed (so the dump
// and credential readers are consumed exactly as real sessions would), records
// each command, and answers with canned results keyed by command shape.
type fakeConn struct {
	mu sync.Mutex

	setupExit   int
	setupStderr string
	importRes   ssh.Result
	importErr   error
	feederErr   error
	countStdout string
	countExit   int
	countStderr string

	cmds        []string // every Run + StreamPipe command, in call order
	feederStdin string   // bytes fed to `cat > fifo`
	importBytes int64    // bytes drained from the import session stdin
	fifoCreated bool
	fifoRemoved bool
}

func (f *fakeConn) record(cmd string) {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.mu.Unlock()
}

func (f *fakeConn) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.record(cmd)
	switch {
	case strings.Contains(cmd, "mkfifo"):
		f.mu.Lock()
		f.fifoCreated = true
		f.mu.Unlock()
		return ssh.Result{ExitCode: f.setupExit, Stderr: f.setupStderr}, nil
	case strings.HasPrefix(cmd, "rm -f"):
		f.mu.Lock()
		f.fifoRemoved = true
		f.mu.Unlock()
		return ssh.Result{}, nil
	case strings.Contains(cmd, "information_schema"):
		return ssh.Result{Stdout: f.countStdout, ExitCode: f.countExit, Stderr: f.countStderr}, nil
	default:
		return ssh.Result{}, nil
	}
}

func (f *fakeConn) StreamPipe(_ context.Context, cmd string, stdin io.Reader, _ io.Writer) (ssh.Result, error) {
	f.record(cmd)
	var drained int64
	if stdin != nil {
		var buf bytes.Buffer
		drained, _ = io.Copy(&buf, stdin)
		if strings.HasPrefix(cmd, "cat > ") {
			f.mu.Lock()
			f.feederStdin = buf.String()
			f.mu.Unlock()
			return ssh.Result{}, f.feederErr
		}
	}
	f.mu.Lock()
	f.importBytes = drained
	f.mu.Unlock()
	return f.importRes, f.importErr
}

// allCmds returns a copy of the recorded commands under the lock.
func (f *fakeConn) allCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// writeDump writes sql as a gzip file and returns its path.
func writeDump(t *testing.T, dir, name, sql string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, gzipped(t, sql), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportHappyPathRemoteGunzip(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "wp.sql.gz", completeDump)
	conn := &fakeConn{countStdout: "2\n"}
	creds := &Credentials{Name: "wpdb", User: "u", Password: "hunter2", Host: "localhost"}

	res, err := Import(context.Background(), conn, creds, path, ImportOptions{RemoteGunzip: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.FooterOK || res.SourceTables != 2 || res.DestTables != 2 {
		t.Errorf("result = %+v", res)
	}
	fi, _ := os.Stat(path)
	if res.CompressedBytes != fi.Size() || res.Bytes != int64(len(completeDump)) {
		t.Errorf("byte accounting off: %+v file=%d", res, fi.Size())
	}

	cmds := conn.allCmds()
	importCmd := findCmd(t, cmds, "gunzip -c | mysql")
	if !strings.Contains(importCmd, "--default-character-set='utf8mb4'") {
		t.Errorf("charset flag missing: %s", importCmd)
	}
	if !strings.Contains(importCmd, `--defaults-extra-file="$HOME"/.rehost/.import-creds.`) {
		t.Errorf("import must read creds from the FIFO: %s", importCmd)
	}
	if !strings.HasSuffix(importCmd, " 'wpdb'") {
		t.Errorf("db name must be the final argument: %s", importCmd)
	}
	if !conn.fifoCreated || !conn.fifoRemoved {
		t.Errorf("FIFO lifecycle incomplete: created=%v removed=%v", conn.fifoCreated, conn.fifoRemoved)
	}
	// One token, shared by mkfifo, the two sessions, and rm.
	assertSingleToken(t, cmds)
}

func TestImportLocalDecompress(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{countStdout: "2\n"}

	res, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{RemoteGunzip: false})
	if err != nil {
		t.Fatal(err)
	}
	importCmd := findCmd(t, conn.allCmds(), "mysql --defaults-extra-file=")
	if strings.Contains(importCmd, "gunzip") {
		t.Errorf("no remote gunzip expected when decompressing locally: %s", importCmd)
	}
	// The whole compressed file must still be consumed (progress stays truthful).
	fi, _ := os.Stat(path)
	if conn.importBytes == 0 {
		t.Error("import session received no SQL")
	}
	if res.CompressedBytes != fi.Size() {
		t.Errorf("compressed size = %d, want %d", res.CompressedBytes, fi.Size())
	}
}

func TestImportCharsetOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{countStdout: "2\n"}
	if _, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{Charset: "latin1"}); err != nil {
		t.Fatal(err)
	}
	importCmd := findCmd(t, conn.allCmds(), "mysql --defaults-extra-file=")
	if !strings.Contains(importCmd, "--default-character-set='latin1'") {
		t.Errorf("charset override not applied: %s", importCmd)
	}
}

func TestImportPasswordNeverInAnyCommand(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{countStdout: "2\n"}
	creds := &Credentials{Name: "d", User: "u", Password: `sup"er\sec'ret`, Host: "localhost"}

	if _, err := Import(context.Background(), conn, creds, path, ImportOptions{RemoteGunzip: true}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range conn.allCmds() {
		// The password may only ride stdin: never in argv. For the import and
		// FIFO commands that means nowhere at all; for the Inspect-style
		// verification it may appear only after the heredoc marker (its stdin).
		argv, _, _ := strings.Cut(cmd, "<<'REHOST_CNF'")
		if strings.Contains(argv, "sec") {
			t.Errorf("password leaked into argv: %s", argv)
		}
	}
	// It must instead reach the destination out-of-band, through the FIFO feeder.
	if !strings.Contains(conn.feederStdin, `password="sup\"er\\sec'ret"`) {
		t.Errorf("password not delivered cnf-quoted over the FIFO: %q", conn.feederStdin)
	}
}

func TestImportRefusesTruncatedDumpBeforeTouchingDestination(t *testing.T) {
	dir := t.TempDir()
	full := gzipped(t, completeDump)
	path := filepath.Join(dir, "trunc.sql.gz")
	if err := os.WriteFile(path, full[:len(full)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	conn := &fakeConn{}
	_, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("truncated dump must be refused, got %v", err)
	}
	if conn.fifoCreated || len(conn.allCmds()) != 0 {
		t.Errorf("no destination session may open for a bad dump: created=%v cmds=%v", conn.fifoCreated, conn.allCmds())
	}
}

func TestImportMissingFooterRefused(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "nofooter.sql.gz", "-- MySQL dump\nCREATE TABLE t (id int);\n")
	conn := &fakeConn{}
	if _, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{}); err == nil {
		t.Error("dump without a completion footer must be refused")
	}
}

func TestImportSurfacesMysqlError(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{
		importRes: ssh.Result{ExitCode: 1, Stderr: "ERROR 1064 (42000) at line 5: syntax error near hunter2\n"},
	}
	creds := &Credentials{Name: "d", Password: "hunter2"}
	_, err := Import(context.Background(), conn, creds, path, ImportOptions{RemoteGunzip: true})
	if err == nil || !strings.Contains(err.Error(), "1064") {
		t.Fatalf("mysql error must surface, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaked the password: %v", err)
	}
	// Cleanup must still have run despite the failure.
	if !conn.fifoRemoved {
		t.Error("FIFO must be removed on the error path")
	}
}

func TestImportStderrErrorFailsEvenOnZeroExit(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{importRes: ssh.Result{ExitCode: 0, Stderr: "ERROR 1146: table missing\n"}}
	if _, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{}); err == nil {
		t.Error("an ERROR on stderr must fail the import even at exit 0")
	}
}

func TestImportSetupFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{setupExit: 1, setupStderr: "mkfifo: Operation not permitted\n"}
	_, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "credentials pipe") {
		t.Fatalf("mkfifo failure must surface, got %v", err)
	}
}

func TestImportVerificationParsing(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{countStdout: "  57 \n"}
	res, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.DestTables != 57 {
		t.Errorf("DestTables = %d, want 57", res.DestTables)
	}
}

func TestImportVerificationError(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{countExit: 1, countStderr: "ERROR 1045: Access denied\n"}
	_, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("verification failure must surface, got %v", err)
	}
}

func TestImportTransportFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeDump(t, dir, "d.sql.gz", completeDump)
	conn := &fakeConn{importErr: errors.New("connection lost")}
	if _, err := Import(context.Background(), conn, &Credentials{Name: "d"}, path, ImportOptions{}); err == nil {
		t.Error("a transport failure on the import session must propagate")
	}
}

func TestProgressReaderMonotonicAndFinal(t *testing.T) {
	const n = 1000
	data := bytes.Repeat([]byte("x"), n)
	var got []int64
	pr := &progressReader{
		r:     iotest.OneByteReader(bytes.NewReader(data)),
		total: n,
		step:  1,
		cb:    func(sent, total int64) { got = append(got, sent) },
	}
	read, err := io.Copy(io.Discard, pr)
	if err != nil || read != n {
		t.Fatalf("copy = %d, %v", read, err)
	}
	if len(got) == 0 || got[len(got)-1] != n {
		t.Fatalf("progress must end at total; got %v", got)
	}
	prev := int64(0)
	for _, s := range got {
		if s < prev || s > n {
			t.Fatalf("progress not monotonic within [0,total]: %v", got)
		}
		prev = s
	}
}

func TestProgressReaderFiresFinalForTinyInput(t *testing.T) {
	data := []byte("hello")
	var calls int
	var lastSent, lastTotal int64
	pr := &progressReader{
		r:     bytes.NewReader(data),
		total: int64(len(data)),
		step:  progressStep(int64(len(data))), // 32 KiB floor: only the EOF call fires
		cb:    func(sent, total int64) { calls++; lastSent, lastTotal = sent, total },
	}
	_, _ = io.Copy(io.Discard, pr)
	if calls != 1 || lastSent != int64(len(data)) || lastTotal != int64(len(data)) {
		t.Errorf("tiny input: calls=%d sent=%d total=%d", calls, lastSent, lastTotal)
	}
}

func TestProgressStep(t *testing.T) {
	if s := progressStep(0); s != 32*1024 {
		t.Errorf("floor: %d", s)
	}
	if s := progressStep(1000); s != 32*1024 {
		t.Errorf("small total floored: %d", s)
	}
	if s := progressStep(100 << 20); s != (100<<20)/100 {
		t.Errorf("large total: %d", s)
	}
}

// findCmd returns the first recorded command containing sub, failing if none.
func findCmd(t *testing.T, cmds []string, sub string) string {
	t.Helper()
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			return c
		}
	}
	t.Fatalf("no command contained %q in %v", sub, cmds)
	return ""
}

// assertSingleToken checks every FIFO-referencing command names the same token.
func assertSingleToken(t *testing.T, cmds []string) {
	t.Helper()
	const marker = ".import-creds."
	token := ""
	for _, c := range cmds {
		i := strings.Index(c, marker)
		if i < 0 {
			continue
		}
		rest := c[i+len(marker):]
		// token runs until a shell/space boundary
		end := strings.IndexAny(rest, " '\"\n")
		if end >= 0 {
			rest = rest[:end]
		}
		if token == "" {
			token = rest
		} else if token != rest {
			t.Fatalf("FIFO token mismatch: %q vs %q", token, rest)
		}
	}
	if token == "" {
		t.Fatal("no FIFO token found in any command")
	}
}
