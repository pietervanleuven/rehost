package ssh

import "strings"

// ShellQuote wraps s in single quotes, escaping any embedded ones, so it
// survives a POSIX shell unchanged.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
