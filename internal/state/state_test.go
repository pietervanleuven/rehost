package state

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/go-ssh/remote"
)

// fakeRunner captures every command and replies with one canned result.
type fakeRunner struct {
	cmds []string
	res  remote.Result
	err  error
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (remote.Result, error) {
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
	r := &fakeRunner{res: remote.Result{ExitCode: 1, Stderr: "cannot create directory: Disk quota exceeded\nmore noise"}}
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
	r := &fakeRunner{res: remote.Result{Stdout: `{"time":"2026-07-26T10:00:00Z","event":"dry-run","site":"/home/u/site-a"}
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
	r := &fakeRunner{res: remote.Result{ExitCode: 1}}
	entries, err := History(context.Background(), r, "/home/u")
	if err != nil || entries != nil {
		t.Errorf("no history file should yield (nil, nil), got (%v, %v)", entries, err)
	}

	// An existing but empty file is the same non-story.
	r = &fakeRunner{res: remote.Result{Stdout: "\n"}}
	entries, err = History(context.Background(), r, "/home/u")
	if err != nil || entries != nil {
		t.Errorf("empty history should yield (nil, nil), got (%v, %v)", entries, err)
	}
}

func TestHistorySkipsCorruptLine(t *testing.T) {
	r := &fakeRunner{res: remote.Result{Stdout: `{"time":"2026-07-26T10:00:00Z","event":"dry-run"}
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

func TestLockedSites(t *testing.T) {
	on := func(site string) Entry { return MaintenanceEntry(site, true) }
	off := func(site string) Entry { return MaintenanceEntry(site, false) }

	cases := []struct {
		name    string
		entries []Entry
		want    []string // roots expected locked
	}{
		{"none", nil, nil},
		{"single on with no later off", []Entry{on("/a")}, []string{"/a"}},
		{"on then off clears it", []Entry{on("/a"), off("/a")}, nil},
		{"on off on leaves it locked", []Entry{on("/a"), off("/a"), on("/a")}, []string{"/a"}},
		{"multiple sites, mixed", []Entry{on("/a"), on("/b"), off("/b"), on("/c")}, []string{"/a", "/c"}},
		{"off before any on is a no-op", []Entry{off("/a")}, nil},
		{
			"unrelated and malformed entries ignored",
			[]Entry{
				{Event: EventDryRun, Site: "/a"},                    // wrong event
				{Event: EventMaintenance},                           // no site
				{Event: EventMaintenance, Site: "/b", Details: nil}, // no state key
				on("/c"),
			},
			[]string{"/c"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LockedSites(c.entries)
			if len(got) != len(c.want) {
				t.Fatalf("LockedSites = %v, want %v", got, c.want)
			}
			for _, root := range c.want {
				if !got[root] {
					t.Errorf("expected %s locked, got %v", root, got)
				}
			}
		})
	}
}

func TestMaintenanceEntryRoundTrip(t *testing.T) {
	// What MaintenanceEntry writes is exactly what LockedSites reads back.
	if got := LockedSites([]Entry{MaintenanceEntry("/a", true)}); !got["/a"] {
		t.Error("an on entry should read back as locked")
	}
	if got := LockedSites([]Entry{MaintenanceEntry("/a", true), MaintenanceEntry("/a", false)}); got["/a"] {
		t.Error("a following off entry should clear the lock")
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

func TestMigratedDatabases(t *testing.T) {
	// What DatabaseMigratedEntry writes is exactly what MigratedDatabases reads.
	id := "u_wp\x00u\x00localhost\x000"
	entries := []Entry{
		{Event: EventMigrate, Site: "/home/d/www"}, // a docroot record, not a DB one
		DatabaseMigratedEntry(id),
		{Event: EventMigrateDB}, // no identity: ignored
	}
	got := MigratedDatabases(entries)
	if len(got) != 1 || !got[id] {
		t.Errorf("MigratedDatabases = %v, want just %q", got, id)
	}
	// A files-only run (docroot migrate record, no DB record) must not register
	// any database as filled — the overwrite guard stays armed.
	if len(MigratedDatabases([]Entry{{Event: EventMigrate, Site: "/home/d/www"}})) != 0 {
		t.Error("a docroot-only migrate record must not mark any database as filled")
	}
}

func TestCompactHistoryUntouchedBelowKeep(t *testing.T) {
	entries := []Entry{{Event: EventDryRun}, {Event: EventDryRun}, {Event: EventDryRun}}
	got := CompactHistory(entries, 5)
	if len(got) != len(entries) {
		t.Errorf("nothing to drop when n <= keepRecent: got %d, want %d", len(got), len(entries))
	}
}

func TestCompactHistoryKeepsRecentAndOrder(t *testing.T) {
	var entries []Entry
	for i := 0; i < 10; i++ {
		entries = append(entries, Entry{Event: EventDryRun, Details: map[string]string{"i": strconv.Itoa(i)}})
	}
	got := CompactHistory(entries, 3)
	if len(got) != 3 {
		t.Fatalf("plain dry-runs should compact to keepRecent: got %d, want 3", len(got))
	}
	for j, want := range []string{"7", "8", "9"} {
		if got[j].Details["i"] != want {
			t.Errorf("kept[%d].i = %q, want %q (oldest-first tail preserved)", j, got[j].Details["i"], want)
		}
	}
}

func TestCompactHistoryPreservesMigratedAndLockedSets(t *testing.T) {
	// A critical EventMigrate and an open maintenance lock sit far in the past,
	// buried under enough recent noise that keepRecent alone would drop them.
	entries := []Entry{
		{Event: EventMigrate, Site: "/dest/old"}, // refusal exemption must survive
		MaintenanceEntry("/src/stuck", true),     // unlock recovery must survive
	}
	for i := 0; i < 50; i++ {
		entries = append(entries, Entry{Event: EventDryRun, Details: map[string]string{"i": strconv.Itoa(i)}})
	}

	got := CompactHistory(entries, 5)
	if len(got) >= len(entries) {
		t.Fatalf("expected compaction to shrink %d entries", len(entries))
	}
	if !sameSet(MigratedSites(got), MigratedSites(entries)) {
		t.Errorf("MigratedSites changed: %v vs %v", MigratedSites(got), MigratedSites(entries))
	}
	if !sameSet(LockedSites(got), LockedSites(entries)) {
		t.Errorf("LockedSites changed: %v vs %v", LockedSites(got), LockedSites(entries))
	}
}

func TestCompactHistoryPreservesMigratedDatabases(t *testing.T) {
	// An old EventMigrateDB record (no Site — keyed by database identity) must
	// survive compaction, or the destination-DB overwrite guard would re-arm
	// against rehost's own import and a rerun would demand --onto-existing.
	id := "u_wp\x00u\x00localhost\x000"
	entries := []Entry{DatabaseMigratedEntry(id)}
	for i := 0; i < 50; i++ {
		entries = append(entries, Entry{Event: EventDryRun})
	}
	got := CompactHistory(entries, 5)
	if len(got) >= len(entries) {
		t.Fatalf("expected compaction to shrink %d entries", len(entries))
	}
	if !MigratedDatabases(got)[id] {
		t.Errorf("MigratedDatabases lost %q after compaction: %v", id, MigratedDatabases(got))
	}
}

func TestCompactHistoryKeepsOnlyLatestMaintenancePerSite(t *testing.T) {
	// on then a later off must both resolve to "not locked" after compaction;
	// keeping only the latest (off) record is enough and correct.
	entries := []Entry{MaintenanceEntry("/a", true), MaintenanceEntry("/a", false)}
	for i := 0; i < 20; i++ {
		entries = append(entries, Entry{Event: EventDryRun})
	}
	got := CompactHistory(entries, 2)
	if LockedSites(got)["/a"] {
		t.Errorf("/a should read back unlocked after compaction: %v", LockedSites(got))
	}
}

func TestCompactBelowThresholdDoesNotRewrite(t *testing.T) {
	// History returns three entries (< CompactThreshold): Compact reads once and
	// issues no rewrite.
	r := &fakeRunner{res: remote.Result{Stdout: `{"event":"dry-run"}
{"event":"dry-run"}
{"event":"dry-run"}
`}}
	if err := Compact(context.Background(), r, "/home/u"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("expected one command (the History read), got %d: %v", len(r.cmds), r.cmds)
	}
	if strings.Contains(r.cmds[0], "mv ") {
		t.Errorf("a below-threshold file must not be rewritten:\n%s", r.cmds[0])
	}
}

func TestCompactAboveThresholdRewritesAtomically(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"event":"migrate","site":"/dest/keep"}` + "\n")
	b.WriteString(`{"event":"maintenance","site":"/src/locked","details":{"state":"on"}}` + "\n")
	for i := 0; i < CompactThreshold; i++ {
		b.WriteString(`{"event":"dry-run"}` + "\n")
	}
	r := &fakeRunner{res: remote.Result{Stdout: b.String()}}
	if err := Compact(context.Background(), r, "/home/u"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(r.cmds) != 2 {
		t.Fatalf("expected History read + rewrite, got %d commands", len(r.cmds))
	}
	rewrite := r.cmds[1]
	for _, want := range []string{
		"cat > '/home/u/.rehost/history.jsonl.tmp' <<'REHOST_STATE'",
		"mv '/home/u/.rehost/history.jsonl.tmp' '/home/u/.rehost/history.jsonl'",
		`"event":"migrate","site":"/dest/keep"`,      // critical record carried over
		`"event":"maintenance","site":"/src/locked"`, // open lock carried over
	} {
		if !strings.Contains(rewrite, want) {
			t.Errorf("rewrite command missing %q:\n%s", want, rewrite[:min(400, len(rewrite))])
		}
	}
}

func TestAcquireLock(t *testing.T) {
	r := &fakeRunner{}
	if err := AcquireLock(context.Background(), r, "/home/d"); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 1 || !strings.Contains(r.cmds[0], "mkdir '/home/d/.rehost/lock'") {
		t.Errorf("lock should be an atomic mkdir: %v", r.cmds)
	}

	held := &fakeRunner{res: remote.Result{ExitCode: lockHeldExit, Stdout: "started 2026-08-28T10:00:00Z\n"}}
	err := AcquireLock(context.Background(), held, "/home/d")
	if err == nil || !strings.Contains(err.Error(), "another rehost run") ||
		!strings.Contains(err.Error(), "/home/d/.rehost/lock") || !strings.Contains(err.Error(), "2026-08-28T10:00:00Z") {
		t.Errorf("held lock should explain holder and cleanup, got %v", err)
	}

	broken := &fakeRunner{res: remote.Result{ExitCode: 2, Stderr: "mkdir: permission denied"}}
	if err := AcquireLock(context.Background(), broken, "/home/d"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("other mkdir failures should surface stderr, got %v", err)
	}
}

func TestReleaseLock(t *testing.T) {
	r := &fakeRunner{}
	if err := ReleaseLock(context.Background(), r, "/home/d"); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 1 || !strings.Contains(r.cmds[0], "rm -rf '/home/d/.rehost/lock'") {
		t.Errorf("release should remove the lock dir: %v", r.cmds)
	}
}
