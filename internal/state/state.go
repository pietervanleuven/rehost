// Package state keeps the migration's paper trail on the source host: a
// hidden folder holding an append-only history of what rehost did there
// (dry runs now, migrations later), which the future status/history
// commands read back. PLAN.md §6 Phase 2 names the folder ".migrate-cli/";
// the binary has since been renamed to rehost, so the folder is ".rehost/".
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/pietervanleuven/go-ssh"
)

// historyFile is the append-only run log inside Dir, one JSON object per line.
const historyFile = "history.jsonl"

// Event names for the Entry.Event field. Record writes them and History
// readers match on them, so they are shared constants rather than scattered
// string literals: EventDryRun for a plan --dry-run, EventMigrate for a
// completed migration onto a destination docroot.
const (
	EventDryRun  = "dry-run"
	EventMigrate = "migrate"
	// EventMaintenance records a maintenance-mode toggle on a source site,
	// written before the framework command runs so a crashed run leaves a
	// trace unlock can recover. The direction lives in Details.
	EventMaintenance = "maintenance"
	// EventMigrateDB records that rehost imported into a specific destination
	// database (identity in Details[databaseKey]). It is distinct from
	// EventMigrate — a docroot record written after file sync, before any DB
	// write — so the destination-DB overwrite guard is only waived once rehost
	// has actually filled that database, never merely by a files-only run.
	EventMigrateDB = "migrate-db"
)

// databaseKey holds the destination database identity in an EventMigrateDB
// entry's Details.
const databaseKey = "database"

const (
	maintenanceStateKey = "state"
	maintenanceOn       = "on"
	maintenanceOff      = "off"
)

// heredocMarker delimits the JSON line fed to the remote append. Quoted at
// the redirect, so the content passes through the shell literally. A JSON
// line can never collide with it — json.Marshal output starts with '{'.
const heredocMarker = "REHOST_STATE"

// Entry is one recorded run. Entries must never contain secrets — the
// history file lives on the source host and is plain JSON; callers are
// responsible for keeping passwords and keys out of Details.
type Entry struct {
	Time    time.Time         `json:"time"`           // RFC 3339 via time.Time's default JSON
	Event   string            `json:"event"`          // e.g. "dry-run"
	Site    string            `json:"site,omitempty"` // install root, when per-site
	Details map[string]string `json:"details,omitempty"`
}

// runner is the slice of ssh.Client the state store needs; tests use a fake.
type runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// Dir returns the state directory for a home ("<home>/.rehost"). An empty
// home means the SSH account's home directory, so the path stays relative
// (like homeOrDot elsewhere).
func Dir(home string) string {
	if home == "" {
		home = "."
	}
	return path.Join(home, ".rehost")
}

// Record appends one entry to <home>/.rehost/history.jsonl on the host in a
// single remote command: create the directory (0700), append the JSON line
// via a quoted heredoc, and tighten the file to 0600. A zero e.Time is
// stamped with the current UTC time. Unlike the best-effort gatherers, a
// failed append is a real error — silently losing state would defeat the
// history's purpose — so a non-zero exit comes back as an error carrying
// the first stderr line; transport errors propagate as-is.
func Record(ctx context.Context, r runner, home string, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	line, err := json.Marshal(e) // a single line: json.Marshal emits no newlines
	if err != nil {
		return fmt.Errorf("encoding state entry: %w", err)
	}

	dir := ssh.ShellQuote(Dir(home))
	file := ssh.ShellQuote(path.Join(Dir(home), historyFile))
	cmd := "mkdir -p " + dir + " && chmod 700 " + dir +
		" && cat >> " + file + " <<'" + heredocMarker + "' && chmod 600 " + file + "\n" +
		string(line) + "\n" + heredocMarker

	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("recording state on host: %s", ssh.FirstLine(res.Stderr))
	}
	return nil
}

const (
	// CompactThreshold is the entry count above which Compact rewrites the
	// history file, and CompactKeepRecent is how many trailing entries it then
	// retains for display. The semantic-critical records (see CompactHistory)
	// are kept regardless of these bounds.
	CompactThreshold  = 2000
	CompactKeepRecent = 1000
)

