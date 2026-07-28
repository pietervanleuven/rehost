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

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// historyFile is the append-only run log inside Dir, one JSON object per line.
const historyFile = "history.jsonl"

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
