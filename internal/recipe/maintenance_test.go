package recipe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// wpInstall is a WordPress site rooted where wpMaintFile expects .maintenance.
var wpInstall = detect.Install{Framework: "wordpress", Root: "/home/u/public_html"}

// noTool is a capability set that reports every probed tool as missing, so a
// layer keyed on HasTool is skipped (a nil Caps would optimistically try it).
func noTool() *ssh.Capabilities { return &ssh.Capabilities{Tools: map[string]ssh.Tool{}} }

func TestWordPressEnableWritesLiveTimeFileNotWPCLI(t *testing.T) {
	// Even with wp-cli available, enable must write .maintenance directly with a
	// live time() call — wp-cli's fixed timestamp self-lifts after 10 minutes,
	// resuming the live site mid-migration and losing writes.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"wp maintenance-mode activate": {ExitCode: 0},
		"cat > ":                       {ExitCode: 0},
	}}
	caps := &ssh.Capabilities{Tools: map[string]ssh.Tool{"wp": {Found: true}}}
	res, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{Run: r, Caps: caps}, wpInstall)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "file" {
		t.Fatalf("result = %+v, want on via file", res)
	}
	var wroteLiveTime bool
	for _, c := range r.calls {
		if strings.Contains(c, "wp maintenance-mode activate") {
			t.Errorf("enable must not use wp-cli (fixed timestamp): %q", c)
		}
		if strings.Contains(c, "cat > ") && strings.Contains(c, ".maintenance") && strings.Contains(c, "$upgrading = time()") {
			wroteLiveTime = true
		}
	}
	if !wroteLiveTime {
		t.Errorf("enable must write .maintenance with a live time() call, calls: %v", r.calls)
	}
}

func TestWordPressDisableWPCLIRemovesFile(t *testing.T) {
	// wp-cli deactivate succeeds, and disable still removes .maintenance so a
	// file a crashed core upgrade left behind is cleared too.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"wp maintenance-mode deactivate": {ExitCode: 0},
		"rm -f":                          {ExitCode: 0},
	}}
	res, err := WordPress{}.DisableMaintenance(context.Background(), db.Host{Run: r}, wpInstall)
	if err != nil {
		t.Fatalf("DisableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOff || res.Method != "wp-cli" {
		t.Fatalf("result = %+v, want off via wp-cli", res)
	}
	var removed bool
	for _, c := range r.calls {
		if strings.Contains(c, "rm -f") && strings.Contains(c, ".maintenance") {
			removed = true
		}
	}
	if !removed {
		t.Errorf("wp-cli disable must also remove .maintenance, calls: %v", r.calls)
	}
}

func TestWordPressEnableWPCLIAbsentUsesFile(t *testing.T) {
	// wp-cli absent per capabilities: the CLI layer is skipped entirely.
	r := &fakeRunner{byContains: map[string]ssh.Result{"cat > ": {ExitCode: 0}}}
	res, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{Run: r, Caps: noTool()}, wpInstall)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Method != "file" || res.State != MaintenanceOn {
		t.Fatalf("result = %+v, want on via file", res)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "wp maintenance-mode") {
			t.Errorf("wp-cli command must not run when the tool is absent: %q", c)
		}
	}
}

func TestWordPressEnableUnwritableDocrootErrors(t *testing.T) {
	// The file write itself failing (non-zero exit) is a real error — there is
	// no further layer below the file.
	r := &fakeRunner{byContains: map[string]ssh.Result{"cat > ": {ExitCode: 1, Stderr: "Permission denied"}}}
	_, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{Run: r, Caps: noTool()}, wpInstall)
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("unwritable docroot should error naming the cause, got: %v", err)
	}
}

