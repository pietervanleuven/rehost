package detect

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDocrootCandidates(t *testing.T) {
	got := DocrootCandidates("/home/u1")
	want := []string{
		"/home/u1/public_html", "/home/u1/www", "/home/u1/htdocs",
		"/home/u1/httpdocs", "/home/u1/web", "/home/u1/public", "/home/u1/html",
		"/home/u1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DocrootCandidates()\n got %v\nwant %v", got, want)
	}
	if c := DocrootCandidates(""); c[len(c)-1] != "." {
		t.Errorf("empty home should fall back to '.', got %v", c)
	}
}

// markerRecipe matches when a given marker file exists in dir.
type markerRecipe struct {
	name   string
	marker string
}

func (m markerRecipe) Name() string { return m.name }
func (m markerRecipe) Detect(ctx context.Context, fs FS, dir string) (*Install, error) {
	ok, err := fs.Exists(ctx, dir+"/"+m.marker)
	if err != nil || !ok {
		return nil, err
	}
	return &Install{Framework: m.name, Root: dir}, nil
}

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFindsInstallsPerRoot(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "public_html/marker_a")
	mkfile(t, root, "www/marker_b")
	// "web" exists but matches nothing -> no install, not an error.
	if err := os.MkdirAll(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}

	fs := NewDirFS(root)
	recipes := []Recipe{
		markerRecipe{name: "a", marker: "marker_a"},
		markerRecipe{name: "b", marker: "marker_b"},
	}
	got, err := Scan(context.Background(), fs, DocrootCandidates(""), recipes)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 installs, got %d: %+v", len(got), got)
	}
	if got[0].Framework != "a" || got[1].Framework != "b" {
		t.Errorf("installs not sorted by framework: %+v", got)
	}
}

func TestScanFirstRecipeWinsPerRoot(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "public_html/marker_a")
	mkfile(t, root, "public_html/marker_b") // both match; order decides

	got, err := Scan(context.Background(), NewDirFS(root), []string{"public_html"},
		[]Recipe{markerRecipe{"a", "marker_a"}, markerRecipe{"b", "marker_b"}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Framework != "a" {
		t.Errorf("first matching recipe should win, got %+v", got)
	}
}

func TestScanSkipsMissingRoots(t *testing.T) {
	got, err := Scan(context.Background(), NewDirFS(t.TempDir()),
		[]string{"nope", "also_nope"}, []Recipe{markerRecipe{"a", "m"}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no installs for missing roots, got %+v", got)
	}
}

func TestDirFSConfinement(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewDirFS(root)
	// A traversal attempt is clamped to root, so the outside file is unseen.
	ok, err := fs.Exists(context.Background(), "../secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("dirFS must not resolve paths outside its root")
	}
}