// Rewrite atomically replaces the on-host history file with entries, one JSON
// line each, via a temp file and mv so a crash never leaves it half-written.
// It is the counterpart to Record's append for the rare compaction rewrite.
func Rewrite(ctx context.Context, r runner, home string, entries []Entry) error {
	var b strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e) // one line: json.Marshal emits no newlines
		if err != nil {
			return fmt.Errorf("encoding state entry: %w", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}

	dir := ssh.ShellQuote(Dir(home))
	file := ssh.ShellQuote(path.Join(Dir(home), historyFile))
	tmp := ssh.ShellQuote(path.Join(Dir(home), historyFile+".tmp"))
	cmd := "mkdir -p " + dir + " && cat > " + tmp + " <<'" + heredocMarker +
		"' && chmod 600 " + tmp + " && mv " + tmp + " " + file + "\n" +
		b.String() + heredocMarker

	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("rewriting state on host: %s", ssh.FirstLine(res.Stderr))
	}
	return nil
}

// Compact bounds the on-host history file: when it holds more than
// CompactThreshold entries it rewrites it down to CompactHistory's kept subset.
// It is best-effort maintenance (callers ignore the error) that never changes
// what MigratedSites, MigratedDatabases or LockedSites read back — it refuses
// to rewrite if it somehow would. A file at or under the threshold is left
// untouched, so the common case is one History read and no write.
func Compact(ctx context.Context, r runner, home string) error {
	entries, err := History(ctx, r, home)
	if err != nil {
		return err
	}
	if len(entries) <= CompactThreshold {
		return nil
	}
	kept := CompactHistory(entries, CompactKeepRecent)
	if len(kept) >= len(entries) {
		return nil
	}
	if !sameSet(MigratedSites(kept), MigratedSites(entries)) ||
		!sameSet(MigratedDatabases(kept), MigratedDatabases(entries)) ||
		!sameSet(LockedSites(kept), LockedSites(entries)) {
		return nil // never let compaction drift the recovery/refusal reads
	}
	return Rewrite(ctx, r, home, kept)
}