func TestWordPressTransportErrorAborts(t *testing.T) {
	boom := errors.New("connection lost")
	r := &fakeRunner{err: boom}
	if _, err := (WordPress{}).EnableMaintenance(context.Background(), db.Host{Run: r}, wpInstall); !errors.Is(err, boom) {
		t.Errorf("enable transport failure should propagate, got %v", err)
	}
	r = &fakeRunner{err: boom}
	if _, err := (WordPress{}).DisableMaintenance(context.Background(), db.Host{Run: r}, wpInstall); !errors.Is(err, boom) {
		t.Errorf("disable transport failure should propagate, got %v", err)
	}
	r = &fakeRunner{err: boom}
	if _, err := (WordPress{}).MaintenanceStatus(context.Background(), db.Host{Run: r}, wpInstall); !errors.Is(err, boom) {
		t.Errorf("status transport failure should propagate, got %v", err)
	}
}

func TestWordPressStatusFromMaintenanceFile(t *testing.T) {
	// .maintenance present → on.
	r := &fakeRunner{byContains: map[string]ssh.Result{"test -e": {ExitCode: 0}}}
	st, err := WordPress{}.MaintenanceStatus(context.Background(), db.Host{Run: r}, wpInstall)
	if err != nil || st != MaintenanceOn {
		t.Fatalf("present .maintenance → %v (err %v), want on", st, err)
	}
	if !strings.Contains(r.calls[0], ".maintenance") {
		t.Errorf("status must probe .maintenance, got %q", r.calls[0])
	}
	// Absent (non-zero exit) → off, not unknown.
	r = &fakeRunner{byContains: map[string]ssh.Result{"test -e": {ExitCode: 1}}}
	st, err = WordPress{}.MaintenanceStatus(context.Background(), db.Host{Run: r}, wpInstall)
	if err != nil || st != MaintenanceOff {
		t.Fatalf("absent .maintenance → %v (err %v), want off", st, err)
	}
	// No runner at all → unknown.
	st, err = WordPress{}.MaintenanceStatus(context.Background(), db.Host{}, wpInstall)
	if err != nil || st != MaintenanceUnknown {
		t.Fatalf("no runner → %v (err %v), want unknown", st, err)
	}
}

func TestWordPressDisableIdempotentWhenOff(t *testing.T) {
	// wp-cli absent, nothing locked: rm -f succeeds regardless, so disable is a
	// safe no-op reporting off.
	r := &fakeRunner{byContains: map[string]ssh.Result{"rm -f": {ExitCode: 0}}}
	res, err := WordPress{}.DisableMaintenance(context.Background(), db.Host{Run: r, Caps: noTool()}, wpInstall)
	if err != nil {
		t.Fatalf("DisableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOff || res.Method != "file" {
		t.Fatalf("result = %+v, want off via file", res)
	}
}

func TestWordPressNoRunnerUnsupported(t *testing.T) {
	res, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{}, wpInstall)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Supported || res.Note == "" {
		t.Errorf("no runner should be unsupported with a note, got %+v", res)
	}
}

var drupalD10 = detect.Install{Framework: "drupal", Root: "/home/u/drupal", Version: "10.3.1"}
var drupalD7 = detect.Install{Framework: "drupal", Root: "/home/u/drupal7", Version: "7.98"}

func TestDrupalEnableModernDrush(t *testing.T) {
	// D8+ uses the state API and rebuilds the cache; the legacy dialect is never
	// tried when the modern one answers.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"state:set system.maintenance_mode 1": {ExitCode: 0},
		"cache:rebuild":                       {ExitCode: 0},
	}}
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "drush" {
		t.Fatalf("result = %+v, want on via drush", res)
	}
	if len(r.calls) != 2 {
		t.Fatalf("modern enable = state:set then cache:rebuild, got %d calls: %v", len(r.calls), r.calls)
	}
	if !strings.Contains(r.calls[0], "drush state:set system.maintenance_mode 1 --input-format=integer") {
		t.Errorf("modern enable command wrong: %q", r.calls[0])
	}
	if !strings.Contains(r.calls[1], "drush cache:rebuild") {
		t.Errorf("second call should rebuild caches: %q", r.calls[1])
	}
	for _, cmd := range r.calls {
		if strings.Contains(cmd, "vset") {
			t.Errorf("modern site must not use the D7 variable dialect: %q", cmd)
		}
	}
	if res.Note != "" {
		t.Errorf("clean enable should carry no note, got %q", res.Note)
	}
}

