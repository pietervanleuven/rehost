package cli

import (
	"strings"
	"testing"
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
