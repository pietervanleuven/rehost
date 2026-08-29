package db

import (
	"strings"
	"testing"
)

func TestNormalizeDriver(t *testing.T) {
	for in, want := range map[string]string{
		"":           DriverMySQL,
		"mysql":      DriverMySQL,
		"mysqli":     DriverMySQL,
		"pdo_mysql":  DriverMySQL,
		"pdomysql":   DriverMySQL,
		"mariadb":    DriverMySQL,
		"MySQLi":     DriverMySQL,
		"pgsql":      DriverPostgres,
		"postgres":   DriverPostgres,
		"postgresql": DriverPostgres,
		"pdo_pgsql":  DriverPostgres,
		"Pgsql":      DriverPostgres,
	} {
		if got := NormalizeDriver(in); got != want {
			t.Errorf("NormalizeDriver(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveClientTools(t *testing.T) {
	has := func(tools ...string) func(string) bool {
		set := map[string]bool{}
		for _, tool := range tools {
			set[tool] = true
		}
		return func(name string) bool { return set[name] }
	}

	if got := ResolveClientTools("mysql", has("mysql", "mysqldump")); got.Client != "mysql" || got.Dump != "mysqldump" {
		t.Errorf("classic host = %+v", got)
	}
	// MariaDB-only naming: modern MariaDB packages ship no mysql symlinks.
	if got := ResolveClientTools("mysqli", has("mariadb", "mariadb-dump")); got.Client != "mariadb" || got.Dump != "mariadb-dump" {
		t.Errorf("mariadb-named host = %+v", got)
	}
	// Mixed: whichever name exists per tool.
	if got := ResolveClientTools("mysql", has("mysql", "mariadb-dump")); got.Client != "mysql" || got.Dump != "mariadb-dump" {
		t.Errorf("mixed host = %+v", got)
	}
	if got := ResolveClientTools("pgsql", has("psql", "pg_dump")); got.Client != "psql" || got.Dump != "pg_dump" {
		t.Errorf("pg host = %+v", got)
	}
}

// The resolved tool names reach the remote command lines.
func TestClientToolsReachCommands(t *testing.T) {
	creds := &Credentials{Name: "d", Tools: ClientTools{Client: "mariadb", Dump: "mariadb-dump"}}
	if cmd := dumpCmd(creds); !strings.HasPrefix(cmd, "mariadb-dump ") {
		t.Errorf("dumpCmd should use mariadb-dump: %s", cmd)
	}
	if got := creds.client(); got != "mariadb" {
		t.Errorf("client() = %q", got)
	}
	pg := &Credentials{Name: "d", Driver: "pgsql"}
	if pg.client() != "psql" || pg.dumper() != "pg_dump" {
		t.Errorf("pg defaults = %s/%s", pg.client(), pg.dumper())
	}
}
