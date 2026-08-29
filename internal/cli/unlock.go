package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func newUnlockCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Clear maintenance mode on the source after a crash",
		Long: `unlock recovers a source left in maintenance mode by an interrupted run.
It connects to the source, finds the sites that might still be locked — the
source's run history plus a live probe per site, trusting the live probe —
and clears maintenance mode on each via the framework's own mechanism
(wp-cli/drush, or the maintenance file as a fallback).

Nothing to unlock is a success. The exit status is non-zero only if the
source cannot be reached or a locked site could not be cleared.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUnlock(cmd, opts)
		},
	}
}

func runUnlock(cmd *cobra.Command, opts *options) error {
	return withSource(cmd, opts, func(ctx context.Context, u ui, f *project.File, client *ssh.Client, caps *ssh.Capabilities) error {
		view, failed, err := unlockSites(ctx, recipe.Host{Run: client, FS: detect.NewShellFS(client), Caps: caps}, caps.Home, caps.Target(), f)
		if err != nil {
			return u.fail(err)
		}
		if rerr := u.renderer.UnlockReport(view); rerr != nil {
			return rerr
		}
		if len(failed) > 0 {
			return fmt.Errorf("could not clear maintenance mode on %d site(s): %s", len(failed), strings.Join(failed, ", "))
		}
		return nil
	})
}

// unlockSites clears maintenance mode across a source's sites and is the
// SSH-free seam the tests drive with a fake recipe.Host. It returns the report
// view, the roots it could not unlock, and a transport error (which aborts the
// whole command). Ordinary per-site failures are carried in the view and the
// failed slice, not as an error — the run still reports every site.
//
// A site is treated as locked when the live probe says so, or when the probe
// cannot tell (Unknown) but history recorded it locked. History is the
// recovery hint; the live probe overrides it.
func unlockSites(ctx context.Context, h recipe.Host, home, source string, f *project.File) (tui.UnlockView, []string, error) {
	entries, err := state.History(ctx, h.Run, home)
	if err != nil {
		return tui.UnlockView{}, nil, fmt.Errorf("reading run history: %w", err)
	}
	historyLocked := state.LockedSites(entries)

	view := tui.UnlockView{Source: source}
	var failed []string
	covered := map[string]bool{}

	for _, s := range f.Sites {
		covered[s.Root] = true
		row, ok, transportErr := unlockOne(ctx, h, s, historyLocked[s.Root])
		if transportErr != nil {
			return tui.UnlockView{}, nil, transportErr
		}
		if !ok {
			failed = append(failed, s.Root)
		}
		view.Sites = append(view.Sites, row)
	}

	var orphans []string
	for root := range historyLocked {
		if !covered[root] {
			orphans = append(orphans, root)
		}
	}
	sort.Strings(orphans)
	for _, root := range orphans {
		view.Sites = append(view.Sites, tui.UnlockSite{
			Site:   root,
			Status: tui.UnlockFailed,
			Detail: "recorded as locked but not in the project file — run 'rehost plan' or clear it by hand",
		})
		failed = append(failed, root)
	}
	return view, failed, nil
}

// unlockOne resolves one site's outcome. ok is false when the site was locked
// and could not be cleared; a non-nil error is a transport failure.
func unlockOne(ctx context.Context, h recipe.Host, s project.Site, historyLocked bool) (tui.UnlockSite, bool, error) {
	row := tui.UnlockSite{Site: s.Root, Framework: s.Framework}
	m := recipe.MaintainerFor(s.Framework)
	if m == nil {
		if !historyLocked {
			row.Status = tui.UnlockNotLocked
			return row, true, nil
		}
		row.Status = tui.UnlockFailed
		row.Detail = "unknown framework " + s.Framework + " — cannot clear maintenance mode"
		return row, false, nil
	}

	inst := detect.Install{Framework: s.Framework, Root: s.Root, Version: s.Version}
	st, err := m.MaintenanceStatus(ctx, h, inst)
	if err != nil {
		return row, false, fmt.Errorf("probing maintenance mode on %s: %w", s.Root, err)
	}
	if st != recipe.MaintenanceOn && (st != recipe.MaintenanceUnknown || !historyLocked) {
		row.Status = tui.UnlockNotLocked
		return row, true, nil
	}

	res, err := m.DisableMaintenance(ctx, h, inst)
	if errors.Is(err, recipe.ErrMaintenanceTool) {
		row.Status = tui.UnlockFailed
		row.Detail = err.Error()
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("disabling maintenance mode on %s: %w", s.Root, err)
	}
	if !res.Supported {
		row.Status = tui.UnlockFailed
		row.Detail = res.Note
		return row, false, nil
	}
	row.Status = tui.UnlockCleared
	row.Method = res.Method
	return row, true, nil
}
