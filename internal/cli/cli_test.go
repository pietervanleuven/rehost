package cli

import (
	"bytes"
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
