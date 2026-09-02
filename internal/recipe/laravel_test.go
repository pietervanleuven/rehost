package recipe

import (
	"context"
	"strings"
	"testing"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/detect"
)

const laravelEnvFile = `APP_NAME=Example
APP_ENV=production
APP_KEY="base64:KEEPME"

DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=laravel_db
DB_USERNAME=laravel_u
DB_PASSWORD="it's \"quoted\""
`

func TestLaravelDetect(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"app/artisan":          "#!/usr/bin/env php\n<?php // console bootstrap",
		"app/composer.json":    `{"require": {"laravel/framework": "^10.0"}}`,
		"app/composer.lock":    `{"packages": [{"name": "laravel/framework", "keywords": ["framework"], "version": "v10.48.2"}]}`,
		"app/.env":             laravelEnvFile,
		"app/public/index.php": "<?php",
	})
	got := detectAt(t, Laravel{}, fs, "app")
	if got == nil {
		t.Fatal("expected Laravel detection")
	}
	if got.Version != "10.48.2" {
		t.Errorf("Version = %q, want 10.48.2", got.Version)
	}
	if got.ConfigFile != "app/.env" {
		t.Errorf("ConfigFile = %q", got.ConfigFile)
	}
	if got.Extra["db_driver"] != "mysql" {
		t.Errorf("Extra = %v", got.Extra)
	}

	// A random file named artisan is not a Laravel app.
	fake, _ := tree(t, map[string]string{"x/artisan": "just a note"})
	if got := detectAt(t, Laravel{}, fake, "x"); got != nil {
		t.Errorf("file named artisan without composer.json must not detect: %+v", got)
	}
}

func TestLaravelDetectSqliteDefault(t *testing.T) {
	// A stock Laravel 11 skeleton: .env leaves DB_CONNECTION unset and
	// config/database.php defaults it to sqlite.
	fs, _ := tree(t, map[string]string{
		"app/artisan":             "#!/usr/bin/env php",
		"app/composer.json":       `{"require": {"laravel/framework": "^11.0"}}`,
		"app/.env":                "APP_KEY=base64:x\n",
		"app/config/database.php": `<?php return ['default' => env('DB_CONNECTION', 'sqlite')];`,
	})
	got := detectAt(t, Laravel{}, fs, "app")
	if got == nil {
		t.Fatal("expected Laravel detection")
	}
	if got.Extra["db_driver"] != "sqlite" {
		t.Errorf("db_driver = %q, want the config/database.php sqlite default", got.Extra["db_driver"])
	}
	req := RequirementsFor(*got)
	if req.NeedsDB {
		t.Error("a sqlite app has no server database — NeedsDB must be false")
	}
	if len(req.RequiredExt) != 1 || req.RequiredExt[0] != "pdo_sqlite" {
		t.Errorf("RequiredExt = %v, want pdo_sqlite", req.RequiredExt)
	}
}

func TestParseLaravelEnv(t *testing.T) {
	creds := parseLaravelEnv([]byte(laravelEnvFile), "")
	if creds == nil {
		t.Fatal("no credentials parsed")
	}
	if creds.Name != "laravel_db" || creds.User != "laravel_u" || creds.Password != `it's "quoted"` ||
		creds.Host != "127.0.0.1" || creds.Port != 3306 {
		t.Errorf("creds = %+v", creds)
	}
	if hostdb.NormalizeDriver(creds.Driver) != hostdb.DriverMySQL {
		t.Errorf("driver should normalize to mysql: %+v", creds)
	}

	// A sqlite connection is a file that travels with the file sync.
	if creds := parseLaravelEnv([]byte("DB_CONNECTION=sqlite\nDB_DATABASE=/home/u/app/database.sqlite\n"), ""); creds != nil {
		t.Errorf("sqlite must yield no server credentials, got %+v", creds)
	}
	if creds := parseLaravelEnv([]byte("DB_DATABASE=ignored\n"), "sqlite"); creds != nil {
		t.Errorf("the detection-resolved sqlite fallback must apply, got %+v", creds)
	}

	// Single-URL form.
	url := parseLaravelEnv([]byte("DATABASE_URL=pgsql://u:pw@db.internal:5433/urls\n"), "")
	if url == nil || url.Name != "urls" || url.User != "u" || url.Password != "pw" || url.Host != "db.internal" || url.Port != 5433 {
		t.Errorf("url creds = %+v", url)
	}
}

