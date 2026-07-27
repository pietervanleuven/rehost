package recipe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/db"
	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/ssh"
)

// fakeRunner returns canned results for commands matched by substring.
type fakeRunner struct {
	byContains map[string]ssh.Result
	err        error
	calls      []string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.calls = append(f.calls, cmd)
	if f.err != nil {
		return ssh.Result{}, f.err
	}
	for substr, res := range f.byContains {
		if strings.Contains(cmd, substr) {
			return res, nil
		}
	}
	return ssh.Result{ExitCode: 127}, nil
}

const wpConfigPHP = `<?php
define( 'DB_NAME', 'wp_prod' );
define( 'DB_USER', "wpuser" );
define( 'DB_PASSWORD', 'se'.'cret' ); // dynamic — regex layer sees nothing usable
define( 'DB_HOST', 'localhost' );
$table_prefix = 'wp_';
require_once ABSPATH . 'wp-settings.php';
`

const wpConfigSimple = `<?php
define('DB_NAME', 'blogdb');
define('DB_USER', 'bloguser');
define('DB_PASSWORD', 'p4ss');
define('DB_HOST', '127.0.0.1:3307');
$table_prefix = 'wp_';
`

const drupalSettings = `<?php
$databases['default']['default'] = array(
  'database' => 'drupal_prod',
  'username' => 'druser',
  'password' => 'drpass',
  'prefix' => '',
  'host' => 'db.example.com',
  'port' => '3306',
  'driver' => 'mysql',
);
`

func TestParseWPConfigList(t *testing.T) {
	out := `[{"name":"table_prefix","value":"wp_","type":"variable"},
{"name":"DB_NAME","value":"wp_prod","type":"constant"},
{"name":"DB_USER","value":"wpuser","type":"constant"},
{"name":"DB_PASSWORD","value":"topsecret","type":"constant"},
{"name":"DB_HOST","value":"localhost","type":"constant"}]`
	creds := parseWPConfigList(out)
	if creds == nil || creds.Name != "wp_prod" || creds.User != "wpuser" ||
		creds.Password != "topsecret" || creds.Host != "localhost" || creds.TablePrefix != "wp_" {
		t.Fatalf("parseWPConfigList = %+v", creds)
	}
	if creds.Method != "wp-cli" {
		t.Errorf("method = %q, want wp-cli", creds.Method)
	}
	if parseWPConfigList("PHP Warning: nope") != nil {
		t.Error("garbage should parse to nil")
	}
}

func TestParseWPConfigRegex(t *testing.T) {
	creds := parseWPConfig([]byte(wpConfigSimple))
	if creds == nil || creds.Name != "blogdb" || creds.User != "bloguser" || creds.Password != "p4ss" {
		t.Fatalf("parseWPConfig = %+v", creds)
	}
	if creds.Host != "127.0.0.1" || creds.Port != 3307 {
		t.Errorf("host:port split = %q:%d, want 127.0.0.1:3307", creds.Host, creds.Port)
	}
	if parseWPConfig([]byte("<?php // no defines")) != nil {
		t.Error("config without DB_NAME should parse to nil")
	}
}

func TestParseDrupalSettingsRegex(t *testing.T) {
	creds := parseDrupalSettings([]byte(drupalSettings))
	if creds == nil || creds.Name != "drupal_prod" || creds.User != "druser" ||
		creds.Password != "drpass" || creds.Host != "db.example.com" || creds.Port != 3306 {
		t.Fatalf("parseDrupalSettings = %+v", creds)
	}
	if creds.Driver != "mysql" || creds.Method != "config-parse" {
		t.Errorf("driver/method = %q/%q", creds.Driver, creds.Method)
	}
}

func TestParseDrushSQLConf(t *testing.T) {
	out := `{"database":"drupal_prod","username":"druser","password":"drpass","host":"localhost","port":"","driver":"mysql","prefix":""}`
	creds := parseDrushSQLConf(out)
	if creds == nil || creds.Name != "drupal_prod" || creds.User != "druser" || creds.Method != "drush" {
		t.Fatalf("parseDrushSQLConf = %+v", creds)
	}

	// drush sometimes prints a numeric port and a per-table prefix map.
	out = `{"database":"d","username":"u","password":"p","host":"h","port":3307,"driver":"mysql","prefix":{"default":"pre_"}}`
	creds = parseDrushSQLConf(out)
	if creds == nil || creds.Port != 3307 || creds.TablePrefix != "" {
		t.Fatalf("parseDrushSQLConf with map prefix = %+v", creds)
	}
}

