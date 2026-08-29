package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/go-ssh/remote"
)

type fakeRunner struct {
	res     remote.Result
	err     error
	lastCmd string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (remote.Result, error) {
	f.lastCmd = cmd
	return f.res, f.err
}

func TestInspectParsesBatchOutput(t *testing.T) {
	r := &fakeRunner{res: remote.Result{Stdout: "version\t10.6.18-MariaDB\ncharset\tutf8mb4\ntables\t57\t123456\nutf8mb4\t57\n"}}
	insp, err := Inspect(context.Background(), r, &Credentials{Name: "wpdb", User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !insp.Connected || insp.ServerVersion != "10.6.18-MariaDB" || insp.Charset != "utf8mb4" ||
		insp.Tables != 57 || insp.SizeKB != 123456 || insp.UTF8MB4Tables != 57 {
		t.Errorf("Inspect = %+v", insp)
	}
}

func TestInspectPasswordNeverInArgv(t *testing.T) {
	r := &fakeRunner{res: remote.Result{}}
	creds := &Credentials{Name: "d", User: "u", Password: `sup"er\sec'ret`, Host: "localhost"}
	if _, err := Inspect(context.Background(), r, creds); err != nil {
		t.Fatal(err)
	}
	// The password may only appear after the heredoc marker (stdin), never in
	// the part of the command line that becomes the remote argv.
	argvPart, _, ok := strings.Cut(r.lastCmd, "<<'REHOST_CNF'")
	if !ok {
		t.Fatalf("command has no heredoc: %s", r.lastCmd)
	}
	if strings.Contains(argvPart, "sec") {
		t.Errorf("password leaked into argv: %s", argvPart)
	}
	if !strings.Contains(r.lastCmd, `password="sup\"er\\sec'ret"`) {
		t.Errorf("password not cnf-quoted in defaults file: %s", r.lastCmd)
	}
}

func TestInspectFailureIsHonest(t *testing.T) {
	r := &fakeRunner{res: remote.Result{ExitCode: 1, Stderr: "ERROR 1045 (28000): Access denied for user 'u'@'localhost' (using password: YES)\n"}}
	insp, err := Inspect(context.Background(), r, &Credentials{Name: "d", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if insp.Connected || !strings.Contains(insp.Reason, "Access denied") {
		t.Errorf("Inspect = %+v", insp)
	}

	r = &fakeRunner{err: errors.New("connection lost")}
	if _, err := Inspect(context.Background(), r, &Credentials{Name: "d"}); err == nil {
		t.Error("transport failure must propagate as an error")
	}
}

func TestInspectReasonStripsPassword(t *testing.T) {
	r := &fakeRunner{res: remote.Result{ExitCode: 1, Stderr: "something echoed hunter2 back\n"}}
	insp, err := Inspect(context.Background(), r, &Credentials{Name: "d", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(insp.Reason, "hunter2") {
		t.Errorf("reason contains the password: %q", insp.Reason)
	}
}

func TestSplitSocket(t *testing.T) {
	host, socket := splitSocket("localhost:/var/run/mysql.sock")
	if host != "localhost" || socket != "/var/run/mysql.sock" {
		t.Errorf("splitSocket = %q, %q", host, socket)
	}
	host, socket = splitSocket("db.example.com")
	if host != "db.example.com" || socket != "" {
		t.Errorf("splitSocket plain = %q, %q", host, socket)
	}
}
