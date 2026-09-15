// Package remote is the transport-free half of the ssh package: the result type,
// helpers and capability probe a consumer needs to run commands on a remote
// host without caring how the connection is made. Packages that only consume
// command output should depend on this package alone — it keeps the SSH dial
// stack (and its dependencies) out of their build, and any transport whose
// Run matches Runner can stand in for a real connection.
package remote

import (
	"context"
	"strings"
)

// Result is the outcome of one remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int // -1 when the session ended without reporting a status
}

// Runner executes one command on a remote host. A non-zero exit status is
// not a Go error — only transport and session failures are. *ssh.Client
// implements it.
type Runner interface {
	Run(ctx context.Context, cmd string) (Result, error)
}

// FirstLine returns the trimmed first line of command output — the useful
// part of stderr for an error message — or "no error output" when there is
// none.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no error output"
	}
	return s
}

// ShellQuote wraps s in single quotes, escaping any embedded ones, so it
// survives a POSIX shell unchanged.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
