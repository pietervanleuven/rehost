// Package hostdb dumps, imports and inspects databases on restricted
// shared-hosting accounts, where the only access is a shell over SSH and
// tooling cannot be assumed: dumps are streamed and truncation-guarded, a
// PHP helper stands in where mysqldump is missing, and passwords travel via
// defaults files on stdin, FIFOs, or umask-077 pgpass staging — never argv
// or the environment.
//
// Credentials live in memory for the duration of a run only. The Password
// field is excluded from JSON serialization as a hard guard.
package hostdb

import (
	"fmt"
	"strings"
)

// StageDir is the directory name under the remote $HOME where transient
// credential material (import FIFOs, pgpass files) is staged for the seconds
// a command runs. It must be a plain directory name with no shell
// metacharacters. Tools embedding this package that already own a dotdir on
// the host can point it there.
var StageDir = ".hostdb"

// humanBytes renders a byte count as a compact human string for progress
// and error messages.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// Normalized driver families. MySQL and MariaDB share one wire protocol and
// one toolchain, so they are a single driver here; telling the two servers
// apart (for the engine-compatibility advice) happens at inspection time.
const (
	DriverMySQL    = "mysql"
	DriverPostgres = "pgsql"
)

// NormalizeDriver maps the driver spellings framework configs use (mysqli,
// pdo_mysql, pdomysql, mariadb; pgsql, postgres, postgresql, pdo_pgsql) to
// the two families this package handles. Empty and unknown values normalize to
// mysql — the overwhelming shared-hosting default.
func NormalizeDriver(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "pgsql", "postgres", "postgresql", "pdo_pgsql", "pdopgsql":
		return DriverPostgres
	default:
		return DriverMySQL
	}
}

// EngineLabel names the engine for report rows: PostgreSQL for pgsql
// credentials, MariaDB when the inspected server version says so, MySQL
// otherwise — a report must not call a PostgreSQL database "MySQL".
func EngineLabel(driver, serverVersion string) string {
	if NormalizeDriver(driver) == DriverPostgres {
		return "PostgreSQL"
	}
	if strings.Contains(serverVersion, "MariaDB") {
		return "MariaDB"
	}
	return "MySQL"
}

// ClientTools names the client binaries used to reach one database. The zero
// value means the driver's classic names; hosts that ship MariaDB without
// the mysql-named symlinks get {"mariadb", "mariadb-dump"}.
type ClientTools struct {
	Client string // mysql, mariadb, or psql
	Dump   string // mysqldump, mariadb-dump, or pg_dump
}

// ResolveClientTools picks the preferred client binaries for a driver on a
// host whose capability probe answers has(). It only expresses preference —
// whether the chosen tools actually exist is the check gate's judgment.
func ResolveClientTools(driver string, has func(string) bool) ClientTools {
	if NormalizeDriver(driver) == DriverPostgres {
		return ClientTools{Client: "psql", Dump: "pg_dump"}
	}
	t := ClientTools{Client: "mysql", Dump: "mysqldump"}
	if !has("mysql") && has("mariadb") {
		t.Client = "mariadb"
	}
	if !has("mysqldump") && has("mariadb-dump") {
		t.Dump = "mariadb-dump"
	}
	return t
}

// Credentials is one site's database connection info as configured on the
// source host.
type Credentials struct {
	Driver      string `json:"driver,omitempty"` // as configured; NormalizeDriver folds it to mysql/pgsql
	Host        string `json:"host,omitempty"`   // may include a port or socket suffix as configured
	Port        int    `json:"port,omitempty"`
	Name        string `json:"name"`
	User        string `json:"user,omitempty"`
	Password    string `json:"-"` // memory only: never serialized, never printed
	TablePrefix string `json:"table_prefix,omitempty"`
	// Method records which extraction layer succeeded ("wp-cli", "drush",
	// "php", "config-parse") so reports can say how trustworthy the data is.
	Method string `json:"method,omitempty"`
	// Charset, when learned from inspection, pins the dump connection's
	// character set so a legacy site storing UTF-8 in latin1 columns is
	// dumped as the bytes it holds instead of being transcoded.
	Charset string `json:"charset,omitempty"`
	// Tools overrides the client binaries for this database; the caller
	// resolves them once from the host's capability probe
	// (ResolveClientTools). Zero value = the driver's classic names.
	Tools ClientTools `json:"-"`
}

// client returns the client binary to invoke for these credentials.
func (c *Credentials) client() string {
	if c.Tools.Client != "" {
		return c.Tools.Client
	}
	if NormalizeDriver(c.Driver) == DriverPostgres {
		return "psql"
	}
	return "mysql"
}

// dumper returns the dump binary to invoke for these credentials.
func (c *Credentials) dumper() string {
	if c.Tools.Dump != "" {
		return c.Tools.Dump
	}
	if NormalizeDriver(c.Driver) == DriverPostgres {
		return "pg_dump"
	}
	return "mysqldump"
}
