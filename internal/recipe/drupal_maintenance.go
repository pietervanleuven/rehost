package recipe

import (
	"context"
	"fmt"
	"strings"

	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// Drupal maintenance mode is driven through drush when it is present. Without a
// working drush the flag is written straight into the database — the key_value
// store on D8+, the variable table on D7 — using the site's own DB credentials,
// which the recipe already extracts without drush (PHP helper → settings.php
// regex). The layering is drush → direct database → unsupported, the last only
// when the credentials are also unavailable; setMaintenance then returns a typed
// unsupported outcome the caller surfaces as a warning rather than silently
// skipping.

// EnableMaintenance turns on Drupal maintenance mode via drush.
func (d Drupal) EnableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return d.setMaintenance(ctx, h, in, true)
}

// DisableMaintenance turns off Drupal maintenance mode via drush. drush setting
// the flag to 0 is inherently idempotent — safe when already off.
func (d Drupal) DisableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return d.setMaintenance(ctx, h, in, false)
}

func (d Drupal) setMaintenance(ctx context.Context, h Host, in detect.Install, on bool) (MaintenanceResult, error) {
	if h.Run == nil {
		return drupalUnsupported(on), nil
	}
	if h.HasTool("drush") {
		for _, legacy := range drushOrder(in.Version) {
			ok, note, err := drushSetMaintenance(ctx, h.Run, in.Root, on, legacy)
			if err != nil {
				return MaintenanceResult{}, err
			}
			if ok {
				return MaintenanceResult{State: maintenanceState(on), Method: "drush", Supported: true, Note: note}, nil
			}
		}
	}
	return d.dbSetMaintenance(ctx, h, in, on)
}

// dbSetMaintenance is the drush-less fallback: it writes the maintenance flag
// straight into the database. Credentials come from the recipe's own extraction
// (drush is already known absent or broken, so the PHP-helper/settings.php
// layers do the work); without them the outcome is the unsupported result whose
// note names both missing paths. h.Run is guaranteed non-nil by the caller.
//
// The write and the cache clear run as two statements, like the drush path runs
// state:set and cache:rebuild separately: once the flag row is written the flag
// IS set, so a failed cache clear — the {prefix}cache_bootstrap table may not
// exist under a memcache/redis backend — must degrade to a note, not undo the
// success or fail the site.
func (d Drupal) dbSetMaintenance(ctx context.Context, h Host, in detect.Install, on bool) (MaintenanceResult, error) {
	creds, err := d.ExtractCredentials(ctx, h, in)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if creds == nil {
		return drupalUnsupported(on), nil
	}
	legacy := drupalMajor(in.Version) == "7"
	write, cache := drupalMaintSQL(creds.TablePrefix, on, legacy)

	res, err := db.RunSQL(ctx, h.Run, creds, write)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if !res.OK {
		return MaintenanceResult{}, fmt.Errorf("%w: writing the Drupal maintenance flag: %s", ErrMaintenanceTool, res.Reason)
	}

	note := ""
	cres, err := db.RunSQL(ctx, h.Run, creds, cache)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if !cres.OK {
		note = "maintenance flag written, but clearing Drupal's bootstrap cache failed (the cache table may be absent under an alternate cache backend) — the change may lag until Drupal's caches expire"
	}
	return MaintenanceResult{State: maintenanceState(on), Method: "db", Supported: true, Note: note}, nil
}

func maintenanceState(on bool) MaintenanceState {
	if on {
		return MaintenanceOn
	}
	return MaintenanceOff
}

// MaintenanceStatus asks drush for the current flag. A drush that is absent or
// cannot bootstrap yields Unknown (not Off) so the caller does not mistake "we
// could not tell" for "not locked".
func (d Drupal) MaintenanceStatus(ctx context.Context, h Host, in detect.Install) (MaintenanceState, error) {
	if h.Run == nil {
		return MaintenanceUnknown, nil
	}
	if h.HasTool("drush") {
		for _, legacy := range drushOrder(in.Version) {
			state, ok, err := drushGetMaintenance(ctx, h.Run, in.Root, legacy)
			if err != nil {
				return MaintenanceUnknown, err
			}
			if ok {
				return state, nil
			}
		}
	}
	return d.dbMaintenanceStatus(ctx, h, in)
}

