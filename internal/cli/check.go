package cli

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func newCheckCmd(opts *options) *cobra.Command {
	var docroots []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Compatibility gate between source and destination, rerunnable until green",
		Long: `check connects to both hosts and verifies the destination can actually run
what the source hosts: PHP version and extensions per detected framework,
database tooling, a workable file-transfer strategy, and disk space.

Blockers mean migration cannot work and must be fixed; warnings mean it can
proceed with caveats. check changes nothing on either host — rerun it until
it is green. The exit status is non-zero while blockers remain.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, opts, docroots)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	return cmd
}

func runCheck(cmd *cobra.Command, opts *options, docroots []string) error {
	u := newUI(cmd, opts)

	f, err := project.Load(opts.projectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no project file at %s — run 'rehost init' first", opts.projectFile)
	}
	if err != nil {
		return err
	}
	if f.Destination == nil {
		return fmt.Errorf("%s has no destination — rerun 'rehost init' or add a destination section", opts.projectFile)
	}

	h, err := gatherHosts(cmd.Context(), u, f, docroots)
	if err != nil {
		if u.mode == tui.ModeJSON {
			u.renderer.Error(err) // keep stdout machine-readable
		}
		return err
	}
	defer h.close()

	results := check.Run(h.input)
	if err := u.renderer.CheckReport(results); err != nil {
		return err
	}
	if blockers, _ := check.Summarize(results); blockers > 0 {
		return fmt.Errorf("check found %d blocker(s) — fix them and rerun 'rehost check'", blockers)
	}
	return nil
}
