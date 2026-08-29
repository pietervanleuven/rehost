package recipe

import (
	"context"
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/go-ssh/remote"
)

func TestRewriteWPConfig(t *testing.T) {
	in := []byte(`<?php
define( 'DB_NAME', 'old_db' );
define( 'DB_USER', "old_user" );
define( 'DB_PASSWORD', 'old\'pass' );
define( 'DB_HOST', 'localhost' );
define( 'AUTH_KEY', 'keep-me-byte-exact!' );
$table_prefix = 'wp_';
`)
	out, err := rewriteWPConfig(in, hostdb.Credentials{
		Name: "u1_wp", User: "u1", Password: "p'w\\d", Host: "db.internal", Port: 3307,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		`define( 'DB_NAME', 'u1_wp' );`,
		`define( 'DB_USER', 'u1' );`,
		`define( 'DB_PASSWORD', 'p\'w\\d' );`,
		`define( 'DB_HOST', 'db.internal:3307' );`,
		`define( 'AUTH_KEY', 'keep-me-byte-exact!' );`,
		`$table_prefix = 'wp_';`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRewriteWPConfigMissingDefineFails(t *testing.T) {
	if _, err := rewriteWPConfig([]byte("<?php // no defines"), hostdb.Credentials{Name: "x"}); err == nil ||
		!strings.Contains(err.Error(), "DB_NAME") {
		t.Errorf("missing define should name the key, got %v", err)
	}
}

func TestRewriteDrupalSettings(t *testing.T) {
	in := []byte(`<?php
$settings['hash_salt'] = 'PRESERVE-ME';
$settings['trusted_host_patterns'] = ['^example\.com$'];
$databases['default']['default'] = array (
  'database' => 'old_db',
  'username' => 'old_user',
  'password' => 'old_pass',
  'prefix' => '',
  'host' => 'localhost',
  'port' => 3306,
  'driver' => 'mysql',
);
`)
	out, missing, err := rewriteDrupalSettings(in, hostdb.Credentials{
		Name: "u1_dru", User: "u1", Password: "s3cr3t", Host: "127.0.0.1", Port: 3307,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("nothing should be missing, got %v", missing)
	}
	got := string(out)
	for _, want := range []string{
		`'database' => 'u1_dru',`,
		`'username' => 'u1',`,
		`'password' => 's3cr3t',`,
		`'host' => '127.0.0.1',`,
		`'port' => '3307',`,
		`'hash_salt'] = 'PRESERVE-ME';`,
		`'trusted_host_patterns'] = ['^example\.com$'];`,
		`'prefix' => '',`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRewriteDrupalSettingsNoDatabasesFails(t *testing.T) {
	if _, _, err := rewriteDrupalSettings([]byte("<?php"), hostdb.Credentials{Name: "x"}); err == nil {
		t.Error("settings.php without $databases should fail")
	}
}

// A stock settings.php keeps default.settings.php's commented @code example
// above the real block the installer appends. The rewrite must edit the real
// block, not the comment, or the migrated site connects to the source DB.
func TestRewriteDrupalSettingsIgnoresDocCommentExample(t *testing.T) {
	in := []byte(`<?php
/**
 * Example:
 * @code
 * $databases['default']['default'] = array(
 *   'database' => 'databasename',
 *   'username' => 'sqlusername',
 *   'password' => 'sqlpassword',
 *   'host' => 'localhost',
 * );
 * @endcode
 */
$databases['default']['default'] = array (
  'database' => 'old_db',
  'username' => 'old_user',
  'password' => 'old_pass',
  'host' => 'localhost',
);
`)
	out, missing, err := rewriteDrupalSettings(in, hostdb.Credentials{
		Name: "new_db", User: "new_user", Password: "new_pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("nothing should be missing, got %v", missing)
	}
	got := string(out)
	// The example block stays verbatim; the real block is rewritten.
	for _, want := range []string{
		`'database' => 'databasename',`,
		`'username' => 'sqlusername',`,
		`'database' => 'new_db',`,
		`'username' => 'new_user',`,
		`'password' => 'new_pass',`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `'database' => 'databasename'`) && !strings.Contains(got, `'database' => 'new_db'`) {
		t.Error("rewrote the commented example instead of the real block")
	}
}

// When username/password are not plain literals (getenv, included file), the
// rewrite cannot repoint them and must report them so migrate warns.
func TestRewriteDrupalSettingsReportsUnwritableCreds(t *testing.T) {
	in := []byte(`<?php
$databases['default']['default'] = array (
  'database' => 'old_db',
  'username' => getenv('DB_USER'),
  'password' => getenv('DB_PASS'),
  'host' => 'localhost',
);
`)
	_, missing, err := rewriteDrupalSettings(in, hostdb.Credentials{
		Name: "new_db", User: "new_user", Password: "new_pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"username": true, "password": true}
	if len(missing) != 2 || !want[missing[0]] || !want[missing[1]] {
		t.Errorf("missing = %v, want username and password", missing)
	}
}

// rewriteRunner scripts the destination host for RewriteConfig: cat returns
// the config, writes and drush are recorded.
type rewriteRunner struct {
	content string
	cmds    []string
	drush   int // exit code for drush cr
}

func (r *rewriteRunner) Run(_ context.Context, cmd string) (remote.Result, error) {
	r.cmds = append(r.cmds, cmd)
	switch {
	case strings.HasPrefix(cmd, "cat -- "):
		return remote.Result{Stdout: r.content}, nil
	case strings.Contains(cmd, "drush cr"):
		return remote.Result{ExitCode: r.drush, Stdout: "rebuilt"}, nil
	default:
		return remote.Result{}, nil
	}
}

func TestWordPressRewriteConfigEndToEnd(t *testing.T) {
	r := &rewriteRunner{content: "<?php\ndefine('DB_NAME','a');define('DB_USER','b');define('DB_PASSWORD','c');define('DB_HOST','d');\n"}
	h := Host{Run: r, Caps: &remote.Capabilities{}}
	res, err := WordPress{}.RewriteConfig(context.Background(), h, ConfigRewrite{
		SourceConfig: "/home/u/site/wp-config.php",
		SourceRoot:   "/home/u/site",
		DestRoot:     "/home/d/www",
		DB:           hostdb.Credentials{Name: "u1_wp", User: "u1", Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Supported || res.Path != "/home/d/www/wp-config.php" {
		t.Errorf("result = %+v", res)
	}
	joined := strings.Join(r.cmds, "\n")
	if !strings.Contains(joined, "cat > '/home/d/www/wp-config.php.rehost-tmp'") || !strings.Contains(joined, "'u1_wp'") {
		t.Errorf("rewritten config should be staged through a temp file:\n%s", joined)
	}
	if !strings.Contains(joined, "mv -f '/home/d/www/wp-config.php.rehost-tmp' '/home/d/www/wp-config.php'") {
		t.Errorf("staged config should be renamed into place:\n%s", joined)
	}
	if !strings.Contains(joined, `"$HOME"/.rehost/config-backups/`) || !strings.Contains(joined, "test -f") {
		t.Errorf("a one-time backup outside the docroot should precede the rewrite:\n%s", joined)
	}
}

func TestWordPressConfigAboveDocrootUnsupported(t *testing.T) {
	res, err := WordPress{}.RewriteConfig(context.Background(), Host{Run: &rewriteRunner{}}, ConfigRewrite{
		SourceConfig: "/home/u/wp-config.php", // one level above the docroot
		SourceRoot:   "/home/u/site",
		DestRoot:     "/home/d/www",
		DB:           hostdb.Credentials{Name: "u1_wp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Supported || !strings.Contains(res.Note, "by hand") {
		t.Errorf("config above the docroot should be unsupported with guidance: %+v", res)
	}
}

func TestDrupalRewriteConfigRunsCacheRebuild(t *testing.T) {
	r := &rewriteRunner{content: "<?php\n$databases['default']['default'] = ['database' => 'a', 'username' => 'b', 'password' => 'c', 'host' => 'd'];\n"}
	h := Host{Run: r, Caps: &remote.Capabilities{Tools: map[string]remote.Tool{"drush": {Found: true}}}}
	res, err := Drupal{}.RewriteConfig(context.Background(), h, ConfigRewrite{
		SourceConfig: "/home/u/site/sites/default/settings.php",
		SourceRoot:   "/home/u/site",
		DestRoot:     "/home/d/www",
		DB:           hostdb.Credentials{Name: "u1_dru"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Supported || res.Path != "/home/d/www/sites/default/settings.php" {
		t.Errorf("result = %+v", res)
	}
	if len(res.PostSteps) != 1 || res.PostSteps[0] != "drush cr" {
		t.Errorf("post steps = %v", res.PostSteps)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "cd '/home/d/www' && drush cr") {
		t.Errorf("drush cr should run in the destination docroot:\n%v", r.cmds)
	}
}

func TestDrupalRewriteConfigNoDrushGuides(t *testing.T) {
	r := &rewriteRunner{content: "<?php\n$databases['default']['default'] = ['database' => 'a'];\n"}
	h := Host{Run: r, Caps: &remote.Capabilities{}} // no drush
	res, err := Drupal{}.RewriteConfig(context.Background(), h, ConfigRewrite{
		SourceConfig: "/home/u/site/sites/default/settings.php",
		SourceRoot:   "/home/u/site",
		DestRoot:     "/home/d/www",
		DB:           hostdb.Credentials{Name: "u1_dru"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PostSteps) != 1 || !strings.Contains(res.PostSteps[0], "no drush") {
		t.Errorf("missing drush should leave guidance: %v", res.PostSteps)
	}
}

// The rewrite must confine itself to the default $databases connection: a
// redis block before it keeps its host and password, and the real entry gets
// the new values.
func TestRewriteDrupalSettingsSkipsUnrelatedBlocks(t *testing.T) {
	in := []byte(`<?php
$settings['redis.connection'] = [
  'host' => 'redis.internal',
  'password' => 'redis-secret',
];
$databases['default']['default'] = [
  'database' => 'old_db',
  'username' => 'old',
  'password' => 'old-pass',
  'host' => 'localhost',
];
`)
	out, missing, err := rewriteDrupalSettings(in, hostdb.Credentials{
		Name: "new_db", User: "new_user", Password: "new_pass", Host: "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	if !strings.Contains(s, "'host' => 'redis.internal'") || !strings.Contains(s, "'password' => 'redis-secret'") {
		t.Errorf("redis block was corrupted:\n%s", s)
	}
	if !strings.Contains(s, "'database' => 'new_db'") || !strings.Contains(s, "'password' => 'new_pass'") {
		t.Errorf("default connection not rewritten:\n%s", s)
	}
	if strings.Contains(s, "'old-pass'") {
		t.Errorf("old credentials survived:\n%s", s)
	}
}

// A file with config blocks but no $databases assignment degrades to the
// edit-by-hand note instead of splicing into an unrelated block.
func TestRewriteDrupalSettingsNoDatabasesErrs(t *testing.T) {
	in := []byte(`<?php
$settings['redis.connection'] = ['host' => 'r', 'password' => 'p', 'database' => 'x'];
`)
	if _, _, err := rewriteDrupalSettings(in, hostdb.Credentials{Name: "n"}); err == nil {
		t.Error("no $databases assignment should be an error")
	}
}

// A commented-out define above the live one must not receive the rewrite.
func TestRewriteWPConfigIgnoresCommentedDefines(t *testing.T) {
	in := []byte(`<?php
// define('DB_NAME', 'olddb');
define('DB_NAME', 'livedb');
define('DB_USER', 'u');
define('DB_PASSWORD', 'p');
define('DB_HOST', 'localhost');
`)
	out, err := rewriteWPConfig(in, hostdb.Credentials{Name: "u1_wp", User: "u1", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "// define('DB_NAME', 'olddb');") {
		t.Errorf("the commented define should be untouched:\n%s", s)
	}
	if !strings.Contains(s, "define('DB_NAME', 'u1_wp');") {
		t.Errorf("the live define should carry the new name:\n%s", s)
	}
}
