package hostdb

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh/remote"
)

// Streamer executes a remote command with streaming stdout; *ssh.Client
// satisfies it.
type Streamer interface {
	Stream(ctx context.Context, cmd string, w io.Writer) (remote.Result, error)
}

// DumpStats describes a completed (and verified) dump.
type DumpStats struct {
	CompressedBytes int64         `json:"compressed_bytes"`
	Bytes           int64         `json:"bytes"` // uncompressed SQL
	Tables          int           `json:"tables"`
	FooterOK        bool          `json:"footer_ok"`
	Duration        time.Duration `json:"duration_ns"`
}

// dumpCmd builds the remote pipeline. --single-transaction --quick keeps a
// consistent InnoDB snapshot without buffering; --no-tablespaces avoids the
// PROCESS-privilege error shared hosts hit on MySQL 8; the inspected charset
// (when known) pins the connection so a legacy latin1-storing site is not
// transcoded; gzip compresses on the wire. Credentials travel like
// Inspect's: defaults file on stdin. The heredoc redirection must sit on
// mysqldump, before the pipe — a trailing redirection in a pipeline attaches
// to the last command, which would feed the defaults file to gzip instead.
func dumpCmd(creds *Credentials) string {
	cmd := creds.dumper() + " --defaults-extra-file=/dev/stdin --single-transaction --quick --no-tablespaces --routines --triggers"
	if creds.Charset != "" {
		cmd += " --default-character-set=" + remote.ShellQuote(creds.Charset)
	}
	return cmd + " " + remote.ShellQuote(creds.Name) + credsHeredoc(creds, " | gzip")
}

// Dump streams a gzipped dump of the database into w while verifying it on
// the fly: the stream is gunzipped in memory to count bytes and tables and
// to confirm the dumper's completion footer — the guard against a silently
// truncated dump (the shell reports gzip's exit status, not the dumper's).
// A verification failure returns the stats alongside the error so callers
// can report what did arrive. PostgreSQL credentials dispatch to DumpPG.
func Dump(ctx context.Context, s Streamer, creds *Credentials, w io.Writer) (*DumpStats, error) {
	if NormalizeDriver(creds.Driver) == DriverPostgres {
		return DumpPG(ctx, s, creds, w)
	}
	return streamVerifiedDump(ctx, s, dumpCmd(creds), w, creds, "mysqldump", "mysqldump's")
}

// streamVerifiedDump is the verification scaffolding Dump and DumpPHP share:
// it runs remoteCmd, tees its gzipped stdout into w, gunzips the tee in
// memory to count bytes/tables and watch for the completion footer, and maps
// the outcome to one error. tool names the producer in failure messages;
// whose possessive-cases it for the missing-footer message. Keeping both
// producers on this one path is what guarantees they accept and reject the
// same dumps.
func streamVerifiedDump(ctx context.Context, s Streamer, remoteCmd string, w io.Writer, creds *Credentials, tool, whose string) (*DumpStats, error) {
	stats := &DumpStats{}
	start := time.Now()

	pr, pw := io.Pipe()
	analyzed := make(chan struct{})
	go func() {
		defer close(analyzed)
		analyzeDump(pr, stats)
	}()

	counted := &countingWriter{}
	res, err := s.Stream(ctx, remoteCmd, io.MultiWriter(w, counted, pw))
	_ = pw.Close()
	<-analyzed
	stats.CompressedBytes = counted.n
	stats.Duration = time.Since(start)

	if err != nil {
		return stats, err
	}
	if res.ExitCode != 0 {
		return stats, fmt.Errorf("%s failed: %s", tool, sanitizeReason(res.Stderr, creds.Password))
	}
	if !stats.FooterOK {
		return stats, fmt.Errorf("dump of %s is incomplete — %s completion footer is missing (%s of SQL received)",
			creds.Name, whose, humanBytes(stats.Bytes))
	}
	return stats, nil
}

// createTableMarker introduces every CREATE TABLE statement in the dumps
// this package produces (mysqldump and the PHP helper both put a newline before it).
const createTableMarker = "\nCREATE TABLE"

// analyzeDump gunzips the stream, counting SQL bytes and CREATE TABLE
// statements and watching the tail for the completion footer.
func analyzeDump(r io.Reader, stats *DumpStats) {
	defer func() { _, _ = io.Copy(io.Discard, r) }() // never stall the writer side

	gz, err := gzip.NewReader(r)
	if err != nil {
		return // empty or non-gzip stream: nothing to verify
	}
	defer func() { _ = gz.Close() }()

	const tailKeep = 512
	var tail []byte
	// carry holds the trailing bytes of the previous chunk so a CREATE TABLE
	// marker split across a read boundary is still counted exactly once: it is
	// shorter than the marker, so any match it participates in must extend into
	// the new chunk (a boundary-straddling match), never one already counted.
	var carry []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := gz.Read(buf)
		if n > 0 {
			stats.Bytes += int64(n)
			combined := make([]byte, 0, len(carry)+n)
			combined = append(combined, carry...)
			combined = append(combined, buf[:n]...)
			stats.Tables += strings.Count(string(combined), createTableMarker)
			if keep := len(createTableMarker) - 1; len(combined) > keep {
				carry = append(carry[:0], combined[len(combined)-keep:]...)
			} else {
				carry = append(carry[:0], combined...)
			}
			tail = append(tail, buf[:n]...)
			if len(tail) > tailKeep {
				tail = tail[len(tail)-tailKeep:]
			}
		}
		if err != nil {
			break // io.EOF or a truncated gzip stream — the footer decides
		}
	}
	stats.FooterOK = footerComplete(tail)
}

// footerComplete reports whether the dump ends with a completion footer —
// mysqldump's "-- Dump completed on …" or pg_dump's "-- PostgreSQL database
// dump complete". Anchoring to the end (rather than a substring search
// anywhere in the tail) stops a truncated dump whose last bytes merely quote
// a footer inside a row from passing as complete: real dumps always end with
// the footer, after all data.
//
// mysqldump's footer really is the final line, but pg_dump's is not: it
// closes the footer with a bare "--" comment line, and since the 2025
// security releases it also appends a "\unrestrict <token>" meta-command.
// Both are structure rather than data, so they are skipped before the footer
// line is read — without that, every PostgreSQL dump reads as truncated.
// Skipping only exact "--" lines and psql backslash commands preserves the
// anti-spoofing property: a COPY data row cannot be the last thing in a dump
// and still look like a footer.
func footerComplete(tail []byte) bool {
	lines := strings.Split(strings.ReplaceAll(string(tail), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " \t")
		if line == "" || line == "--" || strings.HasPrefix(line, `\`) {
			continue
		}
		return strings.HasPrefix(line, "-- Dump completed") ||
			strings.HasPrefix(line, "-- PostgreSQL database dump complete")
	}
	return false
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
