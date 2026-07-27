package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPlanCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "plan [user@host[:port]]",
		Short: "Connect to the hosts and report their capabilities",
		Long: `plan connects to the source (and destination, when configured) and probes
what the hosts offer: shell type, PHP version, and availability of rsync,
mysqldump, tar, gzip, wp, drush and friends.

A target may be given directly (rehost plan user@host) or read from the
project file. The deep source scan and dry-run land in a later phase.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error {
			_ = opts
			return fmt.Errorf("'rehost plan' is not wired up yet — coming in this Phase 0 build")
		},
	}
}
