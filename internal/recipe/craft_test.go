package recipe

import (
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
)

const craftEnvFile = `# Craft environment
CRAFT_APP_ID=CraftCMS--x
CRAFT_ENVIRONMENT=production
CRAFT_SECURITY_KEY="KEEPME"
CRAFT_DB_DRIVER=pgsql
CRAFT_DB_SERVER=127.0.0.1
CRAFT_DB_PORT=5432
CRAFT_DB_DATABASE=craft_db
CRAFT_DB_USER=craft_u
CRAFT_DB_PASSWORD="it's \"quoted\""
CRAFT_DB_TABLE_PREFIX=
`

func TestCraftDetect(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"site/craft":         "#!/usr/bin/env php\n<?php // console bootstrap",
		"site/composer.json": `{"require": {"craftcms/cms": "^4.0"}}`,
		"site/composer.lock": `{"packages": [{"name": "craftcms/cms", "keywords": ["cms"], "version": "4.8.6"}]}`,
		"site/.env":          craftEnvFile,
		"site/web/index.php": "<?php",
	})
	got := detectAt(t, Craft{}, fs, "site")
	if got == nil {
		t.Fatal("expected Craft detection")
	}
	if got.Version != "4.8.6" {
		t.Errorf("Version = %q, want 4.8.6", got.Version)
	}
	if got.ConfigFile != "site/.env" {
		t.Errorf("ConfigFile = %q", got.ConfigFile)
	}
	if got.Extra["db_driver"] != "pgsql" {
		t.Errorf("Extra = %v", got.Extra)
	}

	// A random file named craft is not a Craft project.
	fake, _ := tree(t, map[string]string{"x/craft": "just a note"})
	if got := detectAt(t, Craft{}, fake, "x"); got != nil {
		t.Errorf("file named craft without composer.json must not detect: %+v", got)
	}
}

func TestParseCraftEnv(t *testing.T) {
	creds := parseCraftEnv([]byte(craftEnvFile))
	if creds == nil {
		t.Fatal("no credentials parsed")
	}
	if creds.Name != "craft_db" || creds.User != "craft_u" || creds.Password != `it's "quoted"` ||
		creds.Host != "127.0.0.1" || creds.Port != 5432 {
		t.Errorf("creds = %+v", creds)
	}
	if hostdb.NormalizeDriver(creds.Driver) != hostdb.DriverPostgres {
		t.Errorf("driver should normalize to pgsql: %+v", creds)
	}

	// Craft 3 plain-prefix layout.
	legacy := parseCraftEnv([]byte("DB_DRIVER=mysql\nDB_SERVER=localhost\nDB_DATABASE=c3\nDB_USER=u\nDB_PASSWORD=p\n"))
	if legacy == nil || legacy.Name != "c3" || hostdb.NormalizeDriver(legacy.Driver) != hostdb.DriverMySQL {
		t.Errorf("legacy creds = %+v", legacy)
	}

	// Single-URL form.
	url := parseCraftEnv([]byte("CRAFT_DB_URL=mysql://u:pw@db.internal:3307/urls\n"))
	if url == nil || url.Name != "urls" || url.User != "u" || url.Password != "pw" || url.Host != "db.internal" || url.Port != 3307 {
		t.Errorf("url creds = %+v", url)
	}
}

func TestRewriteCraftEnv(t *testing.T) {
	out, missing, err := rewriteCraftEnv([]byte(craftEnvFile), hostdb.Credentials{
		Name: "new_db", User: "new_u", Password: `p"w\d`, Host: "db.internal", Port: 5433,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	for _, want := range []string{
		`CRAFT_DB_DATABASE="new_db"`,
		`CRAFT_DB_USER="new_u"`,
		`CRAFT_DB_PASSWORD="p\"w\\d"`,
		`CRAFT_DB_SERVER="db.internal"`,
		`CRAFT_DB_PORT="5433"`,
		`CRAFT_SECURITY_KEY="KEEPME"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	// URL/DSN form degrades to guidance rather than a mangled splice.
	if _, _, err := rewriteCraftEnv([]byte("CRAFT_DB_URL=mysql://u:p@h/db\n"), hostdb.Credentials{Name: "x"}); err == nil {
		t.Error("URL-configured env must be a by-hand edit")
	}
}

func TestCraftRequirementsByDriver(t *testing.T) {
	inst := detectAt(t, Craft{}, treeFS(t, map[string]string{
		"s/craft":         "#!/usr/bin/env php",
		"s/composer.json": `{"require":{"craftcms/cms":"^5.0"}}`,
		"s/composer.lock": `{"packages":[{"name":"craftcms/cms","version":"5.6.0"}]}`,
		"s/.env":          "CRAFT_DB_DRIVER=pgsql\nCRAFT_DB_DATABASE=d\n",
	}), "s")
	req := RequirementsFor(*inst)
	if req.MinPHP != "8.2" || len(req.RequiredExt) != 1 || req.RequiredExt[0] != "pdo_pgsql" || !req.NeedsDB {
		t.Errorf("craft pg requirements = %+v", req)
	}
}
