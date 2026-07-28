package tui

import (
	"fmt"

	"github.com/pietervanleuven/rehost/internal/check"
)

// migratePreflightSchema versions the migrate pre-flight JSON envelope so
// later phases can extend it without breaking parsers.
const migratePreflightSchema = "rehost.migrate-preflight.v1"

// MigratePreflightView is what the migrate pre-flight renders: the combined
// compatibility-gate and destination-state results, whether the pre-flight
// passed (no blockers), and — when it passed — the honest-stop notice that the
// execution steps are not wired yet. It is pure data assembled by the caller.
type MigratePreflightView struct {
	Results []check.Result
	Passed  bool
	Notice  string
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

func (r jsonRenderer) MigratePreflight(v MigratePreflightView) error {
	results := v.Results
	if results == nil {
		results = []check.Result{}
	}
	blockers, warnings := check.Summarize(results)
	return encodeJSON(r.out, MigratePreflightEnvelope{
		Schema:   migratePreflightSchema,
		Results:  results,
		Blockers: blockers,
		Warnings: warnings,
		Passed:   v.Passed,
		Notice:   v.Notice,
	})
}
