package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newStubCmd registers a not-yet-implemented command. It fails with a
// non-zero exit so scripts cannot mistake a stub for success.
func newStubCmd(name, short string, phase int) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("'rehost %s' is not implemented yet (planned for Phase %d) — see docs/PLAN.md §6", name, phase)
		},
	}
}