func TestDrupalEnableCacheRebuildFails(t *testing.T) {
	// Once state:set has succeeded the flag is flipped: a cache:rebuild failure
	// degrades to a note, never to a fall-through onto the D7 dialect.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"state:set system.maintenance_mode 1": {ExitCode: 0},
		"cache:rebuild":                       {ExitCode: 1},
	}}
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "drush" {
		t.Fatalf("result = %+v, want on via drush despite failed rebuild", res)
	}
	if !strings.Contains(res.Note, "cache:rebuild failed") {
		t.Errorf("failed rebuild should surface in the note, got %q", res.Note)
	}
	for _, cmd := range r.calls {
		if strings.Contains(cmd, "vset") {
			t.Errorf("failed rebuild must not fall through to the D7 dialect: %q", cmd)
		}
	}
}

func TestDrupalDisableModernDrush(t *testing.T) {
	r := &fakeRunner{byContains: map[string]ssh.Result{"state:set system.maintenance_mode 0": {ExitCode: 0}}}
	res, err := Drupal{}.DisableMaintenance(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil {
		t.Fatalf("DisableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOff || res.Method != "drush" {
		t.Fatalf("result = %+v, want off via drush", res)
	}
	if !strings.Contains(r.calls[0], "drush state:set system.maintenance_mode 0 --input-format=integer") {
		t.Errorf("modern disable command wrong: %q", r.calls[0])
	}
}

func TestDrupalEnableLegacyDrushD7(t *testing.T) {
	// A detected D7 core tries the variable dialect first.
	r := &fakeRunner{byContains: map[string]ssh.Result{"vset --exact maintenance_mode 1": {ExitCode: 0}}}
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD7)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "drush" {
		t.Fatalf("result = %+v, want on via drush", res)
	}
	if len(r.calls) != 1 {
		t.Fatalf("D7 should try the legacy dialect first and stop, got %d calls: %v", len(r.calls), r.calls)
	}
	if !strings.Contains(r.calls[0], "drush vset --exact maintenance_mode 1") {
		t.Errorf("D7 enable command wrong: %q", r.calls[0])
	}
}

func TestDrupalDrushAbsentUnsupported(t *testing.T) {
	// No drush per capabilities: a typed unsupported outcome with an explaining
	// note, never a silent skip.
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: &fakeRunner{}, Caps: noTool()}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Supported {
		t.Fatalf("no drush must be unsupported, got %+v", res)
	}
	if !strings.Contains(res.Note, "drush") {
		t.Errorf("note should explain the missing drush, got %q", res.Note)
	}
}

func TestDrupalDrushFailingAndNoCredsUnsupported(t *testing.T) {
	// drush present but every dialect fails (default 127), and credential
	// extraction also finds nothing (no FS, every command 127): both drush
	// dialects are tried, then the DB fallback, then the outcome is unsupported —
	// not a false success — with a note naming both missing paths.
	r := &fakeRunner{}
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Supported {
		t.Fatalf("failing drush + no creds should be unsupported, got %+v", res)
	}
	if !strings.Contains(res.Note, "drush") || !strings.Contains(res.Note, "credentials") {
		t.Errorf("note should name both drush and the missing credentials, got %q", res.Note)
	}
	var drushCalls int
	for _, c := range r.calls {
		if strings.Contains(c, "drush state:set") || strings.Contains(c, "drush vset") {
			drushCalls++
		}
	}
	if drushCalls != 2 {
		t.Errorf("both drush dialects should be tried before the DB fallback, got %d: %v", drushCalls, r.calls)
	}
	// No flag write can have been attempted without credentials.
	for _, c := range r.calls {
		if strings.Contains(c, "INSERT INTO") {
			t.Errorf("no SQL write should run without credentials: %q", c)
		}
	}
}

