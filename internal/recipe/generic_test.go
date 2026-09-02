package recipe

import (
	"context"
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/rehost/internal/detect"
)

const genericDefineConfig = `<?php
// Old credentials, kept for reference:
// define('DB_NAME', 'ancient_db');
define('SITE_TITLE', 'Example');
define('DB_HOST', 'localhost');
define('DB_USER', 'shop_u');
define('DB_PASSWORD', 'it\'s secret');
define('DB_NAME', 'shop_db');
define('SMTP_PASSWORD', 'not-the-database');

$link = mysqli_connect(DB_HOST, DB_USER, DB_PASSWORD, DB_NAME);
`

func TestGenericDetectDefines(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/index.php":  "<?php require 'config.php';",
		"public_html/config.php": genericDefineConfig,
	})
	got := detectAt(t, Generic{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected a generic-php detection")
	}
	if got.Framework != "generic-php" || got.ConfigFile != "public_html/config.php" {
		t.Errorf("install = %+v", got)
	}
	if got.Extra["db_api"] != "mysqli" {
		t.Errorf("db_api = %q, want mysqli", got.Extra["db_api"])
	}

	req := RequirementsFor(*got)
	if !req.NeedsDB || len(req.RequiredExt) != 1 || req.RequiredExt[0] != "mysqli" {
		t.Errorf("requirements = %+v", req)
	}
	if req.MinPHP == "" {
		t.Error("a PHP site must declare a PHP floor, else the check gate skips PHP entirely")
	}
}

func TestGenericIgnoresCommentedAndUnrelatedKeys(t *testing.T) {
	creds := parseGenericConfig([]byte(genericDefineConfig)).creds
	if creds.Name != "shop_db" {
		t.Errorf("Name = %q — a commented-out define must never win", creds.Name)
	}
	if creds.User != "shop_u" || creds.Password != "it's secret" || creds.Host != "localhost" {
		t.Errorf("creds = %+v", creds)
	}
}

func TestGenericNeedsBothEntryAndConfig(t *testing.T) {
	// A config with credentials but no index.php: a library directory, not a site.
	noEntry, _ := tree(t, map[string]string{"lib/config.php": genericDefineConfig})
	if got := detectAt(t, Generic{}, noEntry, "lib"); got != nil {
		t.Errorf("no entry document must not detect: %+v", got)
	}

	// index.php with a config holding no database settings at all.
	noDB, _ := tree(t, map[string]string{
		"site/index.php":  "<?php echo 'hi';",
		"site/config.php": "<?php define('TIMEZONE', 'UTC');\n$debug = 'off';",
	})
	if got := detectAt(t, Generic{}, noDB, "site"); got != nil {
		t.Errorf("no database config must not detect: %+v", got)
	}
}

func TestGenericVariableAndArrayForms(t *testing.T) {
	scalar := parseGenericConfig([]byte(`<?php
$dbhost = "127.0.0.1";
$dbuser = "u1";
$dbpass = "p1";
$dbname = "app_db";
$smtp_password = "unrelated";
`))
	if scalar == nil || scalar.creds.Name != "app_db" || scalar.creds.User != "u1" ||
		scalar.creds.Password != "p1" || scalar.creds.Host != "127.0.0.1" {
		t.Fatalf("scalar form = %+v", scalar)
	}

	// CodeIgniter-style index assignment.
	ci := parseGenericConfig([]byte(`<?php
$db['default']['hostname'] = 'localhost';
$db['default']['username'] = 'ci_u';
$db['default']['password'] = 'ci_p';
$db['default']['database'] = 'ci_db';
`))
	if ci == nil || ci.creds.Name != "ci_db" || ci.creds.User != "ci_u" || ci.creds.Password != "ci_p" {
		t.Fatalf("index-assignment form = %+v", ci)
	}

	// Arrow form inside a returned array.
	arrow := parseGenericConfig([]byte(`<?php return [
  'db_host' => 'db.internal',
  'db_name' => 'arr_db',
  'db_user' => 'arr_u',
  'db_password' => 'arr_p',
  'db_port' => '3307',
];`))
	if arrow == nil || arrow.creds.Name != "arr_db" || arrow.creds.Port != 3307 || arrow.creds.Host != "db.internal" {
		t.Fatalf("arrow form = %+v", arrow)
	}
}

