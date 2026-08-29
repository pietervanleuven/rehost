package ssh

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestShellQuoteRoundTrip pushes adversarial inputs through a real sh -c and
// asserts every byte comes back verbatim — ShellQuote is the single
// security-load-bearing function of the remote command layer.
func TestShellQuoteRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("quoting targets the remote POSIX shell")
	}
	inputs := []string{
		"plain",
		"with space",
		"it's",
		`double"quote`,
		"'; rm -rf / #",
		"$(reboot)",
		"`reboot`",
		"$HOME ${PATH}",
		"a;b|c&d>e<f",
		"back\\slash",
		"new\nline",
		"tab\there",
		"*glob?[x]",
		"~tilde",
		"-leading-dash",
		"!history",
		"'''",
		`'\''`,
		"héllo wörld …",
		"#comment",
	}
	for _, in := range inputs {
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(in)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round trip mangled %q → %q", in, out)
		}
	}
}

// A quoted argument must always be exactly one argv entry, never split or
// interpreted.
func TestShellQuoteSingleArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("quoting targets the remote POSIX shell")
	}
	in := "a b\nc'd$e"
	out, err := exec.Command("sh", "-c", `for a in `+ShellQuote(in)+`; do printf '%s\0' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(parts) != 1 || parts[0] != in {
		t.Errorf("quoted value split into %q", parts)
	}
}
