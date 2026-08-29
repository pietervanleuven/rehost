package db

import (
	"context"

	"github.com/pietervanleuven/go-ssh"
)

// SQLResult reports the outcome of one RunSQL call. OK is whether MySQL
// accepted the statements; Reason carries a sanitized failure message when it
// did not (a bad statement, a missing table, a refused connection). Stdout is
// the `--batch --skip-column-names` output, populated for SELECTs and empty for
// writes. Separating a MySQL-level failure (OK=false, nil error) from a
// transport failure (a non-nil error) is what lets callers degrade a
// best-effort statement — a cache clear against a table an alternate backend
// never created — to a warning instead of aborting.
type SQLResult struct {
	OK     bool
	Reason string
	Stdout string
}

// RunSQL executes one or more semicolon-separated statements against a site's
// database with its extracted credentials, in the same shape as Inspect: the
// password reaches the mysql client through a defaults file on stdin (heredoc),
// so it never appears in the remote argv or environment. An error return is a
// transport failure; a MySQL-level failure comes back as OK=false with a
// sanitized Reason, never an error.
func RunSQL(ctx context.Context, r Runner, creds *Credentials, sql string) (*SQLResult, error) {
	cmd := creds.client() + " --defaults-extra-file=/dev/stdin --batch --skip-column-names --connect-timeout=10 -e " +
		ssh.ShellQuote(sql) + " " + ssh.ShellQuote(creds.Name) + credsHeredoc(creds, "")
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return &SQLResult{Reason: sanitizeReason(res.Stderr, creds.Password)}, nil
	}
	return &SQLResult{OK: true, Stdout: res.Stdout}, nil
}
