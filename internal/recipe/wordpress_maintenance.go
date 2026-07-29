package recipe

import (
	"context"
	"path"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// wpMaintContent is the drop-in WordPress reads to serve its maintenance page;
// wp-cli's maintenance-mode command writes the same .maintenance file, so the
// file layer and the CLI layer drive one mechanism.
const wpMaintContent = "<?php $upgrading = time(); ?>"

// wpMaintFile is the sentinel WordPress and wp-cli both use, in the docroot.
func wpMaintFile(root string) string { return path.Join(root, ".maintenance") }

// EnableMaintenance turns on WordPress maintenance mode: wp-cli first, falling
// back to writing .maintenance directly. A crashed WP core upgrade leaves the
// same file, so a site already showing the maintenance page stays consistent.
func (WordPress) EnableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	if h.Run == nil {
		return MaintenanceResult{Supported: false, Note: "no command runner to reach the source"}, nil
	}
	if h.HasTool("wp") {
		ok, err := wpMaintCLI(ctx, h.Run, in.Root, "activate")
		if err != nil {
			return MaintenanceResult{}, err
		}
		if ok {
			return MaintenanceResult{State: MaintenanceOn, Method: "wp-cli", Supported: true}, nil
		}
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
