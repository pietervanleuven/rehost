package db

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// PostgreSQL support mirrors the MySQL path's credential discipline as far
// as libpq allows: the password never appears in argv or the environment.
// libpq refuses PGPASSFILE on anything but a regular file (a FIFO or
// /dev/stdin is rejected with "not a plain file"), so the password is staged
// in a mode-600 file under ~/.rehost for the seconds the command runs —
// exactly the contract of a user's own ~/.pgpass — created under umask 077
// and removed on the same command line wherever stdin is free.

// pgPassLine renders the ~/.pgpass entry for creds: wildcards for the
// connection fields (so host/port spelling can never mismatch) and the
// password with its two escapable bytes escaped. A newline cannot be
// represented in the line-based pgpass format at all.
func pgPassLine(creds *Credentials) (string, error) {
	if strings.ContainsAny(creds.Password, "\n\r") {
		return "", fmt.Errorf("the password for %s contains a newline, which the PostgreSQL password-file format cannot carry", creds.Name)
	}
	pw := strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(creds.Password)
	return "*:*:*:*:" + pw, nil
}

// pgConnFlags renders the psql/pg_dump connection flags for creds. An empty
// host means the local default (socket); pg has no combined host:port form
// to split — applyHost already separated any configured port.
func pgConnFlags(creds *Credentials) string {
	var b strings.Builder
	if creds.Host != "" {
		b.WriteString(" -h " + ssh.ShellQuote(creds.Host))
	}
	if creds.Port != 0 {
		b.WriteString(" -p " + strconv.Itoa(creds.Port))
	}
	if creds.User != "" {
		b.WriteString(" -U " + ssh.ShellQuote(creds.User))
	}
	return b.String()
}

// pgWithPassFile wraps cmd in the pgpass staging: the file is written from a
// heredoc (stdin must be free), cmd runs with $t holding its path, and the
// file is removed whatever cmd's exit was, which is then reported. cmd must
// reference the passfile as PGPASSFILE="$t".
func pgWithPassFile(creds *Credentials, cmd string) (string, error) {
	line, err := pgPassLine(creds)
	if err != nil {
		return "", err
	}
	token, err := fifoToken()
	if err != nil {
		return "", err
	}
	marker := "REHOST_PGPASS"
	for strings.Contains(line, marker) {
		marker += "Z"
	}
	return `umask 077 && mkdir -p "$HOME"/.rehost && t="$HOME"/.rehost/.pgpass.` + token +
		` && cat > "$t" <<'` + marker + `' && ` + cmd + `; s=$?; rm -f "$t"; exit $s` +
		"\n" + line + "\n" + marker, nil
}

// inspectPGSQL gathers the same facts the MySQL inspection reads, one
// labeled row each. utf8mb4 accounting stays zero on purpose: that rule is
// MySQL-specific and must stay silent for PostgreSQL sites.
const inspectPGSQL = `SELECT 'version::' || current_setting('server_version')
UNION ALL SELECT 'sizekb::' || (pg_database_size(current_database())/1024)::text
UNION ALL SELECT 'tables::' || (SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') AND table_type = 'BASE TABLE')::text
UNION ALL SELECT 'encoding::' || pg_encoding_to_char(encoding) FROM pg_database WHERE datname = current_database()`

// inspectPG is Inspect for the pgsql driver: same contract, psql transport.
func inspectPG(ctx context.Context, r Runner, creds *Credentials) (*Inspection, error) {
	psql := `PGPASSFILE="$t" ` + creds.client() + " -w -X -A -t" + pgConnFlags(creds) +
		" -d " + ssh.ShellQuote(creds.Name) + " -c " + ssh.ShellQuote(inspectPGSQL)
	cmd, err := pgWithPassFile(creds, psql)
	if err != nil {
		return nil, err
	}
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return &Inspection{Connected: false, Reason: sanitizeReason(res.Stderr, creds.Password)}, nil
	}
	insp := &Inspection{Connected: true}
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "::")
		if !ok {
			continue
		}
		switch key {
		case "version":
			insp.ServerVersion = val
		case "sizekb":
			insp.SizeKB, _ = strconv.ParseInt(val, 10, 64)
		case "tables":
			insp.Tables, _ = strconv.Atoi(val)
		case "encoding":
			insp.Charset = val
		}
	}
	return insp, nil
}

