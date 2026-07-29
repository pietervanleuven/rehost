package db

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// Conn is the destination-host capability Import needs: buffered Run for the
// FIFO setup/teardown and the post-import verification, plus StreamPipe for
// the two streamed sessions (the dump relay and the credential feeder).
// *ssh.Client satisfies it.
type Conn interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
	StreamPipe(ctx context.Context, cmd string, stdin io.Reader, w io.Writer) (ssh.Result, error)
}

// ImportOptions tunes one import. The zero value streams plain SQL (no remote
// gzip) with the utf8mb4 default charset and no progress reporting.
type ImportOptions struct {
	// RemoteGunzip pipes the raw .gz through `gunzip -c` on the destination so
	// only compressed bytes cross the wire. Set it false for hosts whose check
	// reported no gzip: the dump is then decompressed locally and plain SQL is
	// streamed instead. This package never probes — the caller decides from the
	// destination capability report, mirroring how the check gate feeds Dump.
	RemoteGunzip bool
	// Charset is passed to mysql as --default-character-set. Empty means
	// utf8mb4 (the charset the rest of rehost standardizes on); callers that
	// inspected a different source default may override it.
	Charset string
	// Progress, when set, is called as the local dump file is consumed. Both
	// arguments count compressed bytes: sent is the file offset reached, total
	// is the file's size — truthful whether the dump is streamed raw or
	// decompressed locally first (the counter always sits on the file). A final
	// call with sent == total fires at EOF.
	Progress func(sent, total int64)
}

// ImportResult describes a completed (and verified) import.
type ImportResult struct {
	CompressedBytes int64         `json:"compressed_bytes"` // size of the local .gz
	Bytes           int64         `json:"bytes"`            // uncompressed SQL, from local validation
	SourceTables    int           `json:"source_tables"`    // CREATE TABLE statements in the dump
	DestTables      int           `json:"dest_tables"`      // information_schema table count after import
	FooterOK        bool          `json:"footer_ok"`        // the local dump carried mysqldump's completion footer
	Duration        time.Duration `json:"duration_ns"`
}

// Import streams a local gzipped dump file into the destination's MySQL and
// verifies the result. The dump contains DROP TABLE/VIEW/TRIGGER/ROUTINE IF
// EXISTS statements (both the mysqldump and the PHP-helper path emit them), so
// re-import is a deterministic overwrite — rerunning Import converges the
// destination onto the dump rather than accumulating state. That is the
// convergence story for the database side.
//
// The dump is validated locally first: it is gunzipped in full and its
// completion footer checked, so a truncated dump fails here — before any
// destination session is opened, never a partial import. Then the SQL is
// relayed to `mysql` over StreamPipe (through `gunzip -c` on the destination
// when opts.RemoteGunzip, else decompressed locally). After the pipeline exits
// 0 the destination's table count is read back into the result so the caller
// can compare it with the source's count from Inspect.
//
// The password never reaches the destination via argv, environment, or a file
// on disk: mysql's stdin is busy carrying the dump, so the Inspect-style
// defaults-file-on-stdin trick is unavailable here. Instead the credentials
// travel through a short-lived, mode-600 FIFO — see importCreds.
func Import(ctx context.Context, conn Conn, creds *Credentials, dumpPath string, opts ImportOptions) (*ImportResult, error) {
	start := time.Now()
	charset := opts.Charset
	if charset == "" {
		charset = "utf8mb4"
	}

	// analyzeDump gunzips the whole file up front — one cheap local pass — so a
	// truncated or footer-less dump is refused before any destination session
	// opens, never after a half-applied import.
	fi, err := os.Stat(dumpPath)
	if err != nil {
		return nil, err
	}
	vstats := &DumpStats{}
	vf, err := os.Open(dumpPath)
	if err != nil {
		return nil, err
	}
	analyzeDump(vf, vstats)
	_ = vf.Close()
	if !vstats.FooterOK {
		return nil, fmt.Errorf("refusing to import %s: the local dump is incomplete — mysqldump's completion footer is missing (%s of SQL); re-run the dump",
			dumpPath, humanBytes(vstats.Bytes))
	}

	result := &ImportResult{
		CompressedBytes: fi.Size(),
		Bytes:           vstats.Bytes,
		SourceTables:    vstats.Tables,
		FooterOK:        true,
	}

	// The progress counter wraps the file, not the payload: with local
	// decompression the gzip reader sits on top of it, so offsets stay
	// compressed-file bytes and total stays the honest file size.
	df, err := os.Open(dumpPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = df.Close() }()
	counted := &progressReader{r: df, total: fi.Size(), step: progressStep(fi.Size()), cb: opts.Progress}
	var payload io.Reader = counted
	if !opts.RemoteGunzip {
		gz, err := gzip.NewReader(counted)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		payload = gz
	}

	if err := importCreds(ctx, conn, creds, charset, opts.RemoteGunzip, payload); err != nil {
		return nil, err
	}

	n, err := countTables(ctx, conn, creds)
	if err != nil {
		return nil, fmt.Errorf("import of %s completed but verification failed: %w", creds.Name, err)
	}
	result.DestTables = n
	result.Duration = time.Since(start)
	return result, nil
}

