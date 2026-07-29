package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// maxRecentRuns caps how many runs the status summary carries; history shows
// the full log.
const maxRecentRuns = 5

// stateRunner is the slice of ssh.Client that reading run history needs. A
// fake implements it in tests so these commands stay unit-testable without a
// real SSH connection. *ssh.Client satisfies it.
type stateRunner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

func newHistoryCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show the run history recorded on the source host, newest first",
		Long: `history connects to the source and prints the run log rehost keeps in
<home>/.rehost/history.jsonl on that host — every plan --dry-run and every
migrate leave a trace there. It changes nothing on either host.

No runs recorded yet is a normal, successful outcome. Use --json for a
machine-readable list of entries.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHistory(cmd, opts)
		},
	}
	return cmd
}

func newStatusCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show where you are in the flow and what has run",
		Long: `status summarizes the migration's state: the project file and hosts, the
sites plan has detected, and the most recent runs from the source's history
(the last dry-run and its outcome). It reads the source's run log and changes
nothing on either host.

Use --json for a machine-readable summary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd, opts)
		},
	}
	return cmd
}

func runHistory(cmd *cobra.Command, opts *options) error {
	u := newUI(cmd, opts)
	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	client, caps, err := dialSource(cmd.Context(), f, u)
	if err != nil {
		if u.mode == tui.ModeJSON {
			u.renderer.Error(err) // keep stdout machine-readable
		}
		return err
	}
	defer func() { _ = client.Close() }()
	return historyReport(cmd.Context(), client, caps.Home, u.renderer)
}

func runStatus(cmd *cobra.Command, opts *options) error {
	u := newUI(cmd, opts)
	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	client, caps, err := dialSource(cmd.Context(), f, u)
	if err != nil {
		if u.mode == tui.ModeJSON {
			u.renderer.Error(err) // keep stdout machine-readable
		}
		return err
	}
	defer func() { _ = client.Close() }()
	return statusReport(cmd.Context(), client, caps.Home, opts.projectFile, f, u.renderer)
}

// historyReport reads the run history over r and renders it newest-first. It
// is the SSH-free seam the tests exercise: any stateRunner (real client or
// fake) drives it, so corrupt-line tolerance and the empty case are covered
// end-to-end. state.History treats a missing file as (nil, nil), so no
// history is a clean, exit-0 render.
func historyReport(ctx context.Context, r stateRunner, home string, renderer tui.Renderer) error {
	entries, err := state.History(ctx, r, home)
	if err != nil {
		return fmt.Errorf("reading run history: %w", err)
	}
	return renderer.HistoryReport(newestFirst(entries))
}

// statusReport assembles the flow summary from the project file and the
// source history read over r, then renders it. Like historyReport, r is the
// seam that keeps it testable without SSH.
func statusReport(ctx context.Context, r stateRunner, home, projectFile string, f *project.File, renderer tui.Renderer) error {
	entries, err := state.History(ctx, r, home)
	if err != nil {
		return fmt.Errorf("reading run history: %w", err)
	}
	return renderer.StatusReport(buildStatusView(projectFile, f, newestFirst(entries)))
}

// buildStatusView turns the project file and newest-first history into the
// renderer's data. It invents nothing: sites come from what plan persisted,
// runs from what the source actually recorded, and migrate is honestly
// reported as not yet implemented.
func buildStatusView(projectFile string, f *project.File, recent []state.Entry) tui.StatusView {
	sites := make([]tui.StatusSite, 0, len(f.Sites))
	for _, s := range f.Sites {
		sites = append(sites, tui.StatusSite{Framework: s.Framework, Root: s.Root, Version: s.Version})
	}
	if len(recent) > maxRecentRuns {
		recent = recent[:maxRecentRuns]
	}
	var dest string
	if f.Destination != nil {
		dest = targetLabel(f.Destination.SSHConfig())
	}
	return tui.StatusView{
		ProjectFile:        projectFile,
		Source:             targetLabel(f.Source.SSHConfig()),
		Destination:        dest,
		Sites:              sites,
		Recent:             recent,
		MigrateImplemented: false, // migrate is a Phase 3 stub
	}
}

// newestFirst reverses state.History's oldest-first slice in place so reports
// lead with the most recent run.
func newestFirst(entries []state.Entry) []state.Entry {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}

// loadProject loads the project file, turning a missing file into the same
// "run init first" guidance check gives.
func loadProject(path string) (*project.File, error) {
	f, err := project.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no project file at %s — run 'rehost init' first", path)
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// dialSource connects to the source and probes it, returning the client and
// its capabilities (the account home the history file lives under, tool
// availability the recipe layers gate on). The caller owns closing the client.
func dialSource(ctx context.Context, f *project.File, u ui) (*ssh.Client, *ssh.Capabilities, error) {
	cfg := f.Source.SSHConfig()
	u.progress("source: connecting to %s…", targetLabel(cfg))
	client, err := ssh.Dial(ctx, cfg, u.prompter)
	if err != nil {
		return nil, nil, fmt.Errorf("source: %w", err)
	}
	caps, err := ssh.Probe(ctx, client)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("source: %w", err)
	}
	return client, caps, nil
}
