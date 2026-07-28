// Package tui renders rehost output in three modes — styled for terminals,
// plain for non-TTY (CI, pipes), JSON for machines — and provides the
// interactive prompters the ssh layer needs. tui imports ssh, never the
// reverse.
package tui

import (
	"io"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/inventory"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
)

// Mode selects the output format.
type Mode int

const (
	ModeStyled Mode = iota
	ModePlain
	ModeJSON
)

// HostReport pairs a host's role with what was found on it: probed
// capabilities, detected framework installs, and their file inventories
// (keyed by install root; entries may be missing when not measured).
type HostReport struct {
	Role        string // "source" or "destination"
	Caps        *ssh.Capabilities
	Installs    []detect.Install
	Inventories map[string]*inventory.Inventory
}

// Renderer writes rehost reports in one output mode.
type Renderer interface {
	// PlanReport renders the capability/detection report and — when a dry
	// run was performed — its results. One call so JSON consumers get one
	// document, never two.
	PlanReport(reports []HostReport, dryRun []check.Result, ranDryRun bool) error
	CheckReport(results []check.Result) error
	// MigratePreflight renders the migrate pre-flight report: the combined
	// compatibility-gate and destination-state results, plus the honest-stop
	// notice when it passed. Used for a refusal/blocker outcome, where nothing
	// was synced.
	MigratePreflight(v MigratePreflightView) error
	// MigrateReport renders the combined migrate report after a green
	// pre-flight: the pre-flight section plus per-site file-sync results and the
	// honest-stop notice that the migration is not complete yet.
	MigrateReport(v MigrateReportView) error
	// HistoryReport renders the source run history, newest-first. An empty
	// slice is the normal "no runs recorded yet" outcome, not an error.
	HistoryReport(entries []state.Entry) error
	// StatusReport renders the "where am I in the flow" summary.
	StatusReport(v StatusView) error
	Error(err error)
}

// New returns the renderer for a mode, writing to out.
func New(mode Mode, out io.Writer) Renderer {
	switch mode {
	case ModePlain:
		return plainRenderer{out: out}
	case ModeJSON:
		return jsonRenderer{out: out}
	default:
		return styledRenderer{out: out}
	}
}