func TestDrupalTransportErrorAborts(t *testing.T) {
	boom := errors.New("connection lost")
	r := &fakeRunner{err: boom}
	if _, err := (Drupal{}).EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD10); !errors.Is(err, boom) {
		t.Errorf("transport failure should propagate, got %v", err)
	}
}

func TestDrupalStatusViaDrush(t *testing.T) {
	// Modern state:get answering "1" is on.
	r := &fakeRunner{byContains: map[string]ssh.Result{"state:get system.maintenance_mode": {ExitCode: 0, Stdout: "1\n"}}}
	st, err := Drupal{}.MaintenanceStatus(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil || st != MaintenanceOn {
		t.Fatalf("state:get 1 → %v (err %v), want on", st, err)
	}
	// "0" is off.
	r = &fakeRunner{byContains: map[string]ssh.Result{"state:get system.maintenance_mode": {ExitCode: 0, Stdout: "0\n"}}}
	st, err = Drupal{}.MaintenanceStatus(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil || st != MaintenanceOff {
		t.Fatalf("state:get 0 → %v (err %v), want off", st, err)
	}
	// No drush: unknown, never mistaken for off.
	st, err = Drupal{}.MaintenanceStatus(context.Background(), db.Host{Run: &fakeRunner{}, Caps: noTool()}, drupalD10)
	if err != nil || st != MaintenanceUnknown {
		t.Fatalf("no drush → %v (err %v), want unknown", st, err)
	}
}

// drupalSettingsPrefixed is a settings.php the regex creds layer can read
// without drush or php, carrying a non-empty table prefix so tests can assert
// the fallback qualifies every table with it.
const drupalSettingsPrefixed = `<?php
$databases['default']['default'] = array(
  'database' => 'drupal_prod',
  'username' => 'druser',
  'password' => 'drpass',
  'prefix' => 'dr_',
  'host' => 'localhost',
  'driver' => 'mysql',
);`

// drupalDBHost builds a Host whose drush/php tools are reported absent (so the
// direct-DB fallback is the only maintenance path and creds come from the
// settings.php regex layer), with the given runner canning the mysql calls.
func drupalDBHost(t *testing.T, r *fakeRunner) (db.Host, detect.Install, detect.Install) {
	t.Helper()
	fs, _ := tree(t, map[string]string{"site/sites/default/settings.php": drupalSettingsPrefixed})
	h := db.Host{Run: r, FS: fs, Caps: noTool()}
	d10 := detect.Install{Framework: "drupal", Root: "site", ConfigFile: "site/sites/default/settings.php", Version: "10.3.1"}
	d7 := detect.Install{Framework: "drupal", Root: "site", ConfigFile: "site/sites/default/settings.php", Version: "7.98"}
	return h, d10, d7
}

func TestDrupalMaintSQL(t *testing.T) {
	// D8+ enable: state row in the prefixed key_value table, serialized i:1;,
	// idempotent via ON DUPLICATE KEY UPDATE; cache cleared under cid 'state'.
	write, cache := drupalMaintSQL("dr_", true, false)
	wantWrite := "INSERT INTO `dr_key_value` (collection, name, value) VALUES ('state', 'system.maintenance_mode', 'i:1;') ON DUPLICATE KEY UPDATE value = VALUES(value);"
	if write != wantWrite {
		t.Errorf("D8+ enable write =\n  %q\nwant\n  %q", write, wantWrite)
	}
	if cache != "DELETE FROM `dr_cache_bootstrap` WHERE cid = 'state';" {
		t.Errorf("D8+ cache clear = %q", cache)
	}
	// D8+ disable writes i:0; (never deletes the row — absent already means off).
	write, _ = drupalMaintSQL("dr_", false, false)
	if !strings.Contains(write, "'i:0;'") || !strings.Contains(write, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("D8+ disable write = %q", write)
	}
	// D7 enable: variable row, cache under cid 'variables'.
	write, cache = drupalMaintSQL("dr_", true, true)
	wantWrite = "INSERT INTO `dr_variable` (name, value) VALUES ('maintenance_mode', 'i:1;') ON DUPLICATE KEY UPDATE value = VALUES(value);"
	if write != wantWrite {
		t.Errorf("D7 enable write =\n  %q\nwant\n  %q", write, wantWrite)
	}
	if cache != "DELETE FROM `dr_cache_bootstrap` WHERE cid = 'variables';" {
		t.Errorf("D7 cache clear = %q", cache)
	}
	// Empty prefix still yields the bare table names.
	write, _ = drupalMaintSQL("", true, false)
	if !strings.Contains(write, "INTO `key_value`") {
		t.Errorf("empty-prefix write = %q", write)
	}
}

func TestDrupalStatusSQL(t *testing.T) {
	if got := drupalStatusSQL("dr_", false); got != "SELECT value FROM `dr_key_value` WHERE collection = 'state' AND name = 'system.maintenance_mode';" {
		t.Errorf("D8+ status SQL = %q", got)
	}
	if got := drupalStatusSQL("dr_", true); got != "SELECT value FROM `dr_variable` WHERE name = 'maintenance_mode';" {
		t.Errorf("D7 status SQL = %q", got)
	}
}

func TestDrupalIdentQuoting(t *testing.T) {
	// A crafted backtick in the prefix is doubled, never allowed to break out.
	if got := drupalIdent("ev`il_key_value"); got != "`ev``il_key_value`" {
		t.Errorf("drupalIdent = %q", got)
	}
}

func TestDrupalEnableViaDBFallback(t *testing.T) {
	// No drush: the flag is written straight into the DB. Success carries no note
	// and reports the "db" method. Both the write and the cache clear must name
	// the prefixed tables, and the write must be an idempotent upsert.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"INSERT INTO": {ExitCode: 0},
		"DELETE FROM": {ExitCode: 0},
	}}
	h, d10, _ := drupalDBHost(t, r)
	res, err := Drupal{}.EnableMaintenance(context.Background(), h, d10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "db" {
		t.Fatalf("result = %+v, want on via db", res)
	}
	if res.Note != "" {
		t.Errorf("clean DB enable should carry no note, got %q", res.Note)
	}
	var wroteFlag, clearedCache bool
	for _, c := range r.calls {
		if strings.Contains(c, "INSERT INTO `dr_key_value`") && strings.Contains(c, "ON DUPLICATE KEY UPDATE value = VALUES(value)") {
			wroteFlag = true
		}
		if strings.Contains(c, "DELETE FROM `dr_cache_bootstrap`") {
			clearedCache = true
		}
	}
	if !wroteFlag {
		t.Errorf("enable must upsert the prefixed key_value row, calls: %v", r.calls)
	}
	if !clearedCache {
		t.Errorf("enable must clear the prefixed bootstrap cache, calls: %v", r.calls)
	}
}

func TestDrupalDisableViaDBFallback(t *testing.T) {
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"INSERT INTO": {ExitCode: 0},
		"DELETE FROM": {ExitCode: 0},
	}}
	h, d10, _ := drupalDBHost(t, r)
	res, err := Drupal{}.DisableMaintenance(context.Background(), h, d10)
	if err != nil {
		t.Fatalf("DisableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOff || res.Method != "db" {
		t.Fatalf("result = %+v, want off via db", res)
	}
	var wroteOff bool
	for _, c := range r.calls {
		if strings.Contains(c, "INSERT INTO `dr_key_value`") && strings.Contains(c, `'i:0;'`) {
			wroteOff = true
		}
	}
	if !wroteOff {
		t.Errorf("disable must upsert the flag to i:0;, calls: %v", r.calls)
	}
}