// dbMaintenanceStatus reads the flag straight from the database when drush is
// unavailable. A database we cannot reach — no credentials, or a refused
// connection — yields Unknown, never Off, so "we could not tell" is never
// mistaken for "not locked". An absent row or a serialized 0 is Off.
func (d Drupal) dbMaintenanceStatus(ctx context.Context, h Host, in detect.Install) (MaintenanceState, error) {
	creds, err := d.ExtractCredentials(ctx, h, in)
	if err != nil {
		return MaintenanceUnknown, err
	}
	if creds == nil {
		return MaintenanceUnknown, nil
	}
	res, err := db.RunSQL(ctx, h.Run, creds, drupalStatusSQL(creds.TablePrefix, drupalMajor(in.Version) == "7"))
	if err != nil {
		return MaintenanceUnknown, err
	}
	if !res.OK {
		return MaintenanceUnknown, nil
	}
	if strings.TrimSpace(res.Stdout) == "i:1;" {
		return MaintenanceOn, nil
	}
	return MaintenanceOff, nil
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
func drushSetMaintenance(ctx context.Context, r remote.Runner, root string, on, legacy bool) (ok bool, note string, err error) {
	value := "0"
	if on {
		value = "1"
	}
	if legacy {
		cmd := "cd " + remote.ShellQuote(root) + " && drush vset --exact maintenance_mode " + value + " 2>/dev/null"
		res, err := r.Run(ctx, cmd)
		if err != nil {
			return false, "", err
		}
		return res.ExitCode == 0, "", nil
	}
	set := "cd " + remote.ShellQuote(root) + " && drush state:set system.maintenance_mode " + value + " --input-format=integer 2>/dev/null"
	res, err := r.Run(ctx, set)
	if err != nil {
		return false, "", err
	}
	if res.ExitCode != 0 {
		return false, "", nil
	}
	rebuild := "cd " + remote.ShellQuote(root) + " && drush cache:rebuild 2>/dev/null"
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
func drushGetMaintenance(ctx context.Context, r remote.Runner, root string, legacy bool) (MaintenanceState, bool, error) {
	var cmd string
	if legacy {
		cmd = "cd " + remote.ShellQuote(root) + " && drush vget maintenance_mode --format=string 2>/dev/null"
	} else {
		cmd = "cd " + remote.ShellQuote(root) + " && drush state:get system.maintenance_mode --format=string 2>/dev/null"
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
		Note:      "cannot " + action + " Drupal maintenance mode: no working drush, and the database credentials for the direct-database fallback could not be extracted",
	}
}

// Drupal stores the maintenance flag as a PHP-serialized integer: i:1; on,
// i:0; off. On D8+ it is a state entry (a row in {prefix}key_value under the
// 'state' collection), cached in {prefix}cache_bootstrap under cid 'state'. On
// D7 it is a {prefix}variable row, cached under cid 'variables'. Writing the
// flag is an INSERT ... ON DUPLICATE KEY UPDATE so enable and disable are both
// idempotent (an absent row already means off). Clearing the cache is a
// separate DELETE — best effort, since an alternate cache backend may never
// have created the table.

// drupalMaintSQL returns the flag-write and cache-clear statements for the
// dialect. The table prefix is honored on every table and defensively
// backtick-quoted; the serialized values stay exactly i:1;/i:0;.
func drupalMaintSQL(prefix string, on, legacy bool) (write, cache string) {
	value := "i:0;"
	if on {
		value = "i:1;"
	}
	if legacy {
		write = "INSERT INTO " + drupalIdent(prefix+"variable") + " (name, value) VALUES ('maintenance_mode', '" + value + "') " +
			"ON DUPLICATE KEY UPDATE value = VALUES(value);"
		cache = "DELETE FROM " + drupalIdent(prefix+"cache_bootstrap") + " WHERE cid = 'variables';"
		return write, cache
	}
	write = "INSERT INTO " + drupalIdent(prefix+"key_value") + " (collection, name, value) VALUES ('state', 'system.maintenance_mode', '" + value + "') " +
		"ON DUPLICATE KEY UPDATE value = VALUES(value);"
	cache = "DELETE FROM " + drupalIdent(prefix+"cache_bootstrap") + " WHERE cid = 'state';"
	return write, cache
}

// drupalStatusSQL returns the SELECT that reads the flag's serialized value for
// the dialect.
func drupalStatusSQL(prefix string, legacy bool) string {
	if legacy {
		return "SELECT value FROM " + drupalIdent(prefix+"variable") + " WHERE name = 'maintenance_mode';"
	}
	return "SELECT value FROM " + drupalIdent(prefix+"key_value") + " WHERE collection = 'state' AND name = 'system.maintenance_mode';"
}

// drupalIdent backtick-quotes a table identifier, doubling any backtick so a
// crafted table prefix cannot break out of the quoting. Prefixes are normally
// [a-z0-9_], but the value comes from parsed config, so quote defensively.
func drupalIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
