package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validFile() *File {
	return &File{
		Version: SchemaVersion,
		Name:    "example-site",
		Source:  Host{Host: "source.example.com", User: "user", Auth: "agent"},
		Destination: &Host{
			Host: "dest.example.com", Port: 2222, User: "user", Auth: "key", KeyPath: "~/.ssh/id_ed25519",
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	want := validFile()
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got.Destination != *want.Destination || got.Source != want.Source || got.Name != want.Name || got.Version != want.Version {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("project file perms = %o, want 0600", perm)
		}
	}
}

func TestLoadRejectsSecretFields(t *testing.T) {
	for _, field := range []string{"password", "secret", "token"} {
		path := filepath.Join(t.TempDir(), DefaultFilename)
		content := "version: 1\nsource:\n  host: source.example.com\n  " + field + ": hunter2\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatalf("Load accepted a %q field", field)
		}
		if !strings.Contains(err.Error(), "must not contain secrets") {
			t.Errorf("error for %q should give secrets guidance, got: %v", field, err)
		}
	}
}

func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	content := "version: 2\nsource:\n  host: source.example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unsupported schema version 2") {
		t.Errorf("want unsupported-version error, got: %v", err)
	}
}

func TestValidateRejectsUserinfoInHost(t *testing.T) {
	f := validFile()
	f.Source.Host = "user:hunter2@source.example.com"
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "never put passwords") {
		t.Errorf("want userinfo rejection with guidance, got: %v", err)
	}
}

func TestValidateRejectsBadAuth(t *testing.T) {
	f := validFile()
	f.Source.Auth = "keyboard"
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "source.auth") {
		t.Errorf("want auth rejection, got: %v", err)
	}
}

func TestLoadMissingSourceHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	if err := os.WriteFile(path, []byte("version: 1\nsource: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "source.host is required") {
		t.Errorf("want missing-host error, got: %v", err)
	}
}

func TestExampleIsLoadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	if err := os.WriteFile(path, []byte(Example()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Example() should load cleanly: %v", err)
	}
}