func TestDrupalEnableD7ViaDBFallback(t *testing.T) {
	// A detected D7 core uses the variable dialect, never key_value.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"INSERT INTO": {ExitCode: 0},
		"DELETE FROM": {ExitCode: 0},
	}}
	h, _, d7 := drupalDBHost(t, r)
	res, err := Drupal{}.EnableMaintenance(context.Background(), h, d7)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Method != "db" || res.State != MaintenanceOn {
		t.Fatalf("result = %+v, want on via db", res)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "key_value") {
			t.Errorf("D7 must not touch key_value: %q", c)
		}
	}
	var wrote bool
	for _, c := range r.calls {
		if strings.Contains(c, "INSERT INTO `dr_variable`") {
			wrote = true
		}
	}
	if !wrote {
		t.Errorf("D7 enable must upsert the prefixed variable row, calls: %v", r.calls)
	}
}

func TestDrupalDBCacheDeleteFailsDegrades(t *testing.T) {
	// The flag write succeeds but the cache DELETE fails (the table is absent
	// under an alternate cache backend): the flag IS set, so the result stays a
	// success and the failure surfaces only as a note — never an error, never a
	// fall-through.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"INSERT INTO": {ExitCode: 0},
		"DELETE FROM": {ExitCode: 1, Stderr: "ERROR 1146 (42S02): Table 'db.dr_cache_bootstrap' doesn't exist"},
	}}
	h, d10, _ := drupalDBHost(t, r)
	res, err := Drupal{}.EnableMaintenance(context.Background(), h, d10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "db" {
		t.Fatalf("result = %+v, want on via db despite failed cache clear", res)
	}
	if !strings.Contains(res.Note, "cache") {
		t.Errorf("failed cache clear should surface in the note, got %q", res.Note)
	}
}

