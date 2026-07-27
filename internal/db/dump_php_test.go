package db

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/ssh"
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
