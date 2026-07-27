package transfer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/ssh"
)

type manifestRunner struct {
	printfOut string
	printOut  string
	cmds      []string
}

func (m *manifestRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	m.cmds = append(m.cmds, cmd)
	if strings.Contains(cmd, "-printf") {
		return ssh.Result{Stdout: m.printfOut}, nil
	}
	return ssh.Result{Stdout: m.printOut}, nil
}

const printfListing = `512 1690000000.1234 index.php
2048 1690000100.0000 wp-content/uploads/a b.jpg
notaline
1024 1690000200.5 wp-includes/version.php
`

func TestTakeManifestGNU(t *testing.T) {
	r := &manifestRunner{printfOut: printfListing}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", []string{"wp-content/cache", ".git"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || len(m.Files) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	// Sorted by path; spaces in filenames survive.
	if m.Files[1].Path != "wp-content/uploads/a b.jpg" || m.Files[1].Size != 2048 || m.Files[1].MTime != 1690000100 {
		t.Errorf("entry = %+v", m.Files[1])
	}
	if m.TotalBytes() != 3584 {
		t.Errorf("TotalBytes = %d", m.TotalBytes())
	}
	if !strings.Contains(r.cmds[0], `\( -path '/home/u/site/wp-content/cache' -o -path '/home/u/site/.git' \) -prune`) {
		t.Errorf("excludes not pruned: %s", r.cmds[0])
	}
}

func TestTakeManifestFallback(t *testing.T) {
	r := &manifestRunner{printfOut: "", printOut: "/home/u/site/index.html\n/home/u/site/css/site.css\n"}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Complete || len(m.Files) != 2 {
		t.Fatalf("fallback manifest = %+v", m)
	}
	if m.Files[0].Path != "css/site.css" {
		t.Errorf("paths should be root-relative: %+v", m.Files[0])
	}
}

func TestDiff(t *testing.T) {
	prev := &Manifest{Complete: true, Files: []FileEntry{
		{Path: "a.txt", Size: 1, MTime: 100},
		{Path: "b.txt", Size: 2, MTime: 100},
		{Path: "gone.txt", Size: 3, MTime: 100},
	}}
	cur := &Manifest{Complete: true, Files: []FileEntry{
		{Path: "a.txt", Size: 1, MTime: 100}, // unchanged
		{Path: "b.txt", Size: 2, MTime: 999}, // touched
		{Path: "new.txt", Size: 4, MTime: 200},
	}}
	d := Diff(prev, cur)
	if len(d.Added) != 1 || d.Added[0].Path != "new.txt" ||
		len(d.Changed) != 1 || d.Changed[0].Path != "b.txt" ||
		len(d.Removed) != 1 || d.Removed[0] != "gone.txt" ||
		d.Unchanged != 1 || d.Total() != 2 {
		t.Errorf("Diff = %+v", d)
	}
}

func TestDiffDegradedIsPresenceOnly(t *testing.T) {
	prev := &Manifest{Complete: false, Files: []FileEntry{{Path: "a"}, {Path: "b"}}}
	cur := &Manifest{Complete: true, Files: []FileEntry{
		{Path: "a", Size: 9, MTime: 9},
		{Path: "c", Size: 1, MTime: 1},
	}}
	d := Diff(prev, cur)
	if len(d.Changed) != 0 || len(d.Added) != 1 || len(d.Removed) != 1 || d.Unchanged != 1 {
		t.Errorf("degraded diff = %+v", d)
	}
}

func TestManifestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFilename("/home/u/public_html"))
	m := &Manifest{Root: "/home/u/public_html", Complete: true, Files: []FileEntry{{Path: "x", Size: 5, MTime: 42}}}
	if err := SaveManifest(m, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Root != m.Root || len(loaded.Files) != 1 || loaded.Files[0] != m.Files[0] {
		t.Errorf("round trip = %+v", loaded)
	}

	if missing, err := LoadManifest(filepath.Join(dir, "nope.json.gz")); err != nil || missing != nil {
		t.Errorf("missing manifest should be (nil, nil), got %v, %v", missing, err)
	}
}

func TestManifestFilenameStable(t *testing.T) {
	a := ManifestFilename("/home/u/public_html")
	if a != ManifestFilename("/home/u/public_html") {
		t.Error("filename must be deterministic")
	}
	if a == ManifestFilename("/home/u/other") {
		t.Error("different roots must map to different files")
	}
	if !strings.HasPrefix(a, "public_html-") {
		t.Errorf("filename should stay readable: %s", a)
	}
}
