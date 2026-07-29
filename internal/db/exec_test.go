package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

func TestRunSQLSuccessReturnsStdout(t *testing.T) {
	r := &fakeRunner{res: ssh.Result{Stdout: "i:1;\n"}}
	res, err := RunSQL(context.Background(), r, &Credentials{Name: "d", User: "u", Password: "p"}, "SELECT value FROM `t`;")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || strings.TrimSpace(res.Stdout) != "i:1;" {
		t.Fatalf("RunSQL = %+v, want OK with the row value", res)
	}
	// The SQL rides -e; the password may only appear after the heredoc marker.
	argvPart, _, ok := strings.Cut(r.lastCmd, "<<'REHOST_CNF'")
	if !ok || !strings.Contains(argvPart, "SELECT value FROM") {
		t.Errorf("SQL should be in the argv part via -e: %s", argvPart)
	}
}

func TestRunSQLPasswordNeverInArgv(t *testing.T) {
	r := &fakeRunner{res: ssh.Result{}}
	creds := &Credentials{Name: "d", User: "u", Password: `sup"er\sec'ret`, Host: "localhost"}
	if _, err := RunSQL(context.Background(), r, creds, "DELETE FROM `t`;"); err != nil {
		t.Fatal(err)
	}
	argvPart, _, ok := strings.Cut(r.lastCmd, "<<'REHOST_CNF'")
	if !ok {
		t.Fatalf("command has no heredoc: %s", r.lastCmd)
	}
	if strings.Contains(argvPart, "sec") {
		t.Errorf("password leaked into argv: %s", argvPart)
	}
}

func TestRunSQLMySQLFailureIsHonest(t *testing.T) {
	// A MySQL-level failure is OK=false with a sanitized reason, never an error —
	// this is what lets a caller degrade a best-effort statement to a warning.
	r := &fakeRunner{res: ssh.Result{ExitCode: 1, Stderr: "ERROR 1146 (42S02): Table 'db.t' doesn't exist echoing hunter2\n"}}
	res, err := RunSQL(context.Background(), r, &Credentials{Name: "d", Password: "hunter2"}, "DELETE FROM `t`;")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Errorf("mysql failure should report OK=false, got %+v", res)
	}
	if !strings.Contains(res.Reason, "doesn't exist") {
		t.Errorf("reason should carry the mysql error, got %q", res.Reason)
	}
	if strings.Contains(res.Reason, "hunter2") {
		t.Errorf("reason must strip the password, got %q", res.Reason)
	}
}

func TestRunSQLTransportErrorPropagates(t *testing.T) {
	r := &fakeRunner{err: errors.New("connection lost")}
	if _, err := RunSQL(context.Background(), r, &Credentials{Name: "d"}, "SELECT 1;"); err == nil {
		t.Error("transport failure must propagate as an error")
	}
}