func TestRewriteLaravelEnv(t *testing.T) {
	out, missing, err := rewriteLaravelEnv([]byte(laravelEnvFile), hostdb.Credentials{
		Name: "new_db", User: "new_u", Password: `p"w\d`, Host: "db.internal", Port: 3307,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v", missing)
	}
	s := string(out)
	for _, want := range []string{
		`DB_DATABASE="new_db"`,
		`DB_USERNAME="new_u"`,
		`DB_PASSWORD="p\"w\\d"`,
		`DB_HOST="db.internal"`,
		`DB_PORT="3307"`,
		`APP_KEY="base64:KEEPME"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}

	// URL/DSN form degrades to guidance rather than a mangled splice.
	if _, _, err := rewriteLaravelEnv([]byte("DB_URL=mysql://u:p@h/db\n"), hostdb.Credentials{Name: "x"}); err == nil {
		t.Error("URL-configured env must be a by-hand edit")
	}
}

func TestLaravelMaintenanceToggle(t *testing.T) {
	inst := detect.Install{Framework: "laravel", Root: "/home/u/app"}
	r := &fakeRunner{byContains: map[string]remote.Result{"php artisan": {ExitCode: 0}}}
	caps := &remote.Capabilities{Tools: map[string]remote.Tool{"php": {Found: true}}}
	res, err := Laravel{}.EnableMaintenance(context.Background(), Host{Run: r, Caps: caps}, inst)
	if err != nil {
		t.Fatalf("EnableMaintenance: %v", err)
	}
	if !res.Supported || res.State != MaintenanceOn || res.Method != "artisan" {
		t.Fatalf("result = %+v, want on via artisan", res)
	}
	var sawDown bool
	for _, c := range r.calls {
		if strings.Contains(c, "php artisan down") && strings.Contains(c, "/home/u/app") {
			sawDown = true
		}
	}
	if !sawDown {
		t.Errorf("enable must run 'php artisan down' in the project root, calls: %v", r.calls)
	}

	// No PHP CLI: unsupported with a note, never an error.
	res, err = Laravel{}.EnableMaintenance(context.Background(), Host{Run: r, Caps: noTool()}, inst)
	if err != nil || res.Supported {
		t.Fatalf("without php the toggle must degrade: res=%+v err=%v", res, err)
	}
}

func TestLaravelMaintenanceStatusReadsDownFile(t *testing.T) {
	inst := detect.Install{Framework: "laravel", Root: "/home/u/app"}
	down := &fakeRunner{byContains: map[string]remote.Result{"test -e": {ExitCode: 0}}}
	if st, err := (Laravel{}).MaintenanceStatus(context.Background(), Host{Run: down}, inst); err != nil || st != MaintenanceOn {
		t.Errorf("down marker present: state=%v err=%v, want on", st, err)
	}
	up := &fakeRunner{byContains: map[string]remote.Result{"test -e": {ExitCode: 1}}}
	if st, err := (Laravel{}).MaintenanceStatus(context.Background(), Host{Run: up}, inst); err != nil || st != MaintenanceOff {
		t.Errorf("down marker absent: state=%v err=%v, want off", st, err)
	}
}

func TestLaravelRequirementsByDriver(t *testing.T) {
	inst := detectAt(t, Laravel{}, treeFS(t, map[string]string{
		"s/artisan":       "#!/usr/bin/env php",
		"s/composer.json": `{"require":{"laravel/framework":"^11.0"}}`,
		"s/composer.lock": `{"packages":[{"name":"laravel/framework","version":"v11.9.2"}]}`,
		"s/.env":          "DB_CONNECTION=pgsql\nDB_DATABASE=d\n",
	}), "s")
	req := RequirementsFor(*inst)
	if req.MinPHP != "8.2" || len(req.RequiredExt) != 1 || req.RequiredExt[0] != "pdo_pgsql" || !req.NeedsDB {
		t.Errorf("laravel pg requirements = %+v", req)
	}
}
