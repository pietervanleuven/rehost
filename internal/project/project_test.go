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

func TestDestDBRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	f := validFile()
	f.Sites = []Site{{Framework: "wordpress", Root: "/home/u/public_html",
		DestDB: &SiteDB{Name: "u12345_wp", User: "u12345_wp", Host: "127.0.0.1", Port: 3307}}}
	if err := f.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if db := got.Sites[0].DestDB; db == nil || *db != *f.Sites[0].DestDB {
		t.Errorf("dest_db round-trip mismatch: %+v", got.Sites[0].DestDB)
	}

	f.Sites[0].DestDB.Name = ""
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "dest_db needs a name") {
		t.Errorf("dest_db without a name should fail validation, got %v", err)
	}
}

func TestDestDBPasswordRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	content := "version: 1\nsource:\n  host: s.example.com\nsites:\n" +
		"  - framework: wordpress\n    root: /home/u/site\n    dest_db:\n      name: db\n      password: hunter2\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "must not contain secrets") {
		t.Errorf("a dest_db password must be rejected with secrets guidance, got %v", err)
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

// TestSavePreservesComments is the regression guard for the plan-rewrites-
// migrate.yaml gap: a Load→edit→Save cycle must keep the header and every
// hand-written comment on sections it did not change, while still writing the
// updated sites block.
func TestSavePreservesComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	orig := `# migrate.yaml — rehost project file (schema v1)
# NEVER put passwords or secrets in this file; rehost prompts at runtime.
version: 1
name: my-site
domain: my-site.example    # keep the DNS checks on
source:
  host: prod.example.com   # my production box
  user: alice
destination:
  host: new.example.com
  user: alice
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.Sites = []Site{{Framework: "wordpress", Root: "/home/alice/public_html", Version: "6.5"}}
	if err := f.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{
		"# my production box",
		"# keep the DNS checks on",
		"# NEVER put passwords or secrets",
		"framework: wordpress",
		"root: /home/alice/public_html",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Save dropped %q from:\n%s", want, out)
		}
	}
	// The header must appear exactly once — no duplication on rewrite.
	if n := strings.Count(out, "rehost project file (schema v"); n != 1 {
		t.Errorf("header count = %d, want 1:\n%s", n, out)
	}
	// The result must still load and carry the edit.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload after Save: %v", err)
	}
	if len(reloaded.Sites) != 1 || reloaded.Sites[0].Framework != "wordpress" {
		t.Errorf("sites not persisted: %+v", reloaded.Sites)
	}
}

// TestSaveDropsKeyRemovedFromStruct confirms the merge tracks the struct, not
// just the file: clearing a field removes its key (and stale comment) rather
// than stranding an orphaned line.
func TestSaveDropsKeyRemovedFromStruct(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	orig := `version: 1
name: my-site
domain: gone.example   # about to be removed
source:
  host: prod.example.com
  user: alice
`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f.Domain = "" // omitempty → the key should disappear entirely
	if err := f.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "domain:") || strings.Contains(string(got), "about to be removed") {
		t.Errorf("cleared field left an orphan:\n%s", got)
	}
}

// TestSaveFreshFileHasHeader confirms the create path (no existing file, e.g.
// init) still emits the standard header.
func TestSaveFreshFileHasHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFilename)
	if err := validFile().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "rehost project file (schema v") {
		t.Errorf("fresh Save missing header:\n%s", got)
	}
}
