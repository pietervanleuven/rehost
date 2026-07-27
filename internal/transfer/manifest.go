package transfer

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/placeholder/rehost/internal/ssh"
)

// FileEntry is one file of a manifest. Size/MTime are zero in a degraded
// (paths-only) manifest.
type FileEntry struct {
	Path  string `json:"path"` // relative to the root, POSIX separators
	Size  int64  `json:"size,omitempty"`
	MTime int64  `json:"mtime,omitempty"` // unix seconds
}

// Manifest is the convergence bookkeeping of one site: what files existed
// with what size/mtime at one point in time. Diffing two manifests yields
// the delta a rerun must transfer — the incremental story on hosts without
// rsync, and the resume story after an interrupt.
type Manifest struct {
	Root     string      `json:"root"`
	TakenAt  time.Time   `json:"taken_at"`
	Complete bool        `json:"complete"` // false = paths only (no GNU find)
	Files    []FileEntry `json:"files"`
}

// TotalBytes sums the file sizes (0 for a degraded manifest).
func (m *Manifest) TotalBytes() int64 {
	var total int64
	for _, f := range m.Files {
		total += f.Size
	}
	return total
}

// runner is the slice of ssh.Client manifests need.
type runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// TakeManifest lists every file under root (honoring excludes) with size and
// mtime via GNU find's -printf; hosts without it degrade to a paths-only
// listing. Unreadable subdirectories are skipped by find, not fatal.
func TakeManifest(ctx context.Context, r runner, root string, excludes []string) (*Manifest, error) {
	m := &Manifest{Root: root, TakenAt: time.Now().UTC()}

	res, err := r.Run(ctx, findCmd(root, excludes, true))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(res.Stdout) != "" {
		m.Files = parsePrintfListing(res.Stdout)
		m.Complete = true
	} else {
		// BSD/busybox find without -printf: fall back to bare paths.
		res, err = r.Run(ctx, findCmd(root, excludes, false))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(res.Stdout) == "" && res.ExitCode != 0 {
			return nil, fmt.Errorf("find failed on %s: %s", root, firstLine(res.Stderr))
		}
		m.Files = parsePathListing(res.Stdout, root)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

// findCmd builds the listing command. Excluded directories are pruned so
// find never descends into them.
func findCmd(root string, excludes []string, printf bool) string {
	var b strings.Builder
	b.WriteString("find " + ssh.ShellQuote(root))
	if len(excludes) > 0 {
		b.WriteString(` \(`)
		for i, e := range excludes {
			if i > 0 {
				b.WriteString(" -o")
			}
			b.WriteString(" -path " + ssh.ShellQuote(path.Join(root, e)))
		}
		b.WriteString(` \) -prune -o`)
	}
	if printf {
		b.WriteString(` -type f -printf '%s %T@ %P\n' 2>/dev/null`)
	} else {
		b.WriteString(" -type f -print 2>/dev/null")
	}
	return b.String()
}

// parsePrintfListing reads "<size> <mtime.frac> <relative path>" lines.
// Unparseable lines (e.g. filenames containing newlines) are skipped.
func parsePrintfListing(stdout string) []FileEntry {
	var files []FileEntry
	for _, line := range strings.Split(stdout, "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		mtimeF, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		files = append(files, FileEntry{Path: parts[2], Size: size, MTime: int64(mtimeF)})
	}
	return files
}

// parsePathListing reads bare absolute paths into root-relative entries.
func parsePathListing(stdout, root string) []FileEntry {
	prefix := strings.TrimSuffix(root, "/") + "/"
	var files []FileEntry
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, FileEntry{Path: strings.TrimPrefix(line, prefix)})
	}
	return files
}

// Delta is what changed between two manifests.
type Delta struct {
	Added     []FileEntry
	Changed   []FileEntry // size or mtime differ; empty when either side is degraded
	Removed   []string
	Unchanged int
}

// Total returns how many files a rerun would need to transfer.
func (d *Delta) Total() int { return len(d.Added) + len(d.Changed) }

// Diff compares a previous manifest with the current one. When either side
// is paths-only, change detection degrades to presence (added/removed).
func Diff(prev, cur *Manifest) *Delta {
	metadata := prev.Complete && cur.Complete
	prevByPath := make(map[string]FileEntry, len(prev.Files))
	for _, f := range prev.Files {
		prevByPath[f.Path] = f
	}
	d := &Delta{}
	for _, f := range cur.Files {
		old, existed := prevByPath[f.Path]
		delete(prevByPath, f.Path)
		switch {
		case !existed:
			d.Added = append(d.Added, f)
		case metadata && (old.Size != f.Size || old.MTime != f.MTime):
			d.Changed = append(d.Changed, f)
		default:
			d.Unchanged++
		}
	}
	for p := range prevByPath {
		d.Removed = append(d.Removed, p)
	}
	sort.Strings(d.Removed)
	return d
}

// ManifestFilename derives a stable, readable local filename for a root.
func ManifestFilename(root string) string {
	sum := sha256.Sum256([]byte(root))
	base := filepath.Base(strings.TrimSuffix(root, "/"))
	if base == "" || base == "." || base == "/" {
		base = "site"
	}
	return base + "-" + hex.EncodeToString(sum[:4]) + ".json.gz"
}

// SaveManifest writes a manifest as gzipped JSON (0600 — it maps out the
// site) with an atomic rename so an interrupt never corrupts the previous
// one: the old manifest stays valid until the new one is complete.
func SaveManifest(m *Manifest, filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".manifest-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	gz := gzip.NewWriter(tmp)
	if err := json.NewEncoder(gz).Encode(m); err != nil {
		tmp.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filePath)
}

// LoadManifest reads a saved manifest; (nil, nil) when none exists yet.
func LoadManifest(filePath string) (*Manifest, error) {
	f, err := os.Open(filePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	defer gz.Close()
	var m Manifest
	if err := json.NewDecoder(gz).Decode(&m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}
	return &m, nil
}
