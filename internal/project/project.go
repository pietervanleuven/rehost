// Package project reads and writes the rehost project file (migrate.yaml).
//
// The schema deliberately has no field that could hold a secret: passwords
// are prompted at runtime, never stored.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	// Upstream go-yaml is archived but remains the de-facto standard;
	// github.com/goccy/go-yaml is the maintained alternative if we ever
	// hit a limitation.
	"gopkg.in/yaml.v3"
)

const (
	// SchemaVersion is the migrate.yaml schema this build reads and writes.
	SchemaVersion = 1
	// DefaultFilename is the Terraform-style project file in the working directory.
	DefaultFilename = "migrate.yaml"
)

// File is schema v1 of migrate.yaml.
type File struct {
	Version int    `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
	// Domain is the site's primary public domain (optional). It enables the
	// DNS snapshot in check and the cutover report; rehost never changes DNS.
	Domain      string `yaml:"domain,omitempty"`
	Source      Host   `yaml:"source"`
	Destination *Host  `yaml:"destination,omitempty"`
	// Sites is what plan detected on the source — written by plan, read by
	// later commands. Rerunning plan refreshes it.
	Sites []Site `yaml:"sites,omitempty"`
}

// Site is one detected website on the source host.
type Site struct {
	Framework string `yaml:"framework"`
	Root      string `yaml:"root"`
	Version   string `yaml:"version,omitempty"`
	// DestRoot is where this site's files land on the destination. Optional:
	// when empty, migrate rebases Root's home-relative path onto the
	// destination home. It never holds a secret — it is a filesystem path —
	// so it does not weaken the "no secrets in migrate.yaml" guarantee.
	DestRoot string `yaml:"dest_root,omitempty"`
}

// Host describes how to reach one host. Zero values defer to ~/.ssh/config
// and standard defaults at connect time.
type Host struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port,omitempty"`
	User    string `yaml:"user,omitempty"`
	Auth    string `yaml:"auth,omitempty"` // "", agent, key, password
	KeyPath string `yaml:"key_path,omitempty"`
}

var validAuth = map[string]bool{"": true, "agent": true, "key": true, "password": true}

// secretFieldNames trigger a targeted error when found as unknown YAML keys.
var secretFieldNames = []string{"password", "pass", "passwd", "secret", "token", "api_key"}

// Load reads, strictly decodes, and validates a project file.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("%s: file is empty", path)
		}
		return nil, decodeError(path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// decodeError rewrites strict-decode failures on secret-looking keys into
// guidance instead of a bare unknown-field error.
func decodeError(path string, err error) error {
	msg := err.Error()
	for _, name := range secretFieldNames {
		if strings.Contains(msg, "field "+name+" not found") {
			return fmt.Errorf("%s must not contain secrets; remove %q — rehost prompts at runtime (use auth: password)", path, name)
		}
	}
	return fmt.Errorf("%s: %w", path, err)
}

// Validate checks schema invariants beyond YAML syntax.
func (f *File) Validate() error {
	if f.Version != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d (this build supports version %d)", f.Version, SchemaVersion)
	}
	if err := f.Source.validate("source"); err != nil {
		return err
	}
	if f.Source.Host == "" {
		return errors.New("source.host is required")
	}
	if f.Destination != nil {
		if err := f.Destination.validate("destination"); err != nil {
			return err
		}
	}
	return nil
}

func (h Host) validate(section string) error {
	if strings.Contains(h.Host, "@") {
		return fmt.Errorf("%s.host must be a hostname or ~/.ssh/config alias; put the user in %s.user and never put passwords in migrate.yaml", section, section)
	}
	if !validAuth[h.Auth] {
		return fmt.Errorf("%s.auth must be one of agent, key, password (or omitted for auto), got %q", section, h.Auth)
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("%s.port %d is out of range", section, h.Port)
	}
	return nil
}

const header = `# migrate.yaml — rehost project file (schema v%d)
# NEVER put passwords or secrets in this file; rehost prompts at runtime.
`

// Save validates and atomically writes the project file with 0600 permissions.
func (f *File) Save(path string) error {
	if err := f.Validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, header, SchemaVersion)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".migrate-*.yaml.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Example returns a commented sample file for "no project file yet" guidance.
func Example() string {
	return fmt.Sprintf(header, SchemaVersion) + `version: 1
name: example-site
domain: example.com          # optional; enables DNS checks and the cutover report
source:
  host: source.example.com   # or an alias from ~/.ssh/config
  user: user
  auth: agent                # agent | key | password (omit = auto)
destination:                 # optional until 'rehost check'
  host: dest.example.com
  user: user
`
}
