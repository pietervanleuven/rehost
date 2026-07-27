package tui

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"charm.land/lipgloss/v2"

	"github.com/placeholder/rehost/internal/check"
)

// checkSchema versions the check report's JSON envelope.
const checkSchema = "rehost.check-report.v1"

var warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

// checkSummary is the one-line verdict under the checklist.
func checkSummary(blockers, warnings int) string {
	switch {
	case blockers > 0:
		return fmt.Sprintf("%d blocker(s), %d warning(s) — fix the blockers and rerun 'rehost check'.", blockers, warnings)
	case warnings > 0:
		return fmt.Sprintf("Green with %d warning(s) — migration can proceed.", warnings)
	default:
		return "All green — migration can proceed."
	}
}

func (r styledRenderer) CheckReport(results []check.Result) error {
	return r.checklist("Compatibility check", results)
}

func (r styledRenderer) checklist(title string, results []check.Result) error {
	fmt.Fprintln(r.out, titleStyle.Render(title))
	fmt.Fprintln(r.out)
	for _, res := range results {
		var mark string
		switch res.Severity {
		case check.Ok:
			mark = okStyle.Render("✓")
		case check.Info:
			mark = dimStyle.Render("·")
		case check.Warning:
			mark = warnStyle.Render("!")
		default:
			mark = missingStyle.Render("✗")
		}
		fmt.Fprintf(r.out, "  %s %-32s %s\n", mark, res.Title, dimStyle.Render(res.Detail))
	}
	blockers, warnings := check.Summarize(results)
	verdict := checkSummary(blockers, warnings)
	fmt.Fprintln(r.out)
	switch {
	case blockers > 0:
		fmt.Fprintln(r.out, missingStyle.Render(verdict))
	case warnings > 0:
		fmt.Fprintln(r.out, warnStyle.Render(verdict))
	default:
		fmt.Fprintln(r.out, okStyle.Render(verdict))
	}
	return nil
}

func (r plainRenderer) CheckReport(results []check.Result) error {
	return r.checklist(results)
}

func (r plainRenderer) checklist(results []check.Result) error {
	w := tabwriter.NewWriter(r.out, 2, 4, 2, ' ', 0)
	for _, res := range results {
		label := "[" + string(res.Severity) + "]"
		if res.Severity == check.Blocker {
			label = "[BLOCKER]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", label, res.Title, res.Detail)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	blockers, warnings := check.Summarize(results)
	fmt.Fprintf(r.out, "\n%s\n", checkSummary(blockers, warnings))
	return nil
}

// CheckEnvelope is the versioned JSON output shape of checklist reports.
type CheckEnvelope struct {
	Schema   string         `json:"schema"`
	Results  []check.Result `json:"results"`
	Blockers int            `json:"blockers"`
	Warnings int            `json:"warnings"`
	Green    bool           `json:"green"`
}

func (r jsonRenderer) CheckReport(results []check.Result) error {
	if results == nil {
		results = []check.Result{}
	}
	blockers, warnings := check.Summarize(results)
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(CheckEnvelope{
		Schema:   checkSchema,
		Results:  results,
		Blockers: blockers,
		Warnings: warnings,
		Green:    blockers == 0,
	})
}
