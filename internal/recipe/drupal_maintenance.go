package recipe

import (
	"context"
	"strings"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// Drupal maintenance mode is driven through drush. Without a working drush the
// only alternative is writing the flag straight into the database (the
// key_value store on D8+, the variable table on D7), which needs the site's DB
// credentials plumbed through here — deferred, so setMaintenance returns a
// typed unsupported outcome the caller surfaces as a warning rather than
// silently skipping.

// EnableMaintenance turns on Drupal maintenance mode via drush.
func (d Drupal) EnableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	return d.setMaintenance(ctx, h, in, true)
}

// DisableMaintenance turns off Drupal maintenance mode via drush. drush setting
// the flag to 0 is inherently idempotent — safe when already off.
func (d Drupal) DisableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	return d.setMaintenance(ctx, h, in, false)
}

func (Drupal) setMaintenance(ctx context.Context, h db.Host, in detect.Install, on bool) (MaintenanceResult, error) {
	if h.Run == nil || !h.HasTool("drush") {
		return drupalUnsupported(on), nil
	}
	for _, legacy := range drushOrder(in.Version) {
		ok, note, err := drushSetMaintenance(ctx, h.Run, in.Root, on, legacy)
		if err != nil {
			return MaintenanceResult{}, err
		}
		if ok {
			state := MaintenanceOn
			if !on {
				state = MaintenanceOff
			}
			return MaintenanceResult{State: state, Method: "drush", Supported: true, Note: note}, nil
		}
	}
	return drupalUnsupported(on), nil
}

// MaintenanceStatus asks drush for the current flag. A drush that is absent or
// cannot bootstrap yields Unknown (not Off) so the caller does not mistake "we
// could not tell" for "not locked".
func (Drupal) MaintenanceStatus(ctx context.Context, h db.Host, in detect.Install) (MaintenanceState, error) {
	if h.Run == nil || !h.HasTool("drush") {
		return MaintenanceUnknown, nil
	}
	for _, legacy := range drushOrder(in.Version) {
		state, ok, err := drushGetMaintenance(ctx, h.Run, in.Root, legacy)
		if err != nil {
			return MaintenanceUnknown, err
		}
		if ok {
			return state, nil
		}
	}
	return MaintenanceUnknown, nil
}

// drushOrder returns the drush dialects to try, in order: the modern (D8+)
// state API first, D7's variable API second — reversed when the detected core
// version is 7, so the likely dialect is tried first.
func drushOrder(version string) []bool {
	if drupalMajor(version) == "7" {
		return []bool{true, false}
	}
	return []bool{false, true}
}

func drupalMajor(version string) string {
	if i := strings.IndexByte(version, '.'); i >= 0 {
		return version[:i]
	}
	return version
}

// drushSetMaintenance flips the flag, returning whether drush exited cleanly. A
// non-zero exit (drush absent, wrong dialect, failed bootstrap) is a tool
// failure the caller falls through on; only a transport error is returned.
//
// The modern dialect runs state:set and cache:rebuild as separate commands:
// once state:set has succeeded the flag IS flipped, so a cache:rebuild failure
// must degrade to a note — chaining them would misreport the site's real state
// and send the caller on to the wrong dialect.
func drushSetMaintenance(ctx context.Context, r db.Runner, root string, on, legacy bool) (ok bool, note string, err error) {
	value := "0"
	if on {
		value = "1"
	}
	if legacy {
		cmd := "cd " + ssh.ShellQuote(root) + " && drush vset --exact maintenance_mode " + value + " 2>/dev/null"
		res, err := r.Run(ctx, cmd)
		if err != nil {
			return false, "", err
		}
		return res.ExitCode == 0, "", nil
	}
	set := "cd " + ssh.ShellQuote(root) + " && drush state:set system.maintenance_mode " + value + " --input-format=integer 2>/dev/null"
	res, err := r.Run(ctx, set)
	if err != nil {
		return false, "", err
	}
	if res.ExitCode != 0 {
		return false, "", nil
	}
	rebuild := "cd " + ssh.ShellQuote(root) + " && drush cache:rebuild 2>/dev/null"
	res, err = r.Run(ctx, rebuild)
	if err != nil {
		return false, "", err
	}
	if res.ExitCode != 0 {
		note = "maintenance flag set, but drush cache:rebuild failed — the change may lag until Drupal's caches expire"
	}
	return true, note, nil
}

// drushGetMaintenance reads the flag. The bool reports whether drush answered
// (a clean exit); the caller falls through to the next dialect when it did not.
func drushGetMaintenance(ctx context.Context, r db.Runner, root string, legacy bool) (MaintenanceState, bool, error) {
	var cmd string
	if legacy {
		cmd = "cd " + ssh.ShellQuote(root) + " && drush vget maintenance_mode --format=string 2>/dev/null"
	} else {
		cmd = "cd " + ssh.ShellQuote(root) + " && drush state:get system.maintenance_mode --format=string 2>/dev/null"
	}
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return MaintenanceUnknown, false, err
	}
	if res.ExitCode != 0 {
		return MaintenanceUnknown, false, nil
	}
	if strings.TrimSpace(res.Stdout) == "1" {
		return MaintenanceOn, true, nil
	}
	return MaintenanceOff, true, nil
}

func drupalUnsupported(on bool) MaintenanceResult {
	action := "enable"
	if !on {
		action = "disable"
	}
	return MaintenanceResult{
		Supported: false,
		Note:      "cannot " + action + " Drupal maintenance mode without a working drush; the direct-database fallback is not wired yet",
	}
}
