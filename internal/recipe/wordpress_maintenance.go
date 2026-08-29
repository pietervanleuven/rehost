package recipe

import (
	"context"
	"path"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// wpMaintContent is the drop-in WordPress reads to serve its maintenance page.
// $upgrading is a live time() call, re-evaluated every request, so the page
// never expires mid-migration the way wp-cli's fixed integer timestamp does.
const wpMaintContent = "<?php $upgrading = time(); ?>"

// wpMaintFile is the sentinel WordPress and wp-cli both use, in the docroot.
func wpMaintFile(root string) string { return path.Join(root, ".maintenance") }

// EnableMaintenance turns on WordPress maintenance mode by writing .maintenance
// directly, with a live `time()` call as its $upgrading value so the page never
// self-lifts. wp-cli's `maintenance-mode activate` is deliberately NOT used: it
// stamps a fixed integer timestamp, which WordPress core stops honoring 10
// minutes later (wp_is_maintenance_mode: time() - $upgrading >= 600) — so a
// migration longer than that window would silently resume serving the live site
// mid-dump and lose writes. Writing the same file wp-cli would is equivalent for
// WordPress, which keys only on the file's presence and $upgrading value. A
// crashed WP core upgrade leaves the same file, so a site already showing the
// maintenance page stays consistent.
func (WordPress) EnableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	if h.Run == nil {
		return MaintenanceResult{Supported: false, Note: "no command runner to reach the source"}, nil
	}
	if err := writeRemoteFile(ctx, h.Run, wpMaintFile(in.Root), wpMaintContent); err != nil {
		return MaintenanceResult{}, err
	}
	return MaintenanceResult{State: MaintenanceOn, Method: "file", Supported: true}, nil
}

// DisableMaintenance turns off WordPress maintenance mode. It is idempotent:
// removing .maintenance succeeds whether or not it exists, so it also clears a
// .maintenance a crashed WP upgrade left behind. wp-cli runs first; the file
// removal is the fallback and the always-safe cleanup.
func (WordPress) DisableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	if h.Run == nil {
		return MaintenanceResult{Supported: false, Note: "no command runner to reach the source"}, nil
	}
	if h.HasTool("wp") {
		ok, err := wpMaintCLI(ctx, h.Run, in.Root, "deactivate")
		if err != nil {
			return MaintenanceResult{}, err
		}
		if ok {
			if err := removeRemoteFile(ctx, h.Run, wpMaintFile(in.Root)); err != nil {
				return MaintenanceResult{}, err
			}
			return MaintenanceResult{State: MaintenanceOff, Method: "wp-cli", Supported: true}, nil
		}
	}
	if err := removeRemoteFile(ctx, h.Run, wpMaintFile(in.Root)); err != nil {
		return MaintenanceResult{}, err
	}
	return MaintenanceResult{State: MaintenanceOff, Method: "file", Supported: true}, nil
}

// MaintenanceStatus reports whether .maintenance is present — the file wp-cli
// and WordPress core both key on, so it is the ground truth without needing a
// working wp-cli.
func (WordPress) MaintenanceStatus(ctx context.Context, h db.Host, in detect.Install) (MaintenanceState, error) {
	if h.Run == nil {
		return MaintenanceUnknown, nil
	}
	exists, err := remoteExists(ctx, h.Run, wpMaintFile(in.Root))
	if err != nil {
		return MaintenanceUnknown, err
	}
	if exists {
		return MaintenanceOn, nil
	}
	return MaintenanceOff, nil
}

// wpMaintCLI runs `wp maintenance-mode <action>` in root, returning whether it
// exited cleanly. --skip-plugins/--skip-themes keeps site code out of the way.
func wpMaintCLI(ctx context.Context, r db.Runner, root, action string) (bool, error) {
	cmd := "cd " + ssh.ShellQuote(root) + " && wp maintenance-mode " + action + " --skip-plugins --skip-themes 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}