func TestParseSentinelCreds(t *testing.T) {
	stdout := "some stray config output\n" + credSentinel + `{"name":"wp_prod","user":"u","password":"p","host":"localhost:/var/mysql.sock","prefix":"wp_"}`
	creds := parseSentinelCreds(stdout, "php")
	if creds == nil || creds.Name != "wp_prod" || creds.Method != "php" {
		t.Fatalf("parseSentinelCreds = %+v", creds)
	}
	if creds.Host != "localhost:/var/mysql.sock" || creds.Port != 0 {
		t.Errorf("socket host must stay intact, got %q:%d", creds.Host, creds.Port)
	}
	if parseSentinelCreds("no sentinel here", "php") != nil {
		t.Error("missing sentinel should parse to nil")
	}
}

func TestWordPressLayering(t *testing.T) {
	fs, _ := tree(t, map[string]string{"site/wp-config.php": wpConfigSimple})
	install := detect.Install{Framework: "wordpress", Root: "site", ConfigFile: "site/wp-config.php"}

	// wp-cli present and working: first layer wins, nothing else runs.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"wp config list": {Stdout: `[{"name":"DB_NAME","value":"cli_db"},{"name":"DB_USER","value":"u"},{"name":"DB_PASSWORD","value":"p"},{"name":"DB_HOST","value":"localhost"},{"name":"table_prefix","value":"wp_"}]`},
	}}
	creds, err := WordPress{}.ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs}, install)
	if err != nil || creds == nil || creds.Name != "cli_db" || creds.Method != "wp-cli" {
		t.Fatalf("wp-cli layer: creds=%+v err=%v", creds, err)
	}
	if len(r.calls) != 1 {
		t.Errorf("wp-cli success must short-circuit, ran %d commands", len(r.calls))
	}

	// wp-cli and php both absent (exit 127): regex layer reads the file.
	r = &fakeRunner{}
	creds, err = WordPress{}.ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs}, install)
	if err != nil || creds == nil || creds.Name != "blogdb" || creds.Method != "config-parse" {
		t.Fatalf("regex fallback: creds=%+v err=%v", creds, err)
	}

	// Capabilities that say the tools are missing skip those layers entirely.
	caps := &ssh.Capabilities{Tools: map[string]ssh.Tool{}}
	r = &fakeRunner{}
	if _, err := (WordPress{}).ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs, Caps: caps}, install); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("with no wp/php capability nothing should run, ran %v", r.calls)
	}

	// A transport failure aborts instead of masquerading as "not found".
	r = &fakeRunner{err: errors.New("connection lost")}
	if _, err := (WordPress{}).ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs}, install); err == nil {
		t.Error("transport failure must propagate")
	}
}

func TestDrupalLayering(t *testing.T) {
	fs, _ := tree(t, map[string]string{"site/sites/default/settings.php": drupalSettings})
	install := detect.Install{Framework: "drupal", Root: "site", ConfigFile: "site/sites/default/settings.php"}

	// drush works: first layer wins.
	r := &fakeRunner{byContains: map[string]ssh.Result{
		"drush sql-conf": {Stdout: `{"database":"drush_db","username":"u","password":"p","host":"localhost","driver":"mysql","prefix":""}`},
	}}
	creds, err := Drupal{}.ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs}, install)
	if err != nil || creds == nil || creds.Name != "drush_db" || creds.Method != "drush" {
		t.Fatalf("drush layer: creds=%+v err=%v", creds, err)
	}

	// No drush, no php: regex layer.
	r = &fakeRunner{}
	creds, err = Drupal{}.ExtractCredentials(context.Background(), db.Host{Run: r, FS: fs}, install)
	if err != nil || creds == nil || creds.Name != "drupal_prod" || creds.Method != "config-parse" {
		t.Fatalf("regex fallback: creds=%+v err=%v", creds, err)
	}
}

func TestExtractorFor(t *testing.T) {
	if ExtractorFor("wordpress") == nil || ExtractorFor("drupal") == nil {
		t.Error("wordpress and drupal must have extractors")
	}
	if ExtractorFor("static") != nil || ExtractorFor("unknown") != nil {
		t.Error("static/unknown must have no extractor")
	}
}
