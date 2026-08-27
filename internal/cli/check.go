package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/project"
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

	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	if err := requireDestination(f, opts.projectFile, "check"); err != nil {
		return err
	}

	h, err := gatherHosts(cmd.Context(), u, f, docroots)
	if err != nil {
		return u.fail(err)
	}
	defer h.close()

	// Only check feeds the dest_db-visibility rule: migrate's pre-flight
	// verifies the declared databases itself, with richer per-site rows.
	h.input.DestDBs = destDBsConfigured(f)

	results := check.Run(h.input)
	if err := u.renderer.CheckReport(results); err != nil {
		return err
	}
	if blockers, _ := check.Summarize(results); blockers > 0 {
		return fmt.Errorf("check found %d blocker(s) — fix them and rerun 'rehost check'", blockers)
	}
	u.progress("check is green — next: rehost plan --dry-run to rehearse, or rehost migrate to execute")
	return nil
}

// destDBsConfigured maps each project-file site root to whether it names a
// dest_db, feeding the check gate's visibility rule.
func destDBsConfigured(f *project.File) map[string]bool {
	m := make(map[string]bool, len(f.Sites))
	for _, s := range f.Sites {
		m[s.Root] = s.DestDB != nil
	}
	return m
}