func TestDrupalDBWriteFailsErrors(t *testing.T) {
	// The flag write itself failing is a real per-site failure (a maintenance
	// tool error the caller can skip past), not a silent success.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"INSERT INTO": {ExitCode: 1, Stderr: "ERROR 1045 (28000): Access denied"},
	}}
	h, d10, _ := drupalDBHost(t, r)
	_, err := Drupal{}.EnableMaintenance(context.Background(), h, d10)
	if err == nil || !errors.Is(err, ErrMaintenanceTool) {
		t.Fatalf("failed flag write should be an ErrMaintenanceTool, got %v", err)
	}
}

func TestDrupalDrushAbsentNoCredsUnsupported(t *testing.T) {
	// drush absent AND no way to reach credentials (no FS, no php): unsupported,
	// with a note naming both the missing drush and the missing credentials.
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: &fakeRunner{}, Caps: noTool()}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Supported {
		t.Fatalf("no drush + no creds must be unsupported, got %+v", res)
	}
	if !strings.Contains(res.Note, "drush") || !strings.Contains(res.Note, "credentials") {
		t.Errorf("note should name drush and the missing credentials, got %q", res.Note)
	}
}

func TestDrupalStatusViaDBFallback(t *testing.T) {
	// i:1; → on.
	r := &fakeRunner{byContains: map[string]ssh.Result{"SELECT value FROM": {ExitCode: 0, Stdout: "i:1;\n"}}}
	h, d10, _ := drupalDBHost(t, r)
	st, err := Drupal{}.MaintenanceStatus(context.Background(), h, d10)
	if err != nil || st != MaintenanceOn {
		t.Fatalf("i:1; → %v (err %v), want on", st, err)
	}
	if !strings.Contains(r.calls[len(r.calls)-1], "`dr_key_value`") {
		t.Errorf("status must query the prefixed key_value table, got %q", r.calls[len(r.calls)-1])
	}
	// i:0; → off.
	r = &fakeRunner{byContains: map[string]ssh.Result{"SELECT value FROM": {ExitCode: 0, Stdout: "i:0;\n"}}}
	h, d10, _ = drupalDBHost(t, r)
	if st, err = (Drupal{}).MaintenanceStatus(context.Background(), h, d10); err != nil || st != MaintenanceOff {
		t.Fatalf("i:0; → %v (err %v), want off", st, err)
	}
	// Absent row (empty result) → off, not unknown.
	r = &fakeRunner{byContains: map[string]ssh.Result{"SELECT value FROM": {ExitCode: 0, Stdout: ""}}}
	h, d10, _ = drupalDBHost(t, r)
	if st, err = (Drupal{}).MaintenanceStatus(context.Background(), h, d10); err != nil || st != MaintenanceOff {
		t.Fatalf("absent row → %v (err %v), want off", st, err)
	}
	// A DB we cannot reach (mysql-level failure) → unknown, never off.
	r = &fakeRunner{byContains: map[string]ssh.Result{"SELECT value FROM": {ExitCode: 1, Stderr: "ERROR 2002: Can't connect"}}}
	h, d10, _ = drupalDBHost(t, r)
	if st, err = (Drupal{}).MaintenanceStatus(context.Background(), h, d10); err != nil || st != MaintenanceUnknown {
		t.Fatalf("unreachable DB → %v (err %v), want unknown", st, err)
	}
	// No credentials at all → unknown.
	st, err = Drupal{}.MaintenanceStatus(context.Background(), db.Host{Run: &fakeRunner{}, Caps: noTool()}, drupalD10)
	if err != nil || st != MaintenanceUnknown {
		t.Fatalf("no creds → %v (err %v), want unknown", st, err)
	}
}

