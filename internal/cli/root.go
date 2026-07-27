// Package cli wires the rehost command tree.
package cli

import (
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
	verbose     bool
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
	pf.BoolVarP(&opts.verbose, "verbose", "v", false, "verbose logging")

	root.AddCommand(
		newPlanCmd(opts),
		newVersionCmd(info),
		newStubCmd("init", "Interactive wizard: credentials for both hosts, connectivity test, project file", 1),
		newStubCmd("check", "Compatibility gate between source and destination, rerunnable until green", 1),
		newStubCmd("migrate", "Execute the migration; idempotent, rerunning converges", 3),
		newStubCmd("status", "Show where you are in the flow and what is green", 3),
		newStubCmd("unlock", "Clear maintenance mode on the source after a crash", 3),
	)
	return root
}

// Execute runs the CLI and returns any command error.
func Execute(info BuildInfo) error {
	return newRootCmd(info).Execute()
}
