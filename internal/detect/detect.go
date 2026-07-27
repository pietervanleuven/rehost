// Package detect discovers what a host is running: it walks candidate
// docroots over a filesystem abstraction and asks framework recipes to
// fingerprint each one.
//
// The filesystem is read over shell commands rather than SFTP so detection
// works on restricted/jailed shells (SFTP-only mode is a later phase). The
// same interface is satisfied by a local directory, which is what recipe
// tests run against.
package detect

import (
	"context"
	"path" // remote paths are always POSIX, even when the client runs on Windows
	"sort"
)

// FS is the read-only view of a host used for detection. A non-existent path
// is reported as (false, nil); only transport-level failures return an error,
// so a dropped connection never masquerades as "framework not found".
type FS interface {
	Exists(ctx context.Context, path string) (bool, error)
	IsDir(ctx context.Context, path string) (bool, error)
	// ReadFile returns up to a recipe-relevant prefix of the file; config
	// files are small, so a capped read is enough and bounds memory.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// List returns the base names of entries in dir (excluding . and ..).
	List(ctx context.Context, dir string) ([]string, error)
}

// Install is one detected framework installation.
type Install struct {
	Framework  string            `json:"framework"`             // recipe name: drupal, wordpress, static
	Root       string            `json:"root"`                  // docroot / install root
	Version    string            `json:"version,omitempty"`     // best-effort
	ConfigFile string            `json:"config_file,omitempty"` // wp-config.php / settings.php, for later credential extraction
	Sites      []string          `json:"sites,omitempty"`       // multisite subsites (Drupal); empty otherwise
	Extra      map[string]string `json:"extra,omitempty"`       // recipe-specific facts (e.g. table_prefix)
}

// Recipe fingerprints one framework. This is the seam every framework plugs
// into; later phases add credential extraction and migration steps as
// additional interfaces a recipe may also implement.
type Recipe interface {
	// Name is the framework identifier (drupal, wordpress, static).
	Name() string
	// Detect reports an install rooted at dir, or nil if this framework is
	// not present there. An error signals a transport failure, not absence.
	Detect(ctx context.Context, fs FS, dir string) (*Install, error)
}

// commonDocroots are the directory names shared hosts serve sites from,
// relative to the account home. The home itself is also a candidate.
var commonDocroots = []string{
	"public_html", "www", "htdocs", "httpdocs", "web", "public", "html",
}

// DocrootCandidates returns likely docroot paths for an account home, most
// conventional first. It does not touch the host — the engine probes which
// actually exist.
func DocrootCandidates(home string) []string {
	if home == "" {
		home = "."
	}
	roots := make([]string, 0, len(commonDocroots)+1)
	for _, name := range commonDocroots {
		roots = append(roots, path.Join(home, name))
	}
	roots = append(roots, home)
	return roots
}

// Scan probes each candidate root and returns every install found, at most
// one per root. Recipes are tried in the given order and the first match for
// a root wins, so callers should pass specific frameworks before generic
// fallbacks (static last). Roots that do not exist are skipped.
func Scan(ctx context.Context, fs FS, roots []string, recipes []Recipe) ([]Install, error) {
	var installs []Install
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		ok, err := fs.IsDir(ctx, root)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		seen[root] = true
		for _, r := range recipes {
			install, err := r.Detect(ctx, fs, root)
			if err != nil {
				return nil, err
			}
			if install != nil {
				installs = append(installs, *install)
				break
			}
		}
	}
	// Stable, framework-then-root order for deterministic output.
	sort.SliceStable(installs, func(i, j int) bool {
		if installs[i].Framework != installs[j].Framework {
			return installs[i].Framework < installs[j].Framework
		}
		return installs[i].Root < installs[j].Root
	})
	return installs, nil
}
