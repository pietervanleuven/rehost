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
	"strings"
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
	// Find returns the paths, under the given roots, that end in one of the
	// relative markers (e.g. "wp-includes/version.php"). Implementations
	// should locate them in as few round trips as possible.
	Find(ctx context.Context, roots, markers []string, opts FindOptions) ([]string, error)
	// RealPath resolves symlinks in path to a canonical location so the same
	// site reached through different links resolves identically. On failure
	// it returns path unchanged (best effort), never an error for absence.
	RealPath(ctx context.Context, path string) (string, error)
}

// FindOptions bounds a Find so it stays cheap on large or deep trees.
type FindOptions struct {
	MaxDepth int      // directories to descend from each root; 0 → DefaultMaxDepth
	Prune    []string // directory names never to descend into
}

// DefaultMaxDepth covers panel layouts like
// /var/www/vhosts/<domain>/<subdomain>/httpdocs/<framework marker>.
const DefaultMaxDepth = 6

// DefaultPrune lists directories that never hold a site root but are expensive
// to walk.
var DefaultPrune = []string{"node_modules", "vendor", ".git", ".svn", "cache", ".cache", "tmp"}

// Fingerprinter is an optional Recipe capability: it declares marker paths
// that let Discover locate candidate install roots with one Find instead of
// walking the tree by hand. A recipe without markers is only evaluated at
// roots supplied explicitly (e.g. via --docroot).
type Fingerprinter interface {
	// Markers are relative paths whose presence at some directory marks a
	// candidate install root there (e.g. "core/lib/Drupal.php").
	Markers() []string
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

// Discover finds candidate install roots anywhere under the start roots by
// locating framework markers (one Find), then confirms and enriches each with
// the recipes (Scan). It handles multiple sites per account — including nested
// ones under a single vhost — in a single pass.
//
// Recipes that do not implement Fingerprinter contribute no markers, so they
// only match roots another recipe discovered or roots passed to Scan directly.
func Discover(ctx context.Context, fs FS, startRoots []string, recipes []Recipe, opts FindOptions) ([]Install, error) {
	markers := collectMarkers(recipes)
	if len(markers) == 0 {
		return nil, nil
	}
	hits, err := fs.Find(ctx, startRoots, markers, opts)
	if err != nil {
		return nil, err
	}

	// Map each marker hit back to its install root and dedupe; several markers
	// (or a marker plus a static index) can point at the same root.
	var roots []string
	seen := map[string]bool{}
	for _, hit := range hits {
		root := rootFromMarker(hit, markers)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}

	// Collapse roots that resolve to the same real directory — e.g. a
	// deploy-tool symlink (current -> releases/N) and its target both under
	// the search path — so a site is confirmed once, not once per link.
	roots, err = dedupeByRealPath(ctx, fs, roots)
	if err != nil {
		return nil, err
	}
	return Scan(ctx, fs, roots, recipes)
}

// dedupeByRealPath keeps the first root among any that share a canonical path.
// The originally-discovered path is preserved for reporting.
func dedupeByRealPath(ctx context.Context, fs FS, roots []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, r := range roots {
		real, err := fs.RealPath(ctx, r)
		if err != nil {
			return nil, err
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, r)
	}
	return out, nil
}

// collectMarkers gathers the marker paths every Fingerprinter recipe declares.
func collectMarkers(recipes []Recipe) []string {
	var markers []string
	seen := map[string]bool{}
	for _, r := range recipes {
		fp, ok := r.(Fingerprinter)
		if !ok {
			continue
		}
		for _, m := range fp.Markers() {
			if !seen[m] {
				seen[m] = true
				markers = append(markers, m)
			}
		}
	}
	return markers
}

// rootFromMarker returns the install root implied by a marker hit — the found
// path with the matching marker suffix trimmed — or "" if none matches.
func rootFromMarker(hit string, markers []string) string {
	for _, m := range markers {
		if hit == m || strings.HasSuffix(hit, "/"+m) {
			root := strings.TrimSuffix(strings.TrimSuffix(hit, m), "/")
			if root == "" {
				return "."
			}
			return root
		}
	}
	return ""
}

// WalkFind is the fallback Find implementation, driven entirely through the FS
// interface: it descends up to opts.MaxDepth directories from each root,
// pruning the configured names, and records where any marker exists. FS
// backends without an efficient native search (e.g. no remote `find`) can
// delegate to it. Unreadable directories are skipped, not fatal.
func WalkFind(ctx context.Context, fs FS, roots, markers []string, opts FindOptions) ([]string, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	prune := map[string]bool{}
	for _, name := range opts.Prune {
		prune[name] = true
	}

	var hits []string
	seen := map[string]bool{}

	var visit func(dir string, depth int) error
	visit = func(dir string, depth int) error {
		for _, m := range markers {
			ok, err := fs.Exists(ctx, path.Join(dir, m))
			if err != nil {
				return err
			}
			if ok {
				hit := path.Join(dir, m)
				if !seen[hit] {
					seen[hit] = true
					hits = append(hits, hit)
				}
			}
		}
		if depth >= maxDepth {
			return nil
		}
		names, err := fs.List(ctx, dir)
		if err != nil {
			return nil // unreadable directory: skip, don't abort the walk
		}
		for _, name := range names {
			if prune[name] {
				continue
			}
			sub := path.Join(dir, name)
			isDir, err := fs.IsDir(ctx, sub)
			if err != nil {
				return err
			}
			if isDir {
				if err := visit(sub, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, r := range roots {
		if err := visit(r, 0); err != nil {
			return nil, err
		}
	}
	return hits, nil
}
