package recipe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// localRunner executes commands through a local shell, validating that the
// exact command lines the extractors build (quoting included) work against a
// real php binary. Skipped where php is not installed.
type localRunner struct{}

func (localRunner) Run(ctx context.Context, cmd string) (remote.Result, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).Output()
	res := remote.Result{Stdout: string(out)}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
		res.Stderr = string(exitErr.Stderr)
		return res, nil
	}
	return res, err
}

func requirePHP(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("php not installed")
	}
}

// phpOnly makes the extractors skip the CLI layer and land on the PHP helper.
var phpOnly = &remote.Capabilities{Tools: map[string]remote.Tool{"php": {Name: "php", Found: true}}}

func TestWPPHPHelperWithRealPHP(t *testing.T) {
	requirePHP(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "wp-config.php")
	// Dynamic values and the wp-settings require: exactly what defeats the
	// regex layer and what the PHP helper exists for.
	content := `<?php
define( 'DB_NAME', 'wp_' . 'dyn' );
define( 'DB_USER', getenv('NOPE') ?: 'wpuser' );
define( 'DB_PASSWORD', 'p@ss' );
define( 'DB_HOST', 'localhost' );
$table_prefix = 'wp_';
require_once ABSPATH . 'wp-settings.php';
`
	if err := os.WriteFile(config, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	install := detect.Install{Framework: "wordpress", Root: dir, ConfigFile: config}
	creds, err := WordPress{}.ExtractCredentials(context.Background(), Host{Run: localRunner{}, Caps: phpOnly}, install)
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || creds.Method != "php" {
		t.Fatalf("expected php-layer credentials, got %+v", creds)
	}
	if creds.Name != "wp_dyn" || creds.User != "wpuser" || creds.Password != "p@ss" || creds.TablePrefix != "wp_" {
		t.Errorf("unexpected values: %+v", creds)
	}
}

func TestDrupalPHPHelperWithRealPHP(t *testing.T) {
	requirePHP(t)
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, "sites", "default")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(settingsDir, "settings.php")
	content := `<?php
$databases['default']['default'] = array(
  'database' => 'drupaldb',
  'username' => 'druser',
  'password' => "it's quoted",
  'prefix' => '',
  'host' => 'localhost',
  'port' => '',
  'driver' => 'mysql',
);
`
	if err := os.WriteFile(config, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	install := detect.Install{Framework: "drupal", Root: dir, ConfigFile: config}
	creds, err := Drupal{}.ExtractCredentials(context.Background(), Host{Run: localRunner{}, Caps: phpOnly}, install)
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || creds.Method != "php" {
		t.Fatalf("expected php-layer credentials, got %+v", creds)
	}
	if creds.Name != "drupaldb" || creds.User != "druser" || creds.Password != "it's quoted" || creds.Driver != "mysql" {
		t.Errorf("unexpected values: %+v", creds)
	}
}
