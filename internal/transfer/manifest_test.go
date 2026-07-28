package transfer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// manifestRunner answers each find variant with a canned result. The zero
// value answers everything with a clean empty run (exit 0).
type manifestRunner struct {
	printf ssh.Result
	print0 ssh.Result
	print  ssh.Result
	cmds   []string
}

func (m *manifestRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	m.cmds = append(m.cmds, cmd)
	switch {
	case strings.Contains(cmd, "-printf"):
		return m.printf, nil
	case strings.Contains(cmd, "-print0"):
		return m.print0, nil
	default:
		return m.print, nil
	}
}

const printfListing = "512 1690000000.1234 index.php\x00" +
	"2048 1690000100.0000 wp-content/uploads/a b.jpg\x00" +
	"notarecord\x00" +
	"77 1690000300.0 weird\nname.txt\x00" +
	"1024 1690000200.5 wp-includes/version.php\x00"

func TestTakeManifestGNU(t *testing.T) {
	r := &manifestRunner{printf: ssh.Result{Stdout: printfListing}}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", []string{"wp-content/cache", ".git"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || len(m.Files) != 4 {
		t.Fatalf("manifest = %+v", m)
	}
	// Sorted by path; spaces and even newlines in filenames survive.
	if m.Files[3].Path != "wp-includes/version.php" {
		t.Errorf("sort order: %+v", m.Files)
	}
	if m.Files[2].Path != "wp-content/uploads/a b.jpg" || m.Files[2].Size != 2048 || m.Files[2].MTime != 1690000100 {
		t.Errorf("entry = %+v", m.Files[2])
	}
	if m.Files[1].Path != "weird\nname.txt" || m.Files[1].Size != 77 {
		t.Errorf("newline filename should survive: %+v", m.Files[1])
	}
	if m.TotalBytes() != 3661 {
		t.Errorf("TotalBytes = %d", m.TotalBytes())
	}
	if !strings.Contains(r.cmds[0], `\( -path '/home/u/site/wp-content/cache' -o -path '/home/u/site/.git' \) -prune`) {
		t.Errorf("excludes not pruned: %s", r.cmds[0])
	}
	if strings.Contains(r.cmds[0], "2>/dev/null") {
		t.Errorf("stderr must stay attached for diagnostics: %s", r.cmds[0])
	}
}

func TestTakeManifestEmptySiteIsComplete(t *testing.T) {
	// GNU find on an empty (or fully excluded) docroot: exit 0, no output.
	// That is a real empty site, not a missing -printf.
	r := &manifestRunner{}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || len(m.Files) != 0 {
		t.Fatalf("empty site manifest = %+v", m)
	}
	if len(r.cmds) != 1 {
		t.Errorf("a clean empty run must not fall back: %v", r.cmds)
	}
}

func TestTakeManifestPermissionNoiseAccepted(t *testing.T) {
	// Exit 1 with output is find's "some subdirectories were unreadable" —
	// the documented skip-not-fatal case.
	r := &manifestRunner{printf: ssh.Result{
		Stdout:   "512 1690000000.0 index.php\x00",
		Stderr:   "find: ‘/home/u/site/private’: Permission denied\n",
		ExitCode: 1,
	}}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || len(m.Files) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestTakeManifestKilledMidListingFails(t *testing.T) {
	// A find killed by the host's resource limiter leaves partial output and
	// an exit status > 1: the truncated listing must never become a manifest.
	r := &manifestRunner{printf: ssh.Result{
		Stdout:   "512 1690000000.0 index.php\x00",
		ExitCode: 137,
	}}
	if _, err := TakeManifest(context.Background(), r, "/home/u/site", nil); err == nil {
		t.Fatal("truncated listing must be an error")
	} else if !strings.Contains(err.Error(), "exit 137") {
		t.Errorf("error should carry the exit code: %v", err)
	}
}

func TestTakeManifestFallbackPrint0(t *testing.T) {
	r := &manifestRunner{
		printf: ssh.Result{ExitCode: 1, Stderr: "find: -printf: unknown primary or operator\n"},
		print0: ssh.Result{Stdout: "/home/u/site/index.html\x00/home/u/site/css/ site.css\x00"},
	}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Complete || len(m.Files) != 2 {
		t.Fatalf("fallback manifest = %+v", m)
	}
	if m.Files[0].Path != "css/ site.css" {
		t.Errorf("paths must be root-relative and byte-exact: %+v", m.Files[0])
	}
}

func TestTakeManifestLastResortPrint(t *testing.T) {
	r := &manifestRunner{
		printf: ssh.Result{ExitCode: 1},
		print0: ssh.Result{ExitCode: 1},
		print:  ssh.Result{Stdout: "/home/u/site/index.html\n/home/u/site/css/ site.css \n"},
	}
	m, err := TakeManifest(context.Background(), r, "/home/u/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Complete || len(m.Files) != 2 {
		t.Fatalf("last-resort manifest = %+v", m)
	}
	if m.Files[0].Path != "css/ site.css " {
		t.Errorf("whitespace in filenames must survive: %q", m.Files[0].Path)
	}
	if len(r.cmds) != 3 {
		t.Errorf("expected the full degradation ladder, got %v", r.cmds)
	}
}

func TestTakeManifestAllVariantsFail(t *testing.T) {
	fail := ssh.Result{ExitCode: 1, Stderr: "find: /home/u/site: No such file or directory\n"}
	r := &manifestRunner{printf: fail, print0: fail, print: fail}
	if _, err := TakeManifest(context.Background(), r, "/home/u/site", nil); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("error should surface find's stderr: %v", err)
	}
}

func TestFindCmdEscapesGlobMetacharacters(t *testing.T) {
	cmd := findCmd("/home/u/site[old]", []string{"cache*", "wh?t"}, findPrintf)
	// ] needs no escape: it is literal outside a bracket expression.
	for _, want := range []string{`'/home/u/site\[old]/cache\*'`, `'/home/u/site\[old]/wh\?t'`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("prune pattern not glob-escaped: %s (want %s)", cmd, want)
		}
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
	path := filepath.Join(dir, ManifestFilename("u@source.example.com", "/home/u/public_html"))
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
	a := ManifestFilename("u@source.example.com", "/home/u/public_html")
	if a != ManifestFilename("u@source.example.com", "/home/u/public_html") {
		t.Error("filename must be deterministic")
	}
	if a == ManifestFilename("u@source.example.com", "/home/u/other") {
		t.Error("different roots must map to different files")
	}
	if a == ManifestFilename("u@other.example.com", "/home/u/public_html") {
		t.Error("the same root on a different source is a different site")
	}
	if !strings.HasPrefix(a, "public_html-") {
		t.Errorf("filename should stay readable: %s", a)
	}
}