// pgDumpCmd builds the source pipeline: a plain-format pg_dump (its trailing
// "database dump complete" comment is the completion footer the shared
// verifier anchors on), ownership and grants stripped — a cross-account
// restore must not try to ALTER OWNER to the source's role — gzipped on the
// wire. Same heredoc discipline as the MySQL path: pg_dump's stdin is free.
func pgDumpCmd(creds *Credentials) (string, error) {
	dump := `PGPASSFILE="$t" ` + creds.dumper() + " -w --no-owner --no-privileges" + pgConnFlags(creds) +
		" " + ssh.ShellQuote(creds.Name) + " | gzip"
	return pgWithPassFile(creds, dump)
}

// DumpPG streams a verified gzipped pg_dump of the database into w — the
// PostgreSQL counterpart of Dump, sharing its verification scaffolding. Dump
// dispatches here by driver; callers normally never call it directly.
func DumpPG(ctx context.Context, s Streamer, creds *Credentials, w io.Writer) (*DumpStats, error) {
	cmd, err := pgDumpCmd(creds)
	if err != nil {
		return nil, err
	}
	return streamVerifiedDump(ctx, s, cmd, w, creds, "pg_dump", "pg_dump's")
}

// importPGRun relays the (possibly locally gunzipped) SQL payload into psql.
// Unlike the MySQL path there is no FIFO rendezvous: libpq insists on a
// regular passfile, so a setup command stages it (stdin is free there) and a
// deferred command removes it. ON_ERROR_STOP makes psql's exit status
// honest — by default it would exit 0 past failed statements.
func importPGRun(ctx context.Context, conn Conn, creds *Credentials, remoteGunzip bool, payload io.Reader) error {
	line, err := pgPassLine(creds)
	if err != nil {
		return err
	}
	token, err := fifoToken()
	if err != nil {
		return err
	}
	passRef := `"$HOME"/.rehost/.pgpass.` + token
	marker := "REHOST_PGPASS"
	for strings.Contains(line, marker) {
		marker += "Z"
	}
	setup := `umask 077 && mkdir -p "$HOME"/.rehost && cat > ` + passRef + ` <<'` + marker + `'` +
		"\n" + line + "\n" + marker
	if res, err := conn.Run(ctx, setup); err != nil {
		return err
	} else if res.ExitCode != 0 {
		return fmt.Errorf("staging the PostgreSQL password file on the destination: %s", ssh.FirstLine(res.Stderr))
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = conn.Run(cctx, "rm -f "+passRef)
	}()

	importSQL := "PGPASSFILE=" + passRef + " " + creds.client() + " -w -X -q -v ON_ERROR_STOP=1" +
		pgConnFlags(creds) + " -d " + ssh.ShellQuote(creds.Name)
	if remoteGunzip {
		// The pipeline's exit status is psql's (the last stage), which is
		// authoritative; the dump was footer-verified locally already.
		importSQL = "gunzip -c | " + importSQL
	}
	res, err := conn.StreamPipe(ctx, importSQL, payload, io.Discard)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("psql import of %s failed: %s", creds.Name, sanitizeReason(res.Stderr, creds.Password))
	}
	return nil
}

// countTablesPGSQL mirrors countTablesSQL: base tables in user schemas only,
// matching what the dump side counts as CREATE TABLE statements.
const countTablesPGSQL = `SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') AND table_type = 'BASE TABLE'`

// countTablesPG reads the destination table count for verification; stdin is
// free again here, so the passfile stages and cleans up on one command line.
func countTablesPG(ctx context.Context, r Runner, creds *Credentials) (int, error) {
	psql := `PGPASSFILE="$t" ` + creds.client() + " -w -X -A -t" + pgConnFlags(creds) +
		" -d " + ssh.ShellQuote(creds.Name) + " -c " + ssh.ShellQuote(countTablesPGSQL)
	cmd, err := pgWithPassFile(creds, psql)
	if err != nil {
		return 0, err
	}
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("%s", sanitizeReason(res.Stderr, creds.Password))
	}
	n, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		return 0, fmt.Errorf("unexpected table-count output %q", ssh.FirstLine(res.Stdout))
	}
	return n, nil
}