func TestGenericPositionalForms(t *testing.T) {
	mysqli := parseGenericConfig([]byte(`<?php
$conn = mysqli_connect('localhost', 'pos_u', 'pos_p', 'pos_db');
`))
	if mysqli == nil || mysqli.creds.Name != "pos_db" || mysqli.creds.User != "pos_u" || mysqli.creds.Password != "pos_p" {
		t.Fatalf("mysqli_connect = %+v", mysqli)
	}
	if len(mysqli.slots) != 0 {
		t.Error("positional credentials must expose no rewrite slots — the literals carry no key")
	}

	// Legacy three-argument form plus a separate select.
	legacy := parseGenericConfig([]byte(`<?php
$c = mysql_connect('localhost', 'old_u', 'old_p');
mysql_select_db('old_db', $c);
`))
	if legacy == nil || legacy.creds.Name != "old_db" || legacy.creds.User != "old_u" {
		t.Fatalf("mysql_connect = %+v", legacy)
	}
	if legacy.api != "mysql" {
		t.Errorf("api = %q, want the removed mysql API recorded", legacy.api)
	}
	// The removed API must warn, never block: no destination has it.
	req := RequirementsFor(detect.Install{Framework: "generic-php", Extra: map[string]string{"db_api": "mysql"}})
	if len(req.RequiredExt) != 0 || len(req.RecommendedExt) != 1 || req.RecommendedExt[0] != "mysqli" {
		t.Errorf("removed-API requirements = %+v", req)
	}

	pdo := parseGenericConfig([]byte(`<?php
$pdo = new PDO('mysql:host=db.example;port=3308;dbname=pdo_db', 'pdo_u', 'pdo_p');
`))
	if pdo == nil || pdo.creds.Name != "pdo_db" || pdo.creds.User != "pdo_u" ||
		pdo.creds.Password != "pdo_p" || pdo.creds.Host != "db.example" || pdo.creds.Port != 3308 {
		t.Fatalf("PDO DSN = %+v", pdo)
	}
	if pdo.api != "pdo_mysql" {
		t.Errorf("api = %q, want pdo_mysql", pdo.api)
	}
}

func TestGenericPostgresDSN(t *testing.T) {
	pg := parseGenericConfig([]byte(`<?php
$pdo = new PDO('pgsql:host=localhost;dbname=pg_db', 'pg_u', 'pg_p');
`))
	if pg == nil || pg.creds.Name != "pg_db" {
		t.Fatalf("pg DSN = %+v", pg)
	}
	if hostdb.NormalizeDriver(pg.creds.Driver) != hostdb.DriverPostgres {
		t.Errorf("driver = %q, want pgsql", pg.creds.Driver)
	}
	req := RequirementsFor(detect.Install{Framework: "generic-php", Extra: map[string]string{"db_api": "pgsql"}})
	if len(req.RequiredExt) != 1 || req.RequiredExt[0] != "pgsql" {
		t.Errorf("pg requirements = %+v", req)
	}
}

func TestRewriteGenericConfig(t *testing.T) {
	out, missing, err := rewriteGenericConfig([]byte(genericDefineConfig), hostdb.Credentials{
		Name: "new_db", User: "new_u", Password: `p'w\d`, Host: "db.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	for _, want := range []string{
		`define('DB_NAME', 'new_db')`,
		`define('DB_USER', 'new_u')`,
		`define('DB_PASSWORD', 'p\'w\\d')`,
		`define('DB_HOST', 'db.internal')`,
		// Untouched: an unrelated secret and the commented-out original.
		`define('SMTP_PASSWORD', 'not-the-database')`,
		`// define('DB_NAME', 'ancient_db');`,
		`define('SITE_TITLE', 'Example')`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "shop_db") || strings.Contains(s, "shop_u") {
		t.Errorf("source credentials survived the rewrite:\n%s", s)
	}

	// Re-parsing the rewritten file must yield the destination credentials —
	// the convergence property a rerun depends on.
	again := parseGenericConfig(out)
	if again == nil || again.creds.Name != "new_db" || again.creds.Password != `p'w\d` {
		t.Errorf("round trip = %+v", again)
	}
}

func TestRewriteGenericConfigRefusesPositional(t *testing.T) {
	_, _, err := rewriteGenericConfig([]byte(`<?php
$conn = mysqli_connect('localhost', 'u', 'p', 'db');
`), hostdb.Credentials{Name: "new_db"})
	if err == nil {
		t.Fatal("positional credentials must not be spliced — the literals carry no key")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Errorf("error should say why: %v", err)
	}
}

func TestGenericRewriteReportsUnwritableFields(t *testing.T) {
	// A config naming only the database: the user and password have no
	// variable to replace, so the caller must be told to set them by hand.
	out, missing, err := rewriteGenericConfig([]byte("<?php\n$dbname = 'only_name';\n"),
		hostdb.Credentials{Name: "new_db", User: "new_u", Password: "new_p"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `$dbname = 'new_db'`) {
		t.Errorf("the name should still be rewritten:\n%s", out)
	}
	if len(missing) != 2 {
		t.Errorf("missing = %v, want the user and the password reported", missing)
	}
}

func TestGenericLosesToRealFrameworks(t *testing.T) {
	// A WordPress docroot has index.php and could have a config.php beside
	// it; recipe order must still give WordPress the site.
	fs, _ := tree(t, map[string]string{
		"public_html/index.php":               "<?php require 'wp-blog-header.php';",
		"public_html/wp-includes/version.php": "<?php $wp_version = '6.5.2';",
		"public_html/wp-config.php":           "<?php\n$table_prefix = 'wp_';\ndefine('DB_NAME', 'wp_db');",
		"public_html/config.php":              genericDefineConfig,
	})
	got, err := detect.Scan(context.Background(), fs, []string{"public_html"}, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Framework != "wordpress" {
		t.Fatalf("want a single wordpress install, got %+v", got)
	}
}

func TestGenericBeatsStaticWhenBothCouldMatch(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"www/index.php":   "<?php require 'config.php';",
		"www/index.html":  "<h1>placeholder</h1>",
		"www/config.php":  genericDefineConfig,
		"www/nothing.txt": "x",
	})
	got, err := detect.Scan(context.Background(), fs, []string{"www"}, All())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Framework != "generic-php" {
		t.Fatalf("a PHP site with a database is not a static site: %+v", got)
	}
}

func TestGenericFindsConfigInIncludes(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"site/index.php":            "<?php require 'includes/config.php';",
		"site/includes/config.php":  genericDefineConfig,
		"site/includes/helpers.php": "<?php",
	})
	got := detectAt(t, Generic{}, fs, "site")
	if got == nil || got.ConfigFile != "site/includes/config.php" {
		t.Fatalf("install = %+v, want the includes/ config", got)
	}
}

