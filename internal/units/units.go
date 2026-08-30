// Package units formats byte quantities for human-facing progress and
// report lines.
package units

import "fmt"

// HumanBytes renders a byte count as a compact human string.
//
// go-hostdb and go-transfer each carry an unexported clone of this function
// (they are standalone modules and will not depend on rehost for formatting).
// A format change here — SI units, a TiB step — has to land in all three or
// the CLI's report rows and the libraries' progress lines will disagree
// mid-run.
func HumanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
