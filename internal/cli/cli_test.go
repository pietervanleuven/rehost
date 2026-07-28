package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd(BuildInfo{Version: "test", Commit: "abc", Date: "today"})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestHelpListsAllCommands(t *testing.T) {
	stdout, _, err := run(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, cmd := range []string{"init", "check", "plan", "migrate", "status", "history", "unlock", "version"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("help output missing command %q", cmd)
		}
	}
}

func TestVersion(t *testing.T) {
	stdout, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, "rehost test (commit abc, built today)") {
		t.Errorf("unexpected version output: %q", stdout)
	}
}

func TestPlanMissingProjectFile(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // non-interactive: example + error, never the host form
	missing := filepath.Join(t.TempDir(), "migrate.yaml")
	_, stderr, err := run(t, "plan", "-f", missing)
	if err == nil {
		t.Fatal("plan without a project file must fail")
	}
	if !strings.Contains(stderr, "version: 1") || !strings.Contains(stderr, "source.example.com") {
		t.Errorf("stderr should show an example project file, got:\n%s", stderr)
	}
}

func TestPlanRejectsInvalidTarget(t *testing.T) {
	_, _, err := run(t, "plan", "user@host:notaport")
	if err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("want invalid-port error, got: %v", err)
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in      string
		user    string
		host    string
		port    int
		wantErr bool
	}{
		{in: "deploy@web.example.com:2222", user: "deploy", host: "web.example.com", port: 2222},
		{in: "web.example.com", host: "web.example.com"},
		{in: "u@web", user: "u", host: "web"},
		{in: "a@b@web", user: "a@b", host: "web"}, // last @ splits, ssh-style
		{in: "u@", wantErr: true},
		{in: "u@h:0", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseTarget(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTarget(%q) should fail", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTarget(%q): %v", c.in, err)
			continue
		}
		if got.User != c.user || got.Host != c.host || got.Port != c.port {
			t.Errorf("parseTarget(%q) = %+v", c.in, got)
		}
	}
}

func TestInitNonInteractive(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // force plain mode regardless of test TTY
	file := filepath.Join(t.TempDir(), "migrate.yaml")
	_, stderr, err := run(t, "init", "-f", file)
	if err == nil {
		t.Fatal("init without a terminal must fail")
	}
	if !strings.Contains(err.Error(), "interactive") {
		t.Errorf("error should explain a terminal is needed, got: %v", err)
	}
	if !strings.Contains(stderr, "version: 1") || !strings.Contains(stderr, "source.example.com") {
		t.Errorf("stderr should show an example project file, got:\n%s", stderr)
	}
	if _, statErr := os.Stat(file); statErr == nil {
		t.Error("non-interactive init must not write a project file")
	}
}

func TestInitJSONRefused(t *testing.T) {
	_, _, err := run(t, "init", "--json", "-f", filepath.Join(t.TempDir(), "migrate.yaml"))
	if err == nil || !strings.Contains(err.Error(), "--json") {
		t.Errorf("init --json should fail mentioning --json, got: %v", err)
	}
}

func TestCheckMissingProjectFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "migrate.yaml")
	_, _, err := run(t, "check", "-f", missing)
	if err == nil || !strings.Contains(err.Error(), "rehost init") {
		t.Errorf("check without a project file should point at init, got: %v", err)
	}
}

func TestCheckRequiresDestination(t *testing.T) {
	file := filepath.Join(t.TempDir(), "migrate.yaml")
	yaml := "version: 1\nsource:\n  host: source.example.com\n"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "check", "-f", file)
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Errorf("check without a destination should say so, got: %v", err)
	}
}

func TestWriteSites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.yaml")
	f := &project.File{Version: project.SchemaVersion, Source: project.Host{Host: "src.example.com"}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	reports := []tui.HostReport{
		{Role: "source", Installs: []detect.Install{
			{Framework: "wordpress", Root: "/home/u/public_html", Version: "6.5.2"},
		}},
		{Role: "destination", Installs: []detect.Install{
			{Framework: "static", Root: "/home/d/www"}, // never persisted
		}},
	}
	updated, err := writeSites(f, path, reports)
	if err != nil || !updated {
		t.Fatalf("writeSites = %v, %v", updated, err)
	}

	loaded, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Sites) != 1 || loaded.Sites[0].Framework != "wordpress" ||
		loaded.Sites[0].Root != "/home/u/public_html" || loaded.Sites[0].Version != "6.5.2" {
		t.Errorf("persisted sites = %+v", loaded.Sites)
	}

	// Unchanged detection must not rewrite the file.
	updated, err = writeSites(loaded, path, reports)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Error("identical sites should not trigger a write")
	}
}

func TestStubsFail(t *testing.T) {
	for _, cmd := range []string{"migrate", "unlock"} {
		_, _, err := run(t, cmd)
		if err == nil {
			t.Errorf("stub %q should fail until implemented", cmd)
			continue
		}
		if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("stub %q error should say not implemented, got: %v", cmd, err)
		}
	}
}