// CompactHistory returns a bounded subset of entries (oldest-first order
// preserved) that keeps the migration paper trail intact while dropping stale
// bulk. An entry survives when it is among the last keepRecent entries, or it
// is the latest EventMigrate or EventMaintenance for its site, or the latest
// EventMigrateDB for its database identity. Those rules are exactly what makes
// MigratedSites and MigratedDatabases (the destination refusal exemptions) and
// LockedSites (unlock recovery) read back unchanged after compaction.
func CompactHistory(entries []Entry, keepRecent int) []Entry {
	n := len(entries)
	if keepRecent < 0 {
		keepRecent = 0
	}
	if n <= keepRecent {
		return entries
	}
	keep := make([]bool, n)
	for i := n - keepRecent; i < n; i++ {
		keep[i] = true
	}
	latest := map[string]int{} // "<event>\x00<site or db identity>" → last index
	for i, e := range entries {
		switch {
		case e.Event == EventMigrateDB && e.Details[databaseKey] != "":
			latest[e.Event+"\x00"+e.Details[databaseKey]] = i
		case e.Site != "" && (e.Event == EventMigrate || e.Event == EventMaintenance):
			latest[e.Event+"\x00"+e.Site] = i
		}
	}
	for _, i := range latest {
		keep[i] = true
	}
	out := make([]Entry, 0, n)
	for i, e := range entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

// sameSet reports whether two site sets hold the same keys.
func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// History reads all entries from <home>/.rehost/history.jsonl, oldest first.
// A host without the file yields (nil, nil) — no history yet is not an
// error. Corrupt lines are skipped so one bad record never hides the rest;
// transport errors propagate.
func History(ctx context.Context, r runner, home string) ([]Entry, error) {
	file := ssh.ShellQuote(path.Join(Dir(home), historyFile))
	res, err := r.Run(ctx, "cat "+file+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "" {
		return nil, nil
	}
	var entries []Entry
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// MaintenanceEntry builds the write-ahead record for a maintenance-mode
// toggle on site. Callers Record it BEFORE flipping maintenance so a crashed
// run is recoverable: on=true means "about to enable" (the site may be left
// locked), on=false means "disabled" (the window is closed). Keeping the
// on/off encoding here means LockedSites reads back exactly what this wrote.
func MaintenanceEntry(site string, on bool) Entry {
	value := maintenanceOff
	if on {
		value = maintenanceOn
	}
	return Entry{Event: EventMaintenance, Site: site, Details: map[string]string{maintenanceStateKey: value}}
}

// LockedSites returns the set of site roots that might still be in maintenance
// mode: those whose most recent EventMaintenance record is an "on" with no
// later "off". It is the recovery read side that lets unlock find sites a
// crashed run may have left locked. Entries are processed in order (History
// returns them oldest-first), so a later off clears an earlier on. Entries
// with no Site, or a state other than on/off, are ignored. Mirrors
// MigratedSites: a membership set the caller can probe per site.
func LockedSites(entries []Entry) map[string]bool {
	locked := map[string]bool{}
	for _, e := range entries {
		if e.Event != EventMaintenance || e.Site == "" {
			continue
		}
		switch e.Details[maintenanceStateKey] {
		case maintenanceOn:
			locked[e.Site] = true
		case maintenanceOff:
			delete(locked, e.Site)
		}
	}
	return locked
}

// MigratedSites returns the set of site roots that have a completed
// EventMigrate record in entries — the destination docroots rehost itself
// populated. It lets a rerun tell a docroot it filled (safe to converge onto)
// apart from a stranger's non-empty docroot (a collision to refuse). Entries
// with no Site are ignored: a docroot is only "rehost-owned" when a migrate
// record names it. The future migrate step records one via
// Record(ctx, r, home, Entry{Event: EventMigrate, Site: <destRoot>}).
func MigratedSites(entries []Entry) map[string]bool {
	sites := map[string]bool{}
	for _, e := range entries {
		if e.Event == EventMigrate && e.Site != "" {
			sites[e.Site] = true
		}
	}
	return sites
}

// DatabaseMigratedEntry builds the record written on the destination after a
// database import succeeds. identity is an opaque key the caller keeps stable
// across runs (name/user/host/port) so a rerun recognizes the database it
// filled. Keeping the encoding here means MigratedDatabases reads back exactly
// what this wrote.
func DatabaseMigratedEntry(identity string) Entry {
	return Entry{Event: EventMigrateDB, Details: map[string]string{databaseKey: identity}}
}

// MigratedDatabases returns the set of destination database identities rehost
// itself imported into (an EventMigrateDB record names each). It lets a rerun
// tell a database it filled — safe to re-import into — from a stranger's
// non-empty database the import would overwrite. Mirrors MigratedSites.
func MigratedDatabases(entries []Entry) map[string]bool {
	dbs := map[string]bool{}
	for _, e := range entries {
		if e.Event == EventMigrateDB && e.Details[databaseKey] != "" {
			dbs[e.Details[databaseKey]] = true
		}
	}
	return dbs
}

// runLockDir is the advisory cross-run lock inside Dir. mkdir is atomic, so
// exactly one run can create it; a second concurrent migrate would otherwise
// interleave tar pipes into the same docroots and race the history file's
// read-modify-rewrite compaction.
const runLockDir = "lock"

// lockHeldExit distinguishes "the lock directory already exists" from other
// mkdir failures without parsing stderr.
const lockHeldExit = 47

// LockPath returns the advisory lock directory for a home, for messages.
func LockPath(home string) string {
	return path.Join(Dir(home), runLockDir)
}

// AcquireLock takes the advisory per-host run lock. A held lock is an error
// telling the user who to check and how to clear a stale one — rehost cannot
// tell a live concurrent run from a crashed one, so it never steals the lock.
func AcquireLock(ctx context.Context, r runner, home string) error {
	dir := ssh.ShellQuote(Dir(home))
	lock := ssh.ShellQuote(LockPath(home))
	info := ssh.ShellQuote(path.Join(LockPath(home), "info"))
	stamp := time.Now().UTC().Format(time.RFC3339)
	cmd := "mkdir -p " + dir + " && { mkdir " + lock + " 2>/dev/null || { cat " + info + " 2>/dev/null; exit " + fmt.Sprint(lockHeldExit) + "; }; }" +
		" && echo " + ssh.ShellQuote("started "+stamp) + " > " + info
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	switch {
	case res.ExitCode == lockHeldExit:
		holder := strings.TrimSpace(res.Stdout)
		if holder == "" {
			holder = "unknown start time"
		}
		return fmt.Errorf("another rehost run appears to be active on this host (%s) — wait for it to finish; if it crashed, remove %s there and rerun", holder, LockPath(home))
	case res.ExitCode != 0:
		return fmt.Errorf("taking the run lock: %s", ssh.FirstLine(res.Stderr))
	}
	return nil
}

// ReleaseLock clears the advisory run lock. Best-effort by nature: a failure
// only means the next run sees a stale lock and its error explains the fix.
func ReleaseLock(ctx context.Context, r runner, home string) error {
	res, err := r.Run(ctx, "rm -rf "+ssh.ShellQuote(LockPath(home)))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("releasing the run lock: %s", ssh.FirstLine(res.Stderr))
	}
	return nil
}
