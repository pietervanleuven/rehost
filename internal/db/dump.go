package db

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/units"
)

// Streamer executes a remote command with streaming stdout; *ssh.Client
// satisfies it.
type Streamer interface {
	Stream(ctx context.Context, cmd string, w io.Writer) (ssh.Result, error)
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
// PROCESS-privilege error shared hosts hit on MySQL 8; gzip compresses on
// the wire. Credentials travel like Inspect's: defaults file on stdin.
func dumpCmd(creds *Credentials) string {
	return "mysqldump --defaults-extra-file=/dev/stdin --single-transaction --quick --no-tablespaces --routines --triggers " +
		ssh.ShellQuote(creds.Name) +
		" | gzip <<'REHOST_CNF'\n" + clientDefaults(creds) + "REHOST_CNF"
}

// Dump streams a gzipped mysqldump of the database into w while verifying
// it on the fly: the stream is gunzipped in memory to count bytes and
// tables and to confirm mysqldump's completion footer — the guard against
// a silently truncated dump (the shell reports gzip's exit status, not
// mysqldump's). A verification failure returns the stats alongside the
// error so callers can report what did arrive.
func Dump(ctx context.Context, s Streamer, creds *Credentials, w io.Writer) (*DumpStats, error) {
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
			creds.Name, whose, units.HumanBytes(stats.Bytes))
	}
	return stats, nil
}

// analyzeDump gunzips the stream, counting SQL bytes and CREATE TABLE
// statements and watching the tail for mysqldump's completion footer.
func analyzeDump(r io.Reader, stats *DumpStats) {
	defer func() { _, _ = io.Copy(io.Discard, r) }() // never stall the writer side

	gz, err := gzip.NewReader(r)
	if err != nil {
		return // empty or non-gzip stream: nothing to verify
	}
	defer func() { _ = gz.Close() }()

	const tailKeep = 512
	var tail []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := gz.Read(buf)
		if n > 0 {
			stats.Bytes += int64(n)
			stats.Tables += strings.Count(string(buf[:n]), "\nCREATE TABLE")
			tail = append(tail, buf[:n]...)
			if len(tail) > tailKeep {
				tail = tail[len(tail)-tailKeep:]
			}
		}
		if err != nil {
			break // io.EOF or a truncated gzip stream — the footer decides
		}
	}
	stats.FooterOK = strings.Contains(string(tail), "-- Dump completed")
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
