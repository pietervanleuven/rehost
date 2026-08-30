package cli

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	searchreplace "github.com/pietervanleuven/go-searchreplace"
)

func TestDumpFileName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"wpdb", "wpdb.sql.gz"},
		{"client_prod-2024", "client_prod-2024.sql.gz"},
		{"drupal.9", "drupal.9.sql.gz"},
	}
	for _, tt := range tests {
		if got := dumpFileName(tt.name); got != tt.want {
			t.Errorf("dumpFileName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}

	// Remote-controlled names must never carry path bytes into the dumps dir.
	hostile := []string{
		"../../../home/user/.ssh/authorized_keys",
		"..",
		".",
		"",
		"a/b",
		`a\b`,
		".. x",
	}
	for _, name := range hostile {
		got := dumpFileName(name)
		if strings.ContainsAny(got, `/\`) || strings.HasPrefix(got, "..") {
			t.Errorf("dumpFileName(%q) = %q still escapes the dumps dir", name, got)
		}
	}

	// Distinct names must not collide after sanitizing.
	if dumpFileName("a/b") == dumpFileName("a_b.sql.gz") || dumpFileName("a/b") == dumpFileName("a?b") {
		t.Error("sanitized names collide")
	}
}

func writeGzDump(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "dump.sql.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := io.WriteString(gz, body); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func readGz(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFinalizeDumpFileStripsAndRewrites(t *testing.T) {
	// Long enough that the strip stage is still streaming while the rewrite
	// stage consumes — the two run concurrently across a pipe.
	var b strings.Builder
	b.WriteString("/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER t*/;\n")
	for i := 0; i < 20000; i++ {
		b.WriteString("INSERT INTO `wp_options` VALUES ('http://old.example.com/x');\n")
	}
	path := writeGzDump(t, t.TempDir(), b.String())

	pairs := []searchreplace.Pair{{From: "http://old.example.com", To: "https://new.example.com"}}
	stats, err := finalizeDumpFile(path, pairs)
	if err != nil {
		t.Fatalf("finalizeDumpFile: %v", err)
	}
	if stats == nil || stats.ValuesChanged == 0 {
		t.Fatalf("expected changed values, got %+v", stats)
	}
	got := readGz(t, path)
	if strings.Contains(got, "DEFINER") {
		t.Error("DEFINER clause survived the strip")
	}
	if strings.Contains(got, "old.example.com") {
		t.Error("old URL survived the rewrite")
	}
	if n := strings.Count(got, "https://new.example.com/x"); n != 20000 {
		t.Errorf("rewrote %d rows, want 20000", n)
	}
}

// A dump whose gzip stream is truncated must surface an error, leave the
// original file untouched, and — the reason finalizeDumpFile waits on the
// strip goroutine — return rather than deadlock or close gzIn under it.
func TestFinalizeDumpFileTruncatedInput(t *testing.T) {
	dir := t.TempDir()
	path := writeGzDump(t, dir, strings.Repeat("INSERT INTO `t` VALUES ('x');\n", 5000))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := finalizeDumpFile(path, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error on a truncated dump")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("finalizeDumpFile deadlocked on the strip goroutine")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(raw)/2 {
		t.Errorf("original dump was modified: %d bytes, want %d", len(after), len(raw)/2)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp rewrite file left behind: %v", entries)
	}
}
