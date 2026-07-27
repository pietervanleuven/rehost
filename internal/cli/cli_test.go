package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
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
	for _, cmd := range []string{"init", "check", "plan", "migrate", "status", "unlock", "version"} {
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

func TestStubsFail(t *testing.T) {
	for _, cmd := range []string{"init", "check", "migrate", "status", "unlock"} {
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