// importCreds runs the two concurrent destination sessions that together feed
// mysql without the password ever appearing in argv, the environment, or a
// file on disk:
//
//   - A mode-600 FIFO is created under ~/.rehost. A FIFO is an inode with no
//     data blocks — nothing is ever written to disk — and 0600 keeps other
//     users out. Both sessions are already-authenticated SSH sessions of the
//     same user.
//   - The "feeder" session runs `cat > <fifo>` with the [client] defaults file
//     as its stdin: the password reaches the FIFO through the SSH channel, not
//     through any command string (which would land in the remote `sh -c` argv).
//   - The "import" session runs `[gunzip -c |] mysql --defaults-extra-file=<fifo>`
//     with the dump on its stdin. mysql opens the FIFO at option-parse time,
//     reads the credentials, connects, then consumes the SQL from stdin.
//
// The two sessions rendezvous on the FIFO (each open blocks for the other), so
// they must run concurrently. The import session's exit status is authoritative
// for the database outcome; the feeder is best-effort. A failure on either side
// cancels the shared context to unblock a peer stuck on the FIFO, so a missing
// mysql or a bad password can never hang the run indefinitely. The FIFO is
// removed on every path.
func importCreds(ctx context.Context, conn Conn, creds *Credentials, charset string, remoteGunzip bool, payload io.Reader) error {
	token, err := fifoToken()
	if err != nil {
		return err
	}
	// The path keeps $HOME dynamic (double-quoted so a space in HOME survives)
	// while the token is hex, so no part needs shell-metachar quoting and none
	// of it can be an injection vector.
	fifoRef := `"$HOME"/.rehost/.import-creds.` + token

	setup := `mkdir -p "$HOME"/.rehost && mkfifo -m 600 ` + fifoRef
	if res, err := conn.Run(ctx, setup); err != nil {
		return err
	} else if res.ExitCode != 0 {
		return fmt.Errorf("preparing the credentials pipe on the destination: %s", ssh.FirstLine(res.Stderr))
	}
	defer func() {
		// Best-effort cleanup on a context detached from ctx's cancellation so a
		// cancelled or failed import still removes the FIFO.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = conn.Run(cctx, "rm -f "+fifoRef)
	}()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	importSQL := "mysql --defaults-extra-file=" + fifoRef +
		" --default-character-set=" + ssh.ShellQuote(charset) + " " + ssh.ShellQuote(creds.Name)
	if remoteGunzip {
		// The pipeline's exit status is mysql's (the last stage) under POSIX
		// sh, which is what we want: mysql is authoritative. The dump was
		// already footer-verified locally, so gunzip cannot see a truncated
		// stream here short of a transport cut (which StreamPipe surfaces).
		importSQL = "gunzip -c | " + importSQL
	}

	var (
		wg          sync.WaitGroup
		importRes   ssh.Result
		importErr   error
		feederErr   error
		credsReader = strings.NewReader(clientDefaults(creds))
	)
	wg.Add(2)
	go func() { // import session — carries the dump, decides the outcome
		defer wg.Done()
		importRes, importErr = conn.StreamPipe(runCtx, importSQL, payload, io.Discard)
		if importErr != nil || importRes.ExitCode != 0 {
			cancel() // free a feeder still blocked opening the FIFO
		}
	}()
	go func() { // feeder session — pushes the [client] defaults into the FIFO
		defer wg.Done()
		_, feederErr = conn.StreamPipe(runCtx, "cat > "+fifoRef, credsReader, io.Discard)
		if feederErr != nil {
			cancel() // free the import session waiting for credentials
		}
	}()
	wg.Wait()

	if importErr != nil {
		return importErr
	}
	if importRes.ExitCode != 0 || strings.Contains(importRes.Stderr, "ERROR") {
		return fmt.Errorf("mysql import of %s failed: %s", creds.Name, sanitizeReason(importRes.Stderr, creds.Password))
	}
	// A clean import proves the feeder delivered the credentials (mysql could
	// not have connected otherwise), so feederErr is only interesting when the
	// import itself did not fail first — surface it rather than swallow it.
	if feederErr != nil {
		return fmt.Errorf("feeding credentials to mysql for %s: %w", creds.Name, feederErr)
	}
	return nil
}

// countTablesSQL counts the destination's tables the same way Inspect does,
// via DATABASE() so the count is scoped to the imported schema.
const countTablesSQL = `SELECT COUNT(*) FROM information_schema.TABLES WHERE table_schema = DATABASE();`

// countTables reads the destination table count for verification. mysql's
// stdin is free again here, so the password rides the Inspect-style defaults
// file on stdin (heredoc) rather than a FIFO.
func countTables(ctx context.Context, r Runner, creds *Credentials) (int, error) {
	cmd := "mysql --defaults-extra-file=/dev/stdin --batch --skip-column-names --connect-timeout=10 -e " +
		ssh.ShellQuote(countTablesSQL) + " " + ssh.ShellQuote(creds.Name) +
		" <<'REHOST_CNF'\n" + clientDefaults(creds) + "REHOST_CNF"
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

// fifoToken returns a random hex token for a per-import FIFO name so parallel
// or repeated imports never collide and the path carries no shell metachars.
func fifoToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating credentials-pipe name: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// progressReader counts bytes read from the underlying (compressed) file and
// reports progress at most once per step bytes, plus a guaranteed final call
// at EOF. Sitting on the file means the count is compressed offsets whether or
// not we decompress locally, keeping total (the file size) honest.
type progressReader struct {
	r        io.Reader
	total    int64
	step     int64
	sent     int64
	reported int64
	cb       func(sent, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.sent += int64(n)
	if p.cb != nil && p.sent != p.reported && (p.sent-p.reported >= p.step || err == io.EOF) {
		p.cb(p.sent, p.total)
		p.reported = p.sent
	}
	return n, err
}

// progressStep picks the byte interval between Progress callbacks: total/100 so
// there are at most ~100 updates regardless of dump size, floored at 32 KiB so
// tiny dumps do not spam a callback per read (the final EOF call still fires).
func progressStep(total int64) int64 {
	step := total / 100
	if step < 32*1024 {
		step = 32 * 1024
	}
	return step
}
