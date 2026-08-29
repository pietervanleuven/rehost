package recipe

import (
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

const joomlaConfig = `<?php
class JConfig {
	public $offline = false;
	public $offline_message = "Down for maintenance.";
	public $dbtype = 'mysqli';
	public $host = 'localhost';
	public $user = 'joomla_u';
	public $password = "it's j";
	public $db = 'joomla_db';
	public $dbprefix = 'j4x_';
	public $secret = 'KEEPME';
}
`

func TestJoomlaDetectModern(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/libraries/src/Version.php": "class Version {\n  public const MAJOR_VERSION = 4;\n  public const MINOR_VERSION = 4;\n  public const PATCH_VERSION = 8;\n}",
		"public_html/configuration.php":         joomlaConfig,
	})
	got := detectAt(t, Joomla{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected Joomla detection")
	}
	if got.Version != "4.4.8" {
		t.Errorf("Version = %q, want 4.4.8", got.Version)
	}
	if got.ConfigFile != "public_html/configuration.php" {
		t.Errorf("ConfigFile = %q", got.ConfigFile)
	}
	if got.Extra["db_driver"] != "mysqli" || got.Extra["table_prefix"] != "j4x_" {
		t.Errorf("Extra = %v", got.Extra)
	}
}

func TestJoomlaDetectLegacy(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"www/libraries/cms/version/version.php": "class JVersion {\n  const RELEASE = '3.10';\n  const DEV_LEVEL = '12';\n}",
	})
	got := detectAt(t, Joomla{}, fs, "www")
	if got == nil || got.Version != "3.10.12" {
		t.Fatalf("legacy Joomla = %+v, want version 3.10.12", got)
	}
	if got.ConfigFile != "" {
		t.Errorf("uninstalled site should have no config file, got %q", got.ConfigFile)
	}
}

func TestParseJoomlaConfig(t *testing.T) {
	creds := parseJoomlaConfig([]byte(joomlaConfig))
	if creds == nil {
		t.Fatal("no credentials parsed")
	}
	if creds.Name != "joomla_db" || creds.User != "joomla_u" || creds.Password != "it's j" ||
		creds.Host != "localhost" || creds.TablePrefix != "j4x_" || creds.Driver != "mysqli" {
		t.Errorf("creds = %+v", creds)
	}
	if db.NormalizeDriver(creds.Driver) != db.DriverMySQL {
		t.Errorf("mysqli should normalize to mysql")
	}
}

func TestRewriteJoomlaConfig(t *testing.T) {
	out, missing, err := rewriteJoomlaConfig([]byte(joomlaConfig), db.Credentials{
		Name: "new_db", User: "new_u", Password: "new_p", Host: "db.internal", Port: 3307,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	for _, want := range []string{
		`public $db = 'new_db';`,
		`public $user = 'new_u';`,
		`public $password = 'new_p';`,
		`public $host = 'db.internal:3307';`,
		`public $secret = 'KEEPME';`,
		`public $offline = false;`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestReplaceJoomlaPropertyOfflineForms(t *testing.T) {
	for _, form := range []string{
		"public $offline = false;",
		"public $offline = '0';",
		"public $offline = 0;",
		`public $offline = "0";`,
	} {
		out, ok := replaceJoomlaProperty([]byte("<?php class JConfig {\n"+form+"\n}"), "offline", "1")
		if !ok || !strings.Contains(string(out), "$offline = '1';") {
			t.Errorf("form %q not toggled: ok=%v out=%s", form, ok, out)
		}
	}
	// A commented-out property must not win.
	in := []byte("<?php class JConfig {\n// public $offline = false;\npublic $offline = '0';\n}")
	out, ok := replaceJoomlaProperty(in, "offline", "1")
	if !ok || !strings.Contains(string(out), "// public $offline = false;") || !strings.Contains(string(out), "$offline = '1';") {
		t.Errorf("commented property shadowed the live one:\n%s", out)
	}
}

func TestJoomlaRequirementsByDriver(t *testing.T) {
	mysql := detectAt(t, Joomla{}, treeFS(t, map[string]string{
		"a/libraries/src/Version.php": "const MAJOR_VERSION = 5;",
		"a/configuration.php":         "<?php class JConfig { public $dbtype = 'mysqli'; public $db = 'd'; }",
	}), "a")
	req := RequirementsFor(*mysql)
	if req.MinPHP != "8.1" || len(req.RequiredExt) != 1 || req.RequiredExt[0] != "mysqli" || !req.NeedsDB {
		t.Errorf("mysql joomla requirements = %+v", req)
	}

	pg := detectAt(t, Joomla{}, treeFS(t, map[string]string{
		"b/libraries/src/Version.php": "const MAJOR_VERSION = 4;",
		"b/configuration.php":         "<?php class JConfig { public $dbtype = 'pgsql'; public $db = 'd'; }",
	}), "b")
	req = RequirementsFor(*pg)
	if req.MinPHP != "7.2" || len(req.RequiredExt) != 1 || req.RequiredExt[0] != "pgsql" {
		t.Errorf("pg joomla requirements = %+v", req)
	}
}

// treeFS is tree without the root path, for one-line fixtures.
func treeFS(t *testing.T, files map[string]string) detect.FS {
	t.Helper()
	fs, _ := tree(t, files)
	return fs
}
