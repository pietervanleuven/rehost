package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

func pgCreds() *Credentials {
	return &Credentials{Driver: "pgsql", Name: "craftdb", User: "craft", Host: "127.0.0.1", Port: 5432, Password: "s3c:ret"}
}

func TestPGPassLine(t *testing.T) {
	line, err := pgPassLine(pgCreds())
	if err != nil {
		t.Fatal(err)
	}
	if line != `*:*:*:*:s3c\:ret` {
		t.Errorf("pgpass line = %q", line)
	}
	if _, err := pgPassLine(&Credentials{Password: "a\nb"}); err == nil {
		t.Error("a newline in the password cannot ride the pgpass format and must error")
	}
}

func TestInspectPGCommandShape(t *testing.T) {
	r := &fakeRunnerDB{res: ssh.Result{Stdout: "version::16.2\nsizekb::2048\ntables::12\nencoding::UTF8\n"}}
	insp, err := Inspect(context.Background(), r, pgCreds())
	if err != nil {
		t.Fatal(err)
	}
	if !insp.Connected || insp.ServerVersion != "16.2" || insp.SizeKB != 2048 || insp.Tables != 12 || insp.Charset != "UTF8" {
		t.Errorf("inspection = %+v", insp)
	}
	cmd := r.cmds[0]
	if !strings.Contains(cmd, "psql -w -X -A -t -h '127.0.0.1' -p 5432 -U 'craft' -d 'craftdb'") {
		t.Errorf("psql invocation wrong:\n%s", cmd)
	}
	if strings.Contains(cmd, "s3c") && !strings.Contains(cmd, `s3c\:ret`) {
		t.Errorf("password mangled in heredoc:\n%s", cmd)
	}
	// The password travels only in the heredoc body, never before the
	// redirect (i.e. never in argv).
	beforeHeredoc, _, _ := strings.Cut(cmd, "<<'REHOST_PGPASS'")
	if strings.Contains(beforeHeredoc, "s3c") {
		t.Errorf("password leaked outside the heredoc:\n%s", beforeHeredoc)
	}
	if !strings.Contains(cmd, `umask 077`) || !strings.Contains(cmd, `PGPASSFILE="$t"`) || !strings.Contains(cmd, `rm -f "$t"`) {
		t.Errorf("passfile staging/cleanup missing:\n%s", cmd)
	}
}

func TestPGDumpFooterVerified(t *testing.T) {
	complete := "--\n-- PostgreSQL database dump\n--\nCREATE TABLE public.users (id int);\nCOPY public.users (id) FROM stdin;\n1\n\\.\n\n-- PostgreSQL database dump complete\n"
	s := &fakeStreamer{payload: gzipped(t, complete)}
	stats, err := Dump(context.Background(), s, pgCreds(), discardWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.FooterOK || stats.Tables != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if !strings.Contains(s.lastCmd, "pg_dump -w --no-owner --no-privileges") || !strings.Contains(s.lastCmd, "| gzip") {
		t.Errorf("dump cmd = %s", s.lastCmd)
	}

	truncated := &fakeStreamer{payload: gzipped(t, "CREATE TABLE public.users (id int);\nCOPY public.users (id) FROM stdin;\n1\n")}
	if _, err := Dump(context.Background(), truncated, pgCreds(), discardWriter{}); err == nil {
		t.Error("a pg dump without the completion footer must fail verification")
	}
}

// fakeRunnerDB records commands and replies with one canned result.
type fakeRunnerDB struct {
	cmds []string
	res  ssh.Result
	err  error
}

func (f *fakeRunnerDB) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.cmds = append(f.cmds, cmd)
	return f.res, f.err
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestPGDumpCommandThroughShell runs the exact pipeline pgDumpCmd builds
// through a real shell with a stub pg_dump: the passfile must exist mode-600
// with the escaped password when pg_dump runs, and be gone afterwards.
func TestPGDumpCommandThroughShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command runs on a remote POSIX shell")
	}
	bin := t.TempDir()
	home := t.TempDir()
	stub := `#!/bin/sh
[ -n "$PGPASSFILE" ] || { echo "no PGPASSFILE" >&2; exit 1; }
[ -f "$PGPASSFILE" ] || { echo "passfile missing" >&2; exit 1; }
perms=$(ls -l "$PGPASSFILE" | cut -c2-10)
[ "$perms" = "rw-------" ] || { echo "passfile perms $perms" >&2; exit 1; }
grep -q 's3c\\:ret' "$PGPASSFILE" || { echo "password not in passfile" >&2; exit 1; }
printf -- '-- PostgreSQL database dump\nCREATE TABLE public.t (id int);\n-- PostgreSQL database dump complete\n'
`
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, err := pgDumpCmd(pgCreds())
	if err != nil {
		t.Fatal(err)
	}
	sh := exec.Command("sh", "-c", cmd)
	sh.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+home)
	var stdout, stderr bytes.Buffer
	sh.Stdout = &stdout
	sh.Stderr = &stderr
	if err := sh.Run(); err != nil {
		t.Fatalf("pipeline failed: %v stderr=%s", err, stderr.String())
	}
	gz, err := gzip.NewReader(&stdout)
	if err != nil {
		t.Fatalf("stdout is not gzip: %v", err)
	}
	sql, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sql), "-- PostgreSQL database dump complete") {
		t.Errorf("dump body missing:\n%s", sql)
	}
	if strings.Contains(string(sql), "s3c") {
		t.Error("the passfile leaked into the dump stream")
	}
	// The passfile must be cleaned up.
	leftovers, _ := filepath.Glob(filepath.Join(home, ".rehost", ".pgpass.*"))
	if len(leftovers) != 0 {
		t.Errorf("passfile left behind: %v", leftovers)
	}
}
