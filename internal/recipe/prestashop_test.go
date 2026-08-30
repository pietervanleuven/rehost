package recipe

import (
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
)

const psParameters = `<?php return array (
  'parameters' =>
  array (
    'database_host' => '127.0.0.1',
    'database_port' => '',
    'database_name' => 'ps_shop',
    'database_user' => 'ps_u',
    'database_password' => 'it\'s ps',
    'database_prefix' => 'ps_',
    'database_engine' => 'InnoDB',
    'ps_caching' => 'CacheMemcache',
    'cookie_key' => 'KEEPME',
  ),
);
`

const psSettings = `<?php
define('_DB_SERVER_', 'localhost');
define('_DB_NAME_', 'ps16_shop');
define('_DB_USER_', 'ps16_u');
define('_DB_PASSWD_', 'oldpass');
define('_DB_PREFIX_', 'ps_');
define('_PS_VERSION_', '1.6.1.24');
define('_COOKIE_KEY_', 'KEEPME');
`

func TestPrestaShopDetectModern(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"shop/config/defines.inc.php":    "<?php define('_PS_MODE_DEV_', false);",
		"shop/app/AppKernel.php":         "class AppKernel {\n  const VERSION = '8.1.4';\n}",
		"shop/app/config/parameters.php": psParameters,
		"shop/config/settings.inc.php":   "<?php // 1.7 keeps an empty shim",
	})
	got := detectAt(t, PrestaShop{}, fs, "shop")
	if got == nil {
		t.Fatal("expected PrestaShop detection")
	}
	if got.Version != "8.1.4" {
		t.Errorf("Version = %q, want 8.1.4", got.Version)
	}
	if got.ConfigFile != "shop/app/config/parameters.php" {
		t.Errorf("parameters.php should win over the empty shim, got %q", got.ConfigFile)
	}
	if got.Extra["table_prefix"] != "ps_" {
		t.Errorf("Extra = %v", got.Extra)
	}
}

func TestPrestaShopDetectLegacy(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"shop/config/defines.inc.php":  "<?php",
		"shop/config/settings.inc.php": psSettings,
	})
	got := detectAt(t, PrestaShop{}, fs, "shop")
	if got == nil || got.Version != "1.6.1.24" || got.ConfigFile != "shop/config/settings.inc.php" {
		t.Fatalf("legacy PrestaShop = %+v", got)
	}
}

func TestParsePrestaShopConfigBothShapes(t *testing.T) {
	modern := parsePrestaShopConfig([]byte(psParameters))
	if modern == nil {
		t.Fatal("no credentials from parameters.php")
	}
	if modern.Name != "ps_shop" || modern.User != "ps_u" || modern.Password != "it's ps" ||
		modern.Host != "127.0.0.1" || modern.TablePrefix != "ps_" {
		t.Errorf("modern creds = %+v", modern)
	}

	legacy := parsePrestaShopConfig([]byte(psSettings))
	if legacy == nil {
		t.Fatal("no credentials from settings.inc.php")
	}
	if legacy.Name != "ps16_shop" || legacy.User != "ps16_u" || legacy.Password != "oldpass" || legacy.Host != "localhost" {
		t.Errorf("legacy creds = %+v", legacy)
	}
}

func TestRewritePrestaShopConfigBothShapes(t *testing.T) {
	creds := hostdb.Credentials{Name: "new_db", User: "new_u", Password: "new_p", Host: "db.internal", Port: 3307}

	out, missing, err := rewritePrestaShopConfig([]byte(psParameters), creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	for _, want := range []string{
		`'database_name' => 'new_db'`,
		`'database_user' => 'new_u'`,
		`'database_password' => 'new_p'`,
		`'database_host' => 'db.internal'`,
		`'database_port' => '3307'`,
		`'cookie_key' => 'KEEPME'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("modern rewrite missing %q in:\n%s", want, s)
		}
	}

	out, missing, err = rewritePrestaShopConfig([]byte(psSettings), creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s = string(out)
	for _, want := range []string{
		`define('_DB_NAME_', 'new_db')`,
		`define('_DB_USER_', 'new_u')`,
		`define('_DB_PASSWD_', 'new_p')`,
		`define('_DB_SERVER_', 'db.internal:3307')`,
		`define('_COOKIE_KEY_', 'KEEPME')`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("legacy rewrite missing %q in:\n%s", want, s)
		}
	}
}
