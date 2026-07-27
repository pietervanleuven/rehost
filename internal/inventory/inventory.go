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

	"github.com/placeholder/rehost/internal/ssh"
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
	if entries := parseDu(res.Stdout, root); len(entries) > 0 {
		inv.TotalKB = entries[0].SizeKB
	}

	// Largest immediate subdirectories. sort/head keep the transfer small on
	// hosts that have them; the client-side sort below does the real work.
	res, err = r.Run(ctx, "du -sk "+ssh.ShellQuote(root)+"/*/ 2>/dev/null | sort -rn | head -40")
	if err != nil {
		return nil, err
	}
	inv.Top = biggestFirst(parseDu(res.Stdout, root), topLimit)

	if len(suggestions) > 0 {
		var quoted []string
		for _, s := range suggestions {
			quoted = append(quoted, ssh.ShellQuote(root+"/"+s))
		}
		res, err = r.Run(ctx, "du -sk "+strings.Join(quoted, " ")+" 2>/dev/null")
		if err != nil {
			return nil, err
		}
		inv.Suggested = biggestFirst(parseDu(res.Stdout, root), len(suggestions))
	}
	return inv, nil
}

// parseDu reads `du -sk` output lines ("<kb>\t<path>") into entries with
// paths relative to root.
func parseDu(stdout, root string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(stdout, "\n") {
		kbStr, path, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(kbStr), 10, 64)
		if err != nil {
			continue
		}
		path = strings.TrimSuffix(path, "/")
		rel := strings.TrimPrefix(path, strings.TrimSuffix(root, "/"))
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
