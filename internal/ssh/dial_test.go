package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skeema/knownhosts"
)

// A real, valid ed25519 known_hosts entry (github.com's key) as the good line.
const goodKnownHost = "good.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func TestStageKnownHostsToleratesMalformedLines(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	content := goodKnownHost + "\n" +
		"#svn.example.com ssh-rsa \n" + // commented host, key wrapped away
		"AAAAB3NzaC1yc2EAAAADAQABAAABorphanedkeywithnohost\n" + // the orphan
		"\n" // blank line
	if err := os.WriteFile(kh, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// The raw file is exactly what broke a real connection: the parser rejects
	// the whole file because of the orphaned key line.
	if _, err := knownhosts.NewDB(kh); err == nil {
		t.Fatal("expected raw known_hosts with an orphaned key to fail parsing")
	}

	staged, cleanup, err := stageKnownHosts([]string{kh})
	if err != nil {
		t.Fatalf("stageKnownHosts: %v", err)
	}
	defer cleanup()

	// The staged copy parses cleanly...
	if _, err := knownhosts.NewDB(staged); err != nil {
		t.Fatalf("staged known_hosts should parse: %v", err)
	}
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	// ...keeps the valid host...
	if !strings.Contains(string(data), "good.example.com") {
		t.Error("valid host entry should survive staging")
	}
	// ...and drops the malformed orphan.
	if strings.Contains(string(data), "orphanedkeywithnohost") {
		t.Error("orphaned key line should have been dropped")
	}
}

func TestStageKnownHostsEmptyFile(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(kh, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	staged, cleanup, err := stageKnownHosts([]string{kh})
	if err != nil {
		t.Fatalf("stageKnownHosts on empty file: %v", err)
	}
	defer cleanup()
	if _, err := knownhosts.NewDB(staged); err != nil {
		t.Fatalf("empty staged known_hosts should parse: %v", err)
	}
}
