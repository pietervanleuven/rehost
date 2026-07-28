package tui

import (
	"fmt"
	"time"

	"github.com/pietervanleuven/rehost/internal/check"
)

// migratePreflightSchema versions the migrate pre-flight JSON envelope so
// later phases can extend it without breaking parsers.
const migratePreflightSchema = "rehost.migrate-preflight.v1"

// migrateSchema versions the combined migrate JSON envelope: the pre-flight
// section plus the per-site file-sync results. It stays a distinct schema from
// the pre-flight so a consumer can tell "pre-flight only" (a refusal/blocker,
// nothing synced) apart from "pre-flight passed and file sync ran".
const migrateSchema = "rehost.migrate.v1"

// MigratePreflightView is what the migrate pre-flight renders: the combined
// compatibility-gate and destination-state results, whether the pre-flight
// passed (no blockers), and — when it passed — the honest-stop notice that the
// execution steps are not wired yet. It is pure data assembled by the caller.
type MigratePreflightView struct {
	Results []check.Result
	Passed  bool
	Notice  string
}

// SiteSyncResult is one site's file-sync outcome, rendered per site in the
// migrate report. A non-empty Err means that site's sync failed (the run
// stopped there); the fields before it hold whatever the partial sync managed.
type SiteSyncResult struct {
	Site              string        `json:"site"`      // source docroot
	DestRoot          string        `json:"dest_root"` // destination docroot
	Compressed        bool          `json:"compressed"`
	FilesSent         int           `json:"files_sent"`
	BytesSent         int64         `json:"bytes_sent"` // logical (source manifest sizes; 0 when degraded)
	WireBytes         int64         `json:"wire_bytes"` // bytes over the relay (compressed when Compressed)
	FilesDeleted      int           `json:"files_deleted"`
	DestOnlyRemaining int           `json:"dest_only_remaining"` // destination-only files still present after sync
	UnsafePaths       int           `json:"unsafe_paths"`        // destination-only paths the safety check refused
	Duration          time.Duration `json:"duration_ns"`
	Err               string        `json:"error,omitempty"`
}

// MigrateReportView is the combined migrate report: the pre-flight section and
// the per-site file-sync results, plus the honest-stop notice that the
// migration is not complete (no DB import, config rewrite or cutover yet) and
// any non-fatal warnings (a failed history record downgrades to one). Complete
// is always false in Phase 3.
type MigrateReportView struct {
	Preflight MigratePreflightView
	Sites     []SiteSyncResult
	Complete  bool
	Notice    string
	Warnings  []string
}

// --- styled ---

func (r styledRenderer) MigratePreflight(v MigratePreflightView) error {
	if err := r.checklist("Migrate pre-flight", v.Results); err != nil {
		return err
	}
	if v.Passed && v.Notice != "" {
		fmt.Fprintln(r.out)
		fmt.Fprintln(r.out, dimStyle.Render(v.Notice))
	}
	return nil
}

func (r styledRenderer) MigrateReport(v MigrateReportView) error {
	if err := r.checklist("Migrate pre-flight", v.Preflight.Results); err != nil {
		return err
	}
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, titleStyle.Render("File sync"))
	fmt.Fprintln(r.out)
	for _, s := range v.Sites {
		mark := okStyle.Render("✓")
		if s.Err != "" {
			mark = missingStyle.Render("✗")
		}
		fmt.Fprintf(r.out, "  %s %s → %s\n", mark, s.Site, s.DestRoot)
		fmt.Fprintf(r.out, "      %s\n", dimStyle.Render(siteSyncLine(s)))
		if s.Err != "" {
			fmt.Fprintf(r.out, "      %s\n", missingStyle.Render(s.Err))
		}
	}
	for _, w := range v.Warnings {
		fmt.Fprintf(r.out, "\n  %s %s\n", warnStyle.Render("!"), warnStyle.Render(w))
	}
	if v.Notice != "" {
		fmt.Fprintln(r.out)
		fmt.Fprintln(r.out, dimStyle.Render(v.Notice))
	}
	return nil
}

