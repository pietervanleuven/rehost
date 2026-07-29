package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pietervanleuven/rehost/internal/state"
)

// Schemas version the status/history JSON envelopes so later phases can
// extend them without breaking parsers.
const (
	historySchema = "rehost.history.v1"
	statusSchema  = "rehost.status.v1"
)

// StatusSite is one site plan persisted into the project file, as shown by
// the status summary.
type StatusSite struct {
	Framework string `json:"framework"`
	Root      string `json:"root"`
	Version   string `json:"version,omitempty"`
}

// StatusView is the "where am I in the flow" picture status renders. It is
// pure data assembled by the caller from the project file and the source's
// run history; the renderer turns it into narrative.
type StatusView struct {
	ProjectFile string        // the migrate.yaml this status is about
	Source      string        // connected user@host identity of the source
	Destination string        // configured destination, or "" when none
	Sites       []StatusSite  // persisted by plan; empty until plan has run
	Recent      []state.Entry // newest-first source run history (may be capped)
	// MigrateImplemented is false for now: migrate is a Phase 3 stub. The
	// renderer says so honestly instead of implying the step is available.
	MigrateImplemented bool
}

// lastEvent returns the most recent entry with the given event, or nil.
// Recent is newest-first, so the first match wins.
func (v StatusView) lastEvent(event string) *state.Entry {
	for i := range v.Recent {
		if v.Recent[i].Event == event {
			return &v.Recent[i]
		}
	}
	return nil
}

// humanAge renders how long ago t was, from now, in coarse units. A zero
// time is unknown.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now" // clock skew: never render a negative age
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatDetails renders an entry's details map deterministically (keys
// sorted) so output is stable across runs.
func formatDetails(d map[string]string) string {
	if len(d) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+d[k])
	}
	return strings.Join(parts, ", ")
}

// entryLine is the compact one-line form of a history entry shared by the
// text renderers: "<age>  <event>  [site]  details".
func entryLine(e state.Entry) string {
	parts := []string{e.Event}
	if e.Site != "" {
		parts = append(parts, e.Site)
	}
	if d := formatDetails(e.Details); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, "  ")
}

// --- styled ---

func (r styledRenderer) HistoryReport(entries []state.Entry) error {
	fprintln(r.out, titleStyle.Render("Run history (source)"))
	fprintln(r.out)
	if len(entries) == 0 {
		fprintln(r.out, dimStyle.Render("No runs recorded yet — run 'rehost plan --dry-run'."))
		return nil
	}
	for _, e := range entries {
		fprintf(r.out, "  %s  %s\n", okStyle.Render(fmt.Sprintf("%-8s", humanAge(e.Time))), entryLine(e))
	}
	return nil
}

func (r styledRenderer) StatusReport(v StatusView) error {
	fprintln(r.out, titleStyle.Render("rehost status"))
	fprintln(r.out)
	statusLines(v, func(label, value string) {
		fprintf(r.out, "  %s %s\n", roleStyle.Render(fmt.Sprintf("%-12s", label)), value)
	})
	return nil
}

// --- plain (non-TTY / CI) ---

func (r plainRenderer) HistoryReport(entries []state.Entry) error {
	if len(entries) == 0 {
		fprintln(r.out, "no runs recorded yet")
		return nil
	}
	w := tabwriter.NewWriter(r.out, 2, 4, 2, ' ', 0)
	for _, e := range entries {
		fprintf(w, "%s\t%s\n", humanAge(e.Time), entryLine(e))
	}
	return w.Flush()
}

func (r plainRenderer) StatusReport(v StatusView) error {
	w := tabwriter.NewWriter(r.out, 2, 4, 2, ' ', 0)
	statusLines(v, func(label, value string) {
		fprintf(w, "%s:\t%s\n", label, value)
	})
	return w.Flush()
}

// statusLines drives the flow summary shared by the text renderers, emitting
// one (label, value) pair per step so each renderer only decides styling.
func statusLines(v StatusView, emit func(label, value string)) {
	emit("project", v.ProjectFile)
	emit("source", v.Source)
	if v.Destination != "" {
		emit("destination", v.Destination)
	} else {
		emit("destination", "not configured — add one before 'rehost check'")
	}

	if len(v.Sites) == 0 {
		emit("sites", "none detected yet — run 'rehost plan'")
	} else {
		for i, s := range v.Sites {
			label := "sites"
			if i > 0 {
				label = ""
			}
			line := s.Framework
			if s.Version != "" {
				line += " " + s.Version
			}
			line += " at " + s.Root
			emit(label, line)
		}
	}

	if dry := v.lastEvent("dry-run"); dry != nil {
		line := "last run " + humanAge(dry.Time)
		if d := formatDetails(dry.Details); d != "" {
			line += " (" + d + ")"
		}
		emit("dry-run", line)
	} else {
		emit("dry-run", "not run yet — run 'rehost plan --dry-run'")
	}

	if v.MigrateImplemented {
		if m := v.lastEvent("migrate"); m != nil {
			emit("migrate", "last run "+humanAge(m.Time))
		} else {
			emit("migrate", "not run yet — run 'rehost migrate'")
		}
	} else {
		emit("migrate", "not implemented yet (Phase 3) — see docs/PLAN.md §6")
	}
}

// --- JSON ---

// HistoryEnvelope is the versioned JSON shape of the history report.
type HistoryEnvelope struct {
	Schema  string        `json:"schema"`
	Count   int           `json:"count"`
	Entries []state.Entry `json:"entries"`
}

func (r jsonRenderer) HistoryReport(entries []state.Entry) error {
	if entries == nil {
		entries = []state.Entry{}
	}
	return encodeJSON(r.out, HistoryEnvelope{
		Schema:  historySchema,
		Count:   len(entries),
		Entries: entries,
	})
}

// StatusEnvelope is the versioned JSON shape of the status report.
type StatusEnvelope struct {
	Schema             string        `json:"schema"`
	ProjectFile        string        `json:"project_file"`
	Source             string        `json:"source"`
	Destination        string        `json:"destination,omitempty"`
	Sites              []StatusSite  `json:"sites"`
	RecentRuns         []state.Entry `json:"recent_runs"`
	LastDryRun         *state.Entry  `json:"last_dry_run,omitempty"`
	MigrateImplemented bool          `json:"migrate_implemented"`
}

func (r jsonRenderer) StatusReport(v StatusView) error {
	sites := v.Sites
	if sites == nil {
		sites = []StatusSite{}
	}
	recent := v.Recent
	if recent == nil {
		recent = []state.Entry{}
	}
	return encodeJSON(r.out, StatusEnvelope{
		Schema:             statusSchema,
		ProjectFile:        v.ProjectFile,
		Source:             v.Source,
		Destination:        v.Destination,
		Sites:              sites,
		RecentRuns:         recent,
		LastDryRun:         v.lastEvent("dry-run"),
		MigrateImplemented: v.MigrateImplemented,
	})
}

// encodeJSON writes one indented JSON document to out.
func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
