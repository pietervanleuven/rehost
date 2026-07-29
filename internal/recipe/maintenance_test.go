package recipe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// wpInstall is a WordPress site rooted where wpMaintFile expects .maintenance.
var wpInstall = detect.Install{Framework: "wordpress", Root: "/home/u/public_html"}

// noTool is a capability set that reports every probed tool as missing, so a
// layer keyed on HasTool is skipped (a nil Caps would optimistically try it).
func noTool() *ssh.Capabilities { return &ssh.Capabilities{Tools: map[string]ssh.Tool{}} }

func TestWordPressEnableWPCLI(t *testing.T) {
	// wp-cli present and succeeding: the CLI layer wins and nothing else runs.
	r := &fakeRunner{byContains: map[string]ssh.Result{"wp maintenance-mode activate": {ExitCode: 0}}}
	res, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{Run: r}, wpInstall)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "wp-cli" {
		t.Fatalf("result = %+v, want on via wp-cli", res)
	}
	if len(r.calls) != 1 {
		t.Errorf("wp-cli success must short-circuit, ran %d commands: %v", len(r.calls), r.calls)
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

func TestWordPressEnableWPCLIFailsFallsToFile(t *testing.T) {
	// wp-cli present but failing (default 127) falls through to writing the file.
	r := &fakeRunner{byContains: map[string]ssh.Result{"cat > ": {ExitCode: 0}}}
	res, err := WordPress{}.EnableMaintenance(context.Background(), db.Host{Run: r}, wpInstall)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "file" {
		t.Fatalf("result = %+v, want on via file fallback", res)
	}
	var wrote bool
	for _, c := range r.calls {
		if strings.Contains(c, "cat > ") && strings.Contains(c, ".maintenance") && strings.Contains(c, "$upgrading") {
			wrote = true
		}
	}
	if !wrote {
		t.Errorf("fallback must write the .maintenance drop-in, calls: %v", r.calls)
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

func TestDrupalDrushFailingUnsupported(t *testing.T) {
	// drush present but every dialect fails (default 127): both are tried, then
	// the outcome is unsupported — not a false success.
	r := &fakeRunner{}
	res, err := Drupal{}.EnableMaintenance(context.Background(), db.Host{Run: r}, drupalD10)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if res.Supported || res.Note == "" {
		t.Fatalf("failing drush should be unsupported with a note, got %+v", res)
	}
	if len(r.calls) != 2 {
		t.Errorf("both dialects should be tried before giving up, got %d: %v", len(r.calls), r.calls)
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
