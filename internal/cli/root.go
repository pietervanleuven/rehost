// Package cli wires the rehost command tree.
package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// BuildInfo carries version metadata stamped by the build.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// options holds the global flag values shared by all commands.
type options struct {
	projectFile string
	json        bool
	noColor     bool
}

func newRootCmd(info BuildInfo) *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:   "rehost",
		Short: "Migrate a website from one shared host to another over SSH",
		Long: `rehost migrates a website (files + database) from one shared host to
another over SSH, with maximum auto-detection and a Terraform-style
plan/apply workflow.

The flow: init → check → plan → migrate → cutover report.`,
		SilenceUsage: true,
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&opts.projectFile, "project-file", "f", "migrate.yaml", "path to the project file")
	pf.BoolVar(&opts.json, "json", false, "machine-readable JSON output (implies non-interactive)")
	pf.BoolVar(&opts.noColor, "no-color", false, "disable colored output")

	root.AddCommand(
		newPlanCmd(opts),
		newInitCmd(opts),
		newCheckCmd(opts),
		newStatusCmd(opts),
		newHistoryCmd(opts),
		newVersionCmd(info),
		newMigrateCmd(opts),
		newUnlockCmd(opts),
		newCutoverCmd(opts),
	)
	return root
}

// Execute runs the CLI under ctx (cancellation reaches every command through
// cmd.Context()) and returns any command error.
func Execute(ctx context.Context, info BuildInfo) error {
	return newRootCmd(info).ExecuteContext(ctx)
}
