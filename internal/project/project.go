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
	"reflect"
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
	// DestDB names the pre-created destination database this site's data
	// imports into. Shared-host panels pre-create prefixed databases, so
	// rehost verifies it is reachable and never runs CREATE DATABASE
	// (PLAN.md §7). Optional: without it migrate syncs the site's files and
	// skips the database, saying so. No password field by design — the
	// destination DB password is prompted at runtime.
	DestDB *SiteDB `yaml:"dest_db,omitempty"`
}

// SiteDB locates a destination database. It carries connection facts only;
// the password is prompted at runtime, never stored.
type SiteDB struct {
	Name string `yaml:"name"`
	User string `yaml:"user,omitempty"`
	Host string `yaml:"host,omitempty"` // empty = localhost on the destination
	Port int    `yaml:"port,omitempty"`
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
	for i, s := range f.Sites {
		if s.DestDB == nil {
			continue
		}
		if s.DestDB.Name == "" {
			return fmt.Errorf("sites[%d].dest_db needs a name (the panel-created destination database)", i)
		}
		// A database name is an identifier, never a path — a separator here
		// would let migrate.yaml steer file names and remote commands.
		if strings.ContainsAny(s.DestDB.Name, "/\\") || s.DestDB.Name == "." || s.DestDB.Name == ".." {
			return fmt.Errorf("sites[%d].dest_db.name %q is not a valid database name", i, s.DestDB.Name)
		}
		if s.DestDB.Port < 0 || s.DestDB.Port > 65535 {
			return fmt.Errorf("sites[%d].dest_db.port %d is out of range", i, s.DestDB.Port)
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
// When path already holds a parseable project file, Save preserves the
// hand-written comments and key order of every section it does not change:
// it splices the current values into the existing YAML tree, swapping a value
// subtree only where the data actually differs (so a plan rerun keeps the
// user's comments on source, destination, and the header). A fresh or
// unparseable path is written from the struct under the standard header.
func (f *File) Save(path string) error {
	if err := f.Validate(); err != nil {
		return err
	}
	existing, _ := os.ReadFile(path) // absent/unreadable → fresh write below
	content, err := f.encode(existing)
	if err != nil {
		return err
	}
	return writeAtomic(path, content)
}

// encode renders the project file, preserving the comments and layout of
// existing when it is a parseable YAML mapping and emitting a fresh,
// header-prefixed file otherwise.
func (f *File) encode(existing []byte) ([]byte, error) {
	var buf bytes.Buffer
	if merged, ok := mergeExisting(existing, f); ok {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(merged); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	fmt.Fprintf(&buf, header, SchemaVersion)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mergeExisting overlays f's top-level values onto the YAML document parsed
// from existing, keeping the existing key order and every comment. It returns
// (doc, true) on success, or (nil, false) when existing is not a parseable
// mapping (the caller then writes a fresh file).
func mergeExisting(existing []byte, f *File) (*yaml.Node, bool) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return nil, false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(existing, &doc); err != nil {
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false
	}
	var fresh yaml.Node
	if err := fresh.Encode(f); err != nil || fresh.Kind != yaml.MappingNode {
		return nil, false
	}
	root.Content = mergeMapping(root.Content, fresh.Content)
	return &doc, true
}

// mergeMapping overlays the fresh key/value pairs onto the existing ones (both
// the flat [k0,v0,k1,v1,…] Content of a MappingNode). Existing key order is
// kept; a matching key keeps its comment-bearing key node and its value subtree
// unless the data changed, in which case the value is swapped for fresh's;
// fresh-only keys are appended and existing-only keys are dropped, so the
// result tracks the struct while retaining comments on untouched sections.
func mergeMapping(existing, fresh []*yaml.Node) []*yaml.Node {
	freshVal := make(map[string]*yaml.Node, len(fresh)/2)
	for i := 0; i+1 < len(fresh); i += 2 {
		freshVal[fresh[i].Value] = fresh[i+1]
	}
	out := make([]*yaml.Node, 0, len(existing))
	seen := make(map[string]bool, len(fresh)/2)
	for i := 0; i+1 < len(existing); i += 2 {
		k, v := existing[i], existing[i+1]
		fv, ok := freshVal[k.Value]
		if !ok {
			continue // key no longer in the struct → drop
		}
		seen[k.Value] = true
		if sameData(v, fv) {
			out = append(out, k, v)
		} else {
			out = append(out, k, fv)
		}
	}
	for i := 0; i+1 < len(fresh); i += 2 {
		if k := fresh[i]; !seen[k.Value] {
			out = append(out, k, fresh[i+1])
		}
	}
	return out
}

// sameData reports whether two YAML nodes decode to the same data, ignoring
// comments and formatting — the test for "did this section actually change".
func sameData(a, b *yaml.Node) bool {
	var av, bv any
	if a.Decode(&av) != nil || b.Decode(&bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// writeAtomic writes content to path via a same-directory 0600 temp file and a
// rename, so a crash never leaves a half-written project file.
func writeAtomic(path string, content []byte) error {
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
	if _, err := tmp.Write(content); err != nil {
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
# sites: is written by 'rehost plan'; per-site extras you may add by hand:
#   dest_root: /home/user/public_html   # where files land (default: same
#                                       # home-relative path on the destination)
#   dest_db:                            # panel-created destination database;
#     name: u12345_wp                   # migrate imports into it (password is
#     user: u12345_wp                   # prompted at runtime, never stored)
`
}
