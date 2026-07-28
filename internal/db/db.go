// Package db holds database concerns: source credential extraction now,
// inspection and dump/import in later phases.
//
// Credentials live in memory for the duration of a run only — they are never
// written to migrate.yaml or any report. The Password field is excluded from
// JSON serialization as a hard guard.
package db

import (
	"context"

	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// Credentials is one site's database connection info as configured on the
// source host.
type Credentials struct {
	Driver      string `json:"driver,omitempty"` // mysql/mariadb unless a recipe says otherwise
	Host        string `json:"host,omitempty"`   // may include a port or socket suffix as configured
	Port        int    `json:"port,omitempty"`
	Name        string `json:"name"`
	User        string `json:"user,omitempty"`
	Password    string `json:"-"` // memory only: never serialized, never printed
	TablePrefix string `json:"table_prefix,omitempty"`
	// Method records which extraction layer succeeded ("wp-cli", "drush",
	// "php", "config-parse") so reports can say how trustworthy the data is.
	Method string `json:"method,omitempty"`
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