// --- plain (non-TTY / CI) ---

func (r plainRenderer) MigratePreflight(v MigratePreflightView) error {
	if err := r.checklist(v.Results); err != nil {
		return err
	}
	if v.Passed && v.Notice != "" {
		fmt.Fprintln(r.out)
		fmt.Fprintln(r.out, v.Notice)
	}
	return nil
}

func (r plainRenderer) MigrateReport(v MigrateReportView) error {
	if err := r.checklist(v.Preflight.Results); err != nil {
		return err
	}
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "file sync:")
	for _, s := range v.Sites {
		status := "ok"
		if s.Err != "" {
			status = "FAILED"
		}
		fmt.Fprintf(r.out, "  [%s] %s -> %s  %s\n", status, s.Site, s.DestRoot, siteSyncLine(s))
		if s.Err != "" {
			fmt.Fprintf(r.out, "         error: %s\n", s.Err)
		}
	}
	for _, w := range v.Warnings {
		fmt.Fprintf(r.out, "  [warning] %s\n", w)
	}
	if v.Notice != "" {
		fmt.Fprintf(r.out, "\n%s\n", v.Notice)
	}
	return nil
}

// siteSyncLine is the one-line summary of a site's sync, shared by the styled
// and plain renderers.
func siteSyncLine(s SiteSyncResult) string {
	wire := humanBytes(s.WireBytes)
	if s.Compressed {
		wire += " gzip"
	}
	return fmt.Sprintf("%d files · %s logical · %s wire · %d deleted · %d dest-only · %d unsafe · %.1fs",
		s.FilesSent, humanBytes(s.BytesSent), wire, s.FilesDeleted, s.DestOnlyRemaining, s.UnsafePaths, s.Duration.Seconds())
}

// humanBytes renders a byte count as a compact human string for the report.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// --- JSON ---

// MigratePreflightEnvelope is the versioned JSON shape of the pre-flight report.
type MigratePreflightEnvelope struct {
	Schema   string         `json:"schema"`
	Results  []check.Result `json:"results"`
	Blockers int            `json:"blockers"`
	Warnings int            `json:"warnings"`
	Passed   bool           `json:"passed"`
	Notice   string         `json:"notice,omitempty"`
}

// preflightEnvelope builds the pre-flight JSON section from a view, so both the
// standalone pre-flight report and the combined migrate report share one shape.
func preflightEnvelope(v MigratePreflightView) MigratePreflightEnvelope {
	results := v.Results
	if results == nil {
		results = []check.Result{}
	}
	blockers, warnings := check.Summarize(results)
	return MigratePreflightEnvelope{
		Schema:   migratePreflightSchema,
		Results:  results,
		Blockers: blockers,
		Warnings: warnings,
		Passed:   v.Passed,
		Notice:   v.Notice,
	}
}

func (r jsonRenderer) MigratePreflight(v MigratePreflightView) error {
	return encodeJSON(r.out, preflightEnvelope(v))
}

// MigrateEnvelope is the versioned JSON shape of the combined migrate report:
// the pre-flight section plus the per-site file-sync results. Complete is
// always false in Phase 3 — file sync converges but DB import, config rewrite
// and cutover are not wired yet.
type MigrateEnvelope struct {
	Schema    string                   `json:"schema"`
	Preflight MigratePreflightEnvelope `json:"preflight"`
	Sites     []SiteSyncResult         `json:"sites"`
	Complete  bool                     `json:"complete"`
	Notice    string                   `json:"notice,omitempty"`
	Warnings  []string                 `json:"warnings,omitempty"`
}

func (r jsonRenderer) MigrateReport(v MigrateReportView) error {
	sites := v.Sites
	if sites == nil {
		sites = []SiteSyncResult{}
	}
	return encodeJSON(r.out, MigrateEnvelope{
		Schema:    migrateSchema,
		Preflight: preflightEnvelope(v.Preflight),
		Sites:     sites,
		Complete:  v.Complete,
		Notice:    v.Notice,
		Warnings:  v.Warnings,
	})
}
