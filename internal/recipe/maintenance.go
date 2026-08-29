package recipe

import (
	"context"
	"errors"
	"fmt"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ErrMaintenanceTool marks a maintenance failure that is local to one site —
// an unwritable docroot, a failed removal — as opposed to a transport failure.
// Callers unwrap it (errors.Is) to keep going with other sites instead of
// aborting the whole run.
var ErrMaintenanceTool = errors.New("maintenance tool failure")

// MaintenanceState is whether a site is currently serving a maintenance page.
type MaintenanceState int

const (
	MaintenanceUnknown MaintenanceState = iota
	MaintenanceOff
	MaintenanceOn
)

// MaintenanceResult reports the outcome of an enable/disable attempt. A nil
// error from a Maintainer method means the attempt was made and this describes
// it; a non-nil error means the attempt could not be completed (transport
// failure, or the last-resort layer failed) and the result is meaningless.
type MaintenanceResult struct {
	// State is the site's maintenance state after a successful action.
	State MaintenanceState
	// Method names the layer that acted: "wp-cli", "file", "drush", or "noop".
	Method string
	// Supported is false when no strategy applies — e.g. Drupal with no working
	// drush. Note then explains why; the caller surfaces it as a warning.
	Supported bool
	Note      string
}

// Maintainer is the maintenance-mode capability a recipe may implement, the
// same pluggable-strategy shape as db.Extractor. Enable and Disable layer a
// framework CLI over a file fallback like credential extraction does: a
// transport error aborts the chain, a tool failure falls through. Every Enable
// pairs with a Disable that is safe to call when maintenance is already off.
type Maintainer interface {
	EnableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error)
	DisableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error)
	MaintenanceStatus(ctx context.Context, h db.Host, in detect.Install) (MaintenanceState, error)
}

// MaintainerFor returns the maintenance strategy of a framework's recipe, or
// nil when the framework is unknown.
func MaintainerFor(framework string) Maintainer {
	for _, r := range All() {
		if r.Name() != framework {
			continue
		}
		if m, ok := r.(Maintainer); ok {
			return m
		}
	}
	return nil
}

// remoteExists reports whether path exists on the host via `test -e`. A
// transport error propagates; a non-zero exit means absent.
func remoteExists(ctx context.Context, r db.Runner, path string) (bool, error) {
	res, err := r.Run(ctx, "test -e "+ssh.ShellQuote(path))
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// maintHeredoc delimits the file content fed to the remote write. The content
// is fixed PHP that cannot contain this marker.
const maintHeredoc = "REHOST_MAINT"

// writeRemoteFile writes content to path via a quoted heredoc so the payload
// passes through the shell literally. A transport error propagates; a non-zero
// exit (an unwritable docroot) is returned as an error — there is no next
// layer once the file write itself fails.
func writeRemoteFile(ctx context.Context, r db.Runner, path, content string) error {
	cmd := "cat > " + ssh.ShellQuote(path) + " <<'" + maintHeredoc + "'\n" + content + "\n" + maintHeredoc
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: writing %s: %s", ErrMaintenanceTool, path, ssh.FirstLine(res.Stderr))
	}
	return nil
}

// removeRemoteFile deletes path with `rm -f`, which exits cleanly whether or
// not the file exists — the idempotence a safe disable relies on.
func removeRemoteFile(ctx context.Context, r db.Runner, path string) error {
	res, err := r.Run(ctx, "rm -f "+ssh.ShellQuote(path))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: removing %s: %s", ErrMaintenanceTool, path, ssh.FirstLine(res.Stderr))
	}
	return nil
}