func TestDrupalDrushPresentShortCircuitsNoSQL(t *testing.T) {
	// drush working must win outright: no credential extraction, no SQL.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"state:set system.maintenance_mode 1": {ExitCode: 0},
		"cache:rebuild":                       {ExitCode: 0},
	}}
	fs, _ := tree(t, map[string]string{"site/sites/default/settings.php": drupalSettingsPrefixed})
	// nil Caps → drush is optimistically present.
	h := db.Host{Run: r, FS: fs}
	in := detect.Install{Framework: "drupal", Root: "site", ConfigFile: "site/sites/default/settings.php", Version: "10.3.1"}
	res, err := Drupal{}.EnableMaintenance(context.Background(), h, in)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Method != "drush" {
		t.Fatalf("drush present must win, got %+v", res)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "INSERT INTO") || strings.Contains(c, "DELETE FROM") || strings.Contains(c, "sql-conf") {
			t.Errorf("no DB fallback work should run when drush succeeds: %q", c)
		}
	}
}

func TestStaticMaintenanceIsNoop(t *testing.T) {
	on, err := Static{}.EnableMaintenance(context.Background(), db.Host{}, detect.Install{Framework: "static"})
	if err != nil || !on.Supported || on.State != MaintenanceOff || on.Method != "noop" {
		t.Fatalf("static enable = %+v (err %v), want a noop off", on, err)
	}
	off, err := Static{}.DisableMaintenance(context.Background(), db.Host{}, detect.Install{Framework: "static"})
	if err != nil || !off.Supported || off.State != MaintenanceOff || off.Method != "noop" {
		t.Fatalf("static disable = %+v (err %v), want a noop off", off, err)
	}
	st, err := Static{}.MaintenanceStatus(context.Background(), db.Host{}, detect.Install{Framework: "static"})
	if err != nil || st != MaintenanceOff {
		t.Fatalf("static status = %v (err %v), want off", st, err)
	}
}

func TestMaintainerFor(t *testing.T) {
	for _, fw := range []string{"wordpress", "drupal", "static"} {
		if MaintainerFor(fw) == nil {
			t.Errorf("%s should expose a maintainer", fw)
		}
	}
	if MaintainerFor("unknown") != nil {
		t.Error("an unknown framework must have no maintainer")
	}
	if MaintainerFor("") != nil {
		t.Error("an empty framework must have no maintainer")
	}
}
