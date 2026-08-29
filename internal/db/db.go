// Package db holds database concerns: source credential extraction now,
// inspection and dump/import in later phases.
//
// Credentials live in memory for the duration of a run only — they are never
// written to migrate.yaml or any report. The Password field is excluded from
// JSON serialization as a hard guard.
package db

import (
	"context"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// Normalized driver families. MySQL and MariaDB share one wire protocol and
// one toolchain, so they are a single driver here; telling the two servers
// apart (for the engine-compatibility advice) happens at inspection time.
const (
	DriverMySQL    = "mysql"
	DriverPostgres = "pgsql"
)

// NormalizeDriver maps the driver spellings framework configs use (mysqli,
// pdo_mysql, pdomysql, mariadb; pgsql, postgres, postgresql, pdo_pgsql) to
// the two families rehost migrates. Empty and unknown values normalize to
// mysql — the overwhelming shared-hosting default.
func NormalizeDriver(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "pgsql", "postgres", "postgresql", "pdo_pgsql", "pdopgsql":
		return DriverPostgres
	default:
		return DriverMySQL
	}
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

// Runner executes commands on the source host; *ssh.Client satisfies it.
type Runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// Host bundles what extraction may use on the source. Caps may be nil, in
// which case layers try their tool and let the failure speak.
type Host struct {
	Run  Runner
	FS   detect.FS
	Caps *ssh.Capabilities
}

// HasTool reports whether a tool is known to exist on the host; with no
// capability info it optimistically returns true so the layer still tries.
func (h Host) HasTool(name string) bool {
	if h.Caps == nil {
		return true
	}
	return h.Caps.Has(name)
}

// Extractor is the credential-extraction capability a recipe may implement
// alongside detection. (nil, nil) means "not found" — an honest absence;
// an error is a transport failure, never absence.
type Extractor interface {
	ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*Credentials, error)
}
