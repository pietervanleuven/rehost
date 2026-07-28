package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// fakeRunner captures every command and replies with one canned result.
type fakeRunner struct {
	cmds []string
	res  ssh.Result
	err  error
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.cmds = append(f.cmds, cmd)
	return f.res, f.err
}

func TestDir(t *testing.T) {
	if got := Dir("/home/u"); got != "/home/u/.rehost" {
		t.Errorf("Dir(/home/u) = %q, want /home/u/.rehost", got)
	}
	if got := Dir(""); got != ".rehost" {
		t.Errorf("Dir(\"\") = %q, want .rehost", got)
	}
}

func TestRecordCommandShape(t *testing.T) {
	r := &fakeRunner{}
	e := Entry{
		Time:  time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Event: "dry-run",
		Site:  "/home/u/public_html",
	}
	if err := Record(context.Background(), r, "/home/u", e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("Record ran %d commands, want 1", len(r.cmds))
	}
	cmd := r.cmds[0]

	for _, want := range []string{
		"mkdir -p '/home/u/.rehost'",
		"chmod 700 '/home/u/.rehost'",
		"cat >> '/home/u/.rehost/history.jsonl' <<'REHOST_STATE'",
		"chmod 600 '/home/u/.rehost/history.jsonl'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q:\n%s", want, cmd)
		}
	}

	// The JSON payload sits between the heredoc line and the closing marker,
	// as a single line.
	_, after, ok := strings.Cut(cmd, "REHOST_STATE' && chmod 600 '/home/u/.rehost/history.jsonl'\n")
	if !ok {
		t.Fatalf("no heredoc body in command:\n%s", cmd)
	}
	body, ok := strings.CutSuffix(after, "\nREHOST_STATE")
	if !ok {
		t.Fatalf("heredoc not closed with marker:\n%s", cmd)
	}
	if strings.Contains(body, "\n") {
		t.Errorf("JSON payload is not a single line:\n%s", body)
	}
	for _, want := range []string{
		`"event":"dry-run"`,
		`"site":"/home/u/public_html"`,
		`"time":"2026-07-27T12:00:00Z"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("JSON payload missing %s:\n%s", want, body)
		}
	}
}

func TestRecordStampsZeroTime(t *testing.T) {
	r := &fakeRunner{}
	before := time.Now().UTC()
	if err := Record(context.Background(), r, "/home/u", Entry{Event: "dry-run"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	cmd := r.cmds[0]
	if strings.Contains(cmd, `"time":"0001-01-01`) {
		t.Errorf("zero time was not stamped:\n%s", cmd)
	}
	if !strings.Contains(cmd, `"time":"`+before.Format("2006-01-02")) {
		t.Errorf("stamped time is not today's UTC date:\n%s", cmd)
	}
}

func TestRecordAppendFailure(t *testing.T) {
	r := &fakeRunner{res: ssh.Result{ExitCode: 1, Stderr: "cannot create directory: Disk quota exceeded\nmore noise"}}
	err := Record(context.Background(), r, "/home/u", Entry{Event: "dry-run"})
	if err == nil {
		t.Fatal("Record on exit 1 should error — state must not be silently lost")
	}
	if !strings.Contains(err.Error(), "Disk quota exceeded") {
		t.Errorf("error does not name the problem: %v", err)
	}
	if strings.Contains(err.Error(), "more noise") {
		t.Errorf("error should keep only the first stderr line: %v", err)
	}
}

func TestRecordTransportError(t *testing.T) {
	boom := errors.New("connection lost")
	r := &fakeRunner{err: boom}
	if err := Record(context.Background(), r, "/home/u", Entry{Event: "dry-run"}); !errors.Is(err, boom) {
		t.Errorf("transport error should propagate, got %v", err)
	}
}

func TestHistoryParsesEntriesInOrder(t *testing.T) {
	r := &fakeRunner{res: ssh.Result{Stdout: `{"time":"2026-07-26T10:00:00Z","event":"dry-run","site":"/home/u/site-a"}
{"time":"2026-07-27T11:30:00Z","event":"dry-run","site":"/home/u/site-b","details":{"files":"120"}}
`}}
	entries, err := History(context.Background(), r, "/home/u")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("History returned %d entries, want 2", len(entries))
	}
	if entries[0].Site != "/home/u/site-a" || entries[1].Site != "/home/u/site-b" {
		t.Errorf("entries out of file order: %+v", entries)
	}
	if got := entries[0].Time; !got.Equal(time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("entries[0].Time = %v", got)
	}
	if entries[1].Details["files"] != "120" {
		t.Errorf("entries[1].Details = %v", entries[1].Details)
	}
	if want := "cat '/home/u/.rehost/history.jsonl' 2>/dev/null"; r.cmds[0] != want {
		t.Errorf("History command = %q, want %q", r.cmds[0], want)
	}
}

func TestHistoryNoFileYet(t *testing.T) {
	// cat on a missing file: non-zero exit, stderr swallowed by 2>/dev/null.
	r := &fakeRunner{res: ssh.Result{ExitCode: 1}}
	entries, err := History(context.Background(), r, "/home/u")
	if err != nil || entries != nil {
		t.Errorf("no history file should yield (nil, nil), got (%v, %v)", entries, err)
	}

	// An existing but empty file is the same non-story.
	r = &fakeRunner{res: ssh.Result{Stdout: "\n"}}
	entries, err = History(context.Background(), r, "/home/u")
	if err != nil || entries != nil {
		t.Errorf("empty history should yield (nil, nil), got (%v, %v)", entries, err)
	}
}

func TestHistorySkipsCorruptLine(t *testing.T) {
	r := &fakeRunner{res: ssh.Result{Stdout: `{"time":"2026-07-26T10:00:00Z","event":"dry-run"}
{"time":"2026-07-26T11:00:00Z","event":"dry-r
{"time":"2026-07-27T11:30:00Z","event":"dry-run"}
`}}
	entries, err := History(context.Background(), r, "/home/u")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("corrupt line should be skipped, not fatal: got %d entries, want 2", len(entries))
	}
	if !entries[1].Time.Equal(time.Date(2026, 7, 27, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("entries after the corrupt line were lost: %+v", entries)
	}
}

func TestRecordQuotesHomeWithSpace(t *testing.T) {
	r := &fakeRunner{}
	if err := Record(context.Background(), r, "/home/my sites", Entry{Event: "dry-run"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	cmd := r.cmds[0]
	if !strings.Contains(cmd, "'/home/my sites/.rehost'") ||
		!strings.Contains(cmd, "'/home/my sites/.rehost/history.jsonl'") {
		t.Errorf("paths not shell-quoted:\n%s", cmd)
	}
}

func TestMigratedSites(t *testing.T) {
	entries := []Entry{
		{Event: EventDryRun, Site: "/home/d/public_html"},  // not a migrate: ignored
		{Event: EventMigrate, Site: "/home/d/public_html"}, // counts
		{Event: EventMigrate, Site: "/home/d/sub"},         // counts
		{Event: EventMigrate},                              // no site: ignored
	}
	got := MigratedSites(entries)
	if len(got) != 2 || !got["/home/d/public_html"] || !got["/home/d/sub"] {
		t.Errorf("MigratedSites = %v, want the two migrated docroots", got)
	}
	if got["/home/d/never"] {
		t.Error("a docroot never migrated must not be reported")
	}
	if len(MigratedSites(nil)) != 0 {
		t.Error("no entries should yield an empty set")
	}
}