func TestDiscoverFindsGenericAtDocroot(t *testing.T) {
	// The generic recipe declares no markers, so discovery can only reach it
	// through the conventional docroot candidates. This is the end-to-end
	// proof that a hand-rolled site is found at all.
	fs, _ := tree(t, map[string]string{
		"public_html/index.php":  "<?php require 'config.php';",
		"public_html/config.php": genericDefineConfig,
	})
	got, err := detect.Discover(context.Background(), fs, []string{"."}, All(),
		detect.FindOptions{Prune: detect.DefaultPrune})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Framework != "generic-php" {
		t.Fatalf("want one generic-php install, got %+v", got)
	}
	if got[0].Root != "public_html" {
		t.Errorf("root = %q, want public_html", got[0].Root)
	}
}

// countingFS counts the filesystem round trips a recipe spends.
type countingFS struct {
	detect.FS
	calls int
}

func (c *countingFS) Exists(ctx context.Context, p string) (bool, error) {
	c.calls++
	return c.FS.Exists(ctx, p)
}

func (c *countingFS) IsDir(ctx context.Context, p string) (bool, error) {
	c.calls++
	return c.FS.IsDir(ctx, p)
}

func (c *countingFS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	c.calls++
	return c.FS.ReadFile(ctx, p)
}

func (c *countingFS) List(ctx context.Context, dir string) ([]string, error) {
	c.calls++
	return c.FS.List(ctx, dir)
}

func TestGenericCostsOneRoundTripWhenNotAnAppRoot(t *testing.T) {
	// Every remote FS call is one SSH round trip and nothing is cached, so a
	// recipe that runs at every candidate root has to stay cheap where it
	// does not match — which is almost everywhere.
	base, _ := tree(t, map[string]string{
		"assets/style.css":  "body{}",
		"assets/config.php": genericDefineConfig, // credentials, but no entry document
	})
	fs := &countingFS{FS: base}
	if got := detectAt(t, Generic{}, fs, "assets"); got != nil {
		t.Fatalf("must not detect without an entry document: %+v", got)
	}
	if fs.calls != 1 {
		t.Errorf("a non-matching root cost %d round trips, want exactly 1 (the listing)", fs.calls)
	}
}

func TestGenericExtractCredentials(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"site/index.php":  "<?php",
		"site/config.php": genericDefineConfig,
	})
	inst := detect.Install{Framework: "generic-php", Root: "site", ConfigFile: "site/config.php"}
	creds, err := Generic{}.ExtractCredentials(context.Background(), Host{FS: fs}, inst)
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || creds.Name != "shop_db" || creds.Method != "config-parse" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestExcludesProtectAccountStateAtEveryFramework(t *testing.T) {
	// A site root can turn out to be the account home. Syncing .ssh would
	// hand the account's private keys to the destination; syncing .rehost
	// would overwrite the destination's own run history, which is what marks
	// a docroot as rehost-created and what unlock recovers from.
	for _, framework := range []string{"wordpress", "drupal", "joomla", "prestashop", "craft", "laravel", "generic-php", "static", "unknown"} {
		excludes := ExcludeSuggestionsFor(detect.Install{Framework: framework})
		for _, want := range []string{".ssh", ".rehost"} {
			found := false
			for _, e := range excludes {
				if e == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s excludes %v, missing %q", framework, excludes, want)
			}
		}
	}
}
