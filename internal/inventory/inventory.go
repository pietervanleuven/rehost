// Package inventory measures what a site's files weigh on the source host:
// total size, the largest top-level directories, and how much the
// framework's cache/backup directories contribute — the candidates for
// transfer exclusion. Everything is best-effort: a host without du/sort
// yields a partial inventory, never an error the caller must handle.
package inventory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pietervanleuven/go-ssh"
)

// Entry is one measured path.
type Entry struct {
	Path   string `json:"path"` // relative to the install root
	SizeKB int64  `json:"size_kb"`
}

// Inventory is the size picture of one install root.
type Inventory struct {
	TotalKB int64 `json:"total_kb"`
	// Top lists the largest immediate subdirectories, biggest first.
	Top []Entry `json:"top,omitempty"`
	// Suggested lists framework cache/backup directories that exist and
	// could be excluded from transfer, biggest first.
	Suggested []Entry `json:"suggested,omitempty"`
}

// runner is the slice of ssh.Client this package needs; tests use a fake.
type runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// topLimit bounds the breakdown so a vhost with hundreds of directories
// stays readable.
const topLimit = 10

// Take measures one install root. suggestions are root-relative directories
// worth excluding when present (from the framework recipe). Only transport
// errors are returned; missing tools or paths degrade to partial data.
func Take(ctx context.Context, r runner, root string, suggestions []string) (*Inventory, error) {
	inv := &Inventory{}

	res, err := r.Run(ctx, "du -sk "+ssh.ShellQuote(root)+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	if entries := parseDu(res, root, "\n"); len(entries) > 0 {
		inv.TotalKB = entries[0].SizeKB
	}

	// Largest immediate subdirectories, dot-directories included (.git can
	// dwarf the site). NUL-terminated GNU du first so odd directory names
	// survive — few enough entries that no remote sort/head is needed;
	// plain newline output with a remote pre-sort as the fallback.
	globs := ssh.ShellQuote(root) + "/*/ " + ssh.ShellQuote(root) + "/.[!.]*/"
	res, err = r.Run(ctx, "du -sk -0 "+globs+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	var top []Entry
	if strings.Contains(res.Stdout, "\x00") {
		top = parseDu(res, root, "\x00")
	} else {
		res, err = r.Run(ctx, "du -sk "+globs+" 2>/dev/null | sort -rn | head -40")
		if err != nil {
			return nil, err
		}
		top = parseDu(res, root, "\n")
	}
	inv.Top = biggestFirst(top, topLimit)

	if len(suggestions) > 0 {
		var quoted []string
		for _, s := range suggestions {
			quoted = append(quoted, ssh.ShellQuote(root+"/"+s))
		}
		res, err = r.Run(ctx, "du -sk "+strings.Join(quoted, " ")+" 2>/dev/null")
		if err != nil {
			return nil, err
		}
		inv.Suggested = biggestFirst(parseDu(res, root, "\n"), len(suggestions))
	}
	return inv, nil
}

// parseDu reads `du -sk` output records ("<kb>\t<path>" terminated by sep)
// into entries with root-relative paths. Paths are kept byte-exact — a
// directory named " cache " is a real directory. A du that was killed
// mid-run (exit above 1, or the session dying) yields nothing: sizes from a
// truncated run must not be reported as measurements. Exit 1 with output is
// du's unreadable-subdir noise and stays usable.
func parseDu(res ssh.Result, root, sep string) []Entry {
	if res.ExitCode != 0 && (res.ExitCode != 1 || res.Stdout == "") {
		return nil
	}
	var entries []Entry
	for _, rec := range strings.Split(res.Stdout, sep) {
		kbStr, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(kbStr), 10, 64)
		if err != nil {
			continue
		}
		p = strings.TrimSuffix(p, "/")
		rel := strings.TrimPrefix(p, strings.TrimSuffix(root, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = "."
		}
		entries = append(entries, Entry{Path: rel, SizeKB: kb})
	}
	return entries
}

// biggestFirst sorts by size descending and keeps at most limit entries,
// dropping empty ones (du reports 0 for an empty dir — not worth excluding).
func biggestFirst(entries []Entry, limit int) []Entry {
	var kept []Entry
	for _, e := range entries {
		if e.SizeKB > 0 && e.Path != "." {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].SizeKB > kept[j].SizeKB })
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

// HumanKB renders a KiB count for humans. Shared by every report that talks
// about sizes.
func HumanKB(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%.1f MiB", float64(kb)/1024)
	default:
		return fmt.Sprintf("%d KiB", kb)
	}
}
