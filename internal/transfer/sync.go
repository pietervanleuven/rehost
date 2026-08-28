package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/units"
)

// Conn is the slice of *ssh.Client a Sync endpoint needs: a buffered exec for
// setup and inspection (mkdir, find, rm) and a streamed stdin+stdout exec for
// the tar relay. *ssh.Client satisfies Run today; StreamPipe is the streaming
// primitive with a fed stdin — the current ssh.Client.Stream is StreamPipe
// with a nil stdin, so the relay needs that one companion method on the
// client (see the package doc note in sync.go).
type Conn interface {
	// Run executes cmd and buffers its stdout (mkdir, find, rm).
	Run(ctx context.Context, cmd string) (ssh.Result, error)
	// StreamPipe executes cmd, copying stdin into the remote process and its
	// stdout into w as bytes arrive — neither side is buffered whole. A nil
	// stdin means the command reads no input.
	StreamPipe(ctx context.Context, cmd string, stdin io.Reader, w io.Writer) (ssh.Result, error)
}

// Endpoint is one side of a sync: a connection, the docroot on that host, and
// the host's user@host identity (used for the persisted manifest's filename
// and for messages).
type Endpoint struct {
	Conn   Conn
	Root   string
	Target string
}

// Options tunes a Sync. The capability-probe-first posture lives in the
// caller: Compress and NullList are decided from the two hosts' capabilities
// (both ends have gzip; the source tar is GNU tar) and passed in — Sync stays
// pure and does not probe.
type Options struct {
	// Delete removes destination-only files (rsync --delete). Off by default:
	// the decided destination-state policy is additive.
	Delete bool
	// Compress pipes the relay through gzip on both ends. Requires gzip on the
	// source and the destination.
	Compress bool
	// NullList feeds the source tar a NUL-delimited file list (GNU tar's
	// --null -T -), keeping filenames with newlines byte-exact. When false the
	// list is newline-delimited (the portable degradation; a filename
	// containing a newline would then split — rare, and the manifest still
	// catches the miss on the next run).
	NullList bool
	// DestManifestPath, when set, is where the post-sync destination manifest
	// is atomically persisted (the caller owns the .rehost layout and supplies
	// the path; DestManifestFilename derives a stable basename).
	DestManifestPath string
	// MaxRmBytes caps the length of a single rm command line when deleting
	// destination-only files; zero uses defaultMaxRmBytes.
	MaxRmBytes int
}

// defaultMaxRmBytes keeps a batched `rm` well under a conservative ARG_MAX so
// deletion never trips the argument-list-too-long limit on a shared host.
const defaultMaxRmBytes = 96 << 10

// SyncStats is what one Sync did — enough for the cutover report.
type SyncStats struct {
	Compressed bool `json:"compressed"`
	// Degraded means at least one end's manifest was paths-only, so the diff
	// could compare presence but not size/mtime: files modified in place are
	// not detected and not re-sent. Surfaced so the report never claims full
	// convergence it cannot prove.
	Degraded          bool          `json:"degraded"`
	FilesSent         int           `json:"files_sent"`
	BytesSent         int64         `json:"bytes_sent"` // logical, from source manifest sizes (0 when degraded)
	WireBytes         int64         `json:"wire_bytes"` // bytes over the relay (compressed when Compressed)
	FilesDeleted      int           `json:"files_deleted"`
	DestOnlyRemaining int           `json:"dest_only_remaining"` // present on the destination, absent on the source, still there after sync
	UnsafePaths       int           `json:"unsafe_paths"`        // destination-only paths refused by the safety check
	Duration          time.Duration `json:"duration_ns"`
	// DestManifest is the destination's file list re-taken after the transfer:
	// the convergence proof a rerun diffs against. Nil if the post-sync
	// manifest could not be taken.
	DestManifest *Manifest `json:"-"`
}

// Sync makes the destination docroot match the source over a manifest-driven
// tar-pipe relayed through this process: it ensures the destination root
// exists, takes a size/mtime manifest of each end (same excludes on both),
// diffs them, and streams the new-and-changed files source→destination as a
// single tar pipe (created from an explicit file list on the source, extracted
// under the destination root). Destination-only files are removed only when
// Delete is set; otherwise they are counted and reported, never touched.
//
// Atomicity/interruption: the relay is not atomic. A tar extraction writes
// files in stream order and does not stage-then-rename, so an interrupted
// transfer can leave the last file partial and later files absent. That is
// safe because it is convergent, not because it is transactional: the partial
// file has the wrong size/mtime and the missing files are absent, so the next
// Sync's manifest diff re-sends exactly those. Do not read this as an
// all-or-nothing guarantee — it is a "rerun converges" guarantee.
func Sync(ctx context.Context, src, dst Endpoint, excludes []string, opts Options, progress func(string)) (*SyncStats, error) {
	start := time.Now()
	if opts.MaxRmBytes <= 0 {
		opts.MaxRmBytes = defaultMaxRmBytes
	}
	stats := &SyncStats{Compressed: opts.Compress}

	// Ensure the destination docroot exists before anything inspects it.
	if res, err := dst.Conn.Run(ctx, "mkdir -p "+ssh.ShellQuote(dst.Root)); err != nil {
		return stats, err
	} else if res.ExitCode != 0 {
		return stats, fmt.Errorf("creating destination root %s (exit %d): %s", dst.Root, res.ExitCode, ssh.FirstLine(res.Stderr))
	}

	// Manifest each end. Excludes are applied identically so the diff compares
	// like with like.
	note(progress, "source: building file manifest of %s", src.Root)
	srcManifest, err := TakeManifest(ctx, src.Conn, src.Root, excludes)
	if err != nil {
		return stats, fmt.Errorf("source manifest: %w", err)
	}
	note(progress, "destination: building file manifest of %s", dst.Root)
	dstManifest, err := TakeManifest(ctx, dst.Conn, dst.Root, excludes)
	if err != nil {
		return stats, fmt.Errorf("destination manifest: %w", err)
	}

	// Diff with the destination as the baseline: Added+Changed is what the
	// source has that the destination lacks or differs on (what to send);
	// Removed is destination-only (deletion candidates).
	d := Diff(dstManifest, srcManifest)
	if !srcManifest.Complete || !dstManifest.Complete {
		stats.Degraded = true
		note(progress, "warning: no size/mtime available (GNU find missing on %s) — files modified in place will NOT be re-sent, only new files; verify changed content by hand",
			degradedEnds(srcManifest, dstManifest, src, dst))
	}
	if opts.Delete && srcManifest.Pruned {
		// The source listing skipped unreadable entries, so "absent from the
		// source" is unproven — deleting on that evidence could destroy
		// already-migrated files. Fall back to additive for this run.
		opts.Delete = false
		note(progress, "source: file listing was incomplete (find could not read some entries) — skipping --delete this run")
	}
	send := make([]FileEntry, 0, len(d.Added)+len(d.Changed))
	send = append(send, d.Added...)
	send = append(send, d.Changed...)

	// Transfer the needed files as one tar pipe.
	if len(send) > 0 {
		for _, e := range send {
			stats.BytesSent += e.Size
		}
		note(progress, "sending %d files (%s)", len(send), units.HumanBytes(stats.BytesSent))
		wire, err := relay(ctx, src, dst, send, opts)
		stats.WireBytes = wire
		stats.FilesSent = len(send)
		if err != nil {
			stats.Duration = time.Since(start)
			return stats, err
		}
	} else {
		note(progress, "nothing to send — the destination already matches the source")
	}

	// Destination-only files: delete opt-in, always safety-checked.
	if err := reconcileDeletions(ctx, dst, d.Removed, opts, progress, stats); err != nil {
		stats.Duration = time.Since(start)
		return stats, err
	}

	// Re-take the destination manifest as the convergence proof, and persist
	// it when the caller asked for it. A failure here does not undo the
	// transfer — report it and carry on.
	if post, err := TakeManifest(ctx, dst.Conn, dst.Root, excludes); err != nil {
		note(progress, "destination: could not re-read the file manifest after sync: %v", err)
	} else {
		stats.DestManifest = post
		if opts.DestManifestPath != "" {
			if err := SaveManifest(post, opts.DestManifestPath); err != nil {
				note(progress, "destination: could not persist the post-sync manifest: %v", err)
			}
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// relay streams the send list source→destination: the source runs tar reading
// the file list from its stdin and writing the archive to its stdout, which
// this process pipes straight into the destination's extracting tar. Nothing
// buffers a site in memory — io.Pipe is unbuffered and back-pressured by the
// destination's read rate. Returns the number of (possibly compressed) bytes
// that crossed the wire.
func relay(ctx context.Context, src, dst Endpoint, send []FileEntry, opts Options) (int64, error) {
	list := fileList(send, opts.NullList)

	pr, pw := io.Pipe()
	wire := &countingWriter{}

	type outcome struct {
		res ssh.Result
		err error
	}
	srcDone := make(chan outcome, 1)
	go func() {
		res, err := src.Conn.StreamPipe(ctx, srcTarCmd(src.Root, opts.NullList, opts.Compress),
			bytes.NewReader(list), io.MultiWriter(pw, wire))
		// Signal EOF (or the failure) to the destination's read side so its
		// tar sees the end of the archive.
		pw.CloseWithError(err)
		srcDone <- outcome{res, err}
	}()

	dstRes, dstErr := dst.Conn.StreamPipe(ctx, destTarCmd(dst.Root, opts.Compress), pr, io.Discard)
	// If the destination ended early (error), unblock the source's blocked
	// write into the pipe.
	pr.CloseWithError(dstErr)
	srcOut := <-srcDone

	// The source pipeline ends in `| gzip` when compressing, so its exit code
	// is gzip's, not tar's — a truncated source tar cannot be seen here and is
	// instead caught by the destination's tar and, failing that, by the next
	// run's manifest diff. The destination outcome is reported first: when the
	// extract dies, the pipe close makes the still-streaming source fail with
	// a closed-pipe transport error that would otherwise mask the extract's
	// stderr (the actual diagnosis, e.g. "No space left on device").
	srcFail := tarOK(srcOut.res, srcOut.err, "source tar", true)
	// No exit-1 tolerance for the extract: GNU tar reserves 1 for warnings
	// that cannot occur extracting, and bsdtar uses it for fatal errors
	// including a truncated archive — the relay's truncation signal.
	dstFail := tarOK(dstRes, dstErr, "destination extract", false)
	switch {
	case dstFail != nil && srcFail != nil:
		return wire.n, errors.Join(dstFail, srcFail)
	case dstFail != nil:
		return wire.n, dstFail
	default:
		return wire.n, srcFail
	}
}

// reconcileDeletions removes destination-only files when Delete is set. Every
// path is verified relative and inside the destination root first; anything
// suspicious is refused (counted, never deleted). Deletions are reported
// before they happen.
func reconcileDeletions(ctx context.Context, dst Endpoint, destOnly []string, opts Options, progress func(string), stats *SyncStats) error {
	if len(destOnly) == 0 {
		return nil
	}
	var safe []string
	for _, p := range destOnly {
		if withinRoot(p) {
			safe = append(safe, p)
		} else {
			stats.UnsafePaths++
			note(progress, "destination: refusing to delete unsafe path %q", p)
		}
	}

	if !opts.Delete {
		// Additive mode: report, do not touch. Everything destination-only
		// remains (the safe ones plus the refused ones).
		stats.DestOnlyRemaining = len(destOnly)
		if len(destOnly) > 0 {
			note(progress, "destination: %d destination-only files left in place (pass --delete to remove)", len(destOnly))
		}
		return nil
	}

	if len(safe) > 0 {
		note(progress, "destination: deleting %d files", len(safe))
		for _, cmd := range rmCommands(dst.Root, safe, opts.MaxRmBytes) {
			res, err := dst.Conn.Run(ctx, cmd)
			if err != nil {
				return err
			}
			if res.ExitCode != 0 {
				return fmt.Errorf("deleting destination-only files (exit %d): %s", res.ExitCode, ssh.FirstLine(res.Stderr))
			}
		}
		stats.FilesDeleted = len(safe)
	}
	// Only the refused paths remain destination-only after a --delete pass.
	stats.DestOnlyRemaining = stats.UnsafePaths
	return nil
}

// srcTarCmd builds the source pipeline: tar reads the file list from stdin
// (NUL- or newline-delimited) and writes the archive to stdout, gzip'd on the
// wire when asked. `cd root` makes the listed relative paths resolve and keeps
// them relative in the archive so they extract under the destination root.
// mtimes are stored by default (no flag needed); the destination restores them
// as long as it does not extract with --touch.
func srcTarCmd(root string, null, compress bool) string {
	var b strings.Builder
	b.WriteString("cd " + ssh.ShellQuote(root) + " && tar -c")
	if null {
		b.WriteString(" --null")
	}
	b.WriteString(" -T - -f -")
	if compress {
		b.WriteString(" | gzip")
	}
	return b.String()
}

// destTarCmd builds the destination pipeline: optionally gunzip, then extract
// under root. -p preserves permissions; mtimes are restored by tar's default
// (no --touch), which is what keeps a second Sync a zero delta. Extraction is
// not staged: files land in stream order (see the Sync doc on interruption).
func destTarCmd(root string, compress bool) string {
	var b strings.Builder
	if compress {
		b.WriteString("gzip -dc | ")
	}
	b.WriteString("tar -x -p -f - -C " + ssh.ShellQuote(root))
	return b.String()
}

// fileList renders the send set as a delimited byte slice for the source tar's
// stdin. Terminator style (a separator after every path) is correct for both
// NUL (-T - --null) and newline (-T -) modes. Only paths cross this channel,
// so it is bounded by file count, not file data. Every entry is prefixed with
// ./ — in newline mode tar would otherwise read a dash-prefixed filename
// (plantable by anyone who can write to the source docroot) as an option,
// which for names like --checkpoint-action=exec=… is remote command execution.
func fileList(entries []FileEntry, null bool) []byte {
	sep := byte('\n')
	if null {
		sep = 0
	}
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString("./")
		b.WriteString(e.Path)
		b.WriteByte(sep)
	}
	return b.Bytes()
}

// withinRoot reports whether a manifest-relative path is safe to delete under
// the destination root: non-empty, not absolute, and with no `..` that would
// escape the root. Manifest paths are already root-relative and byte-exact, so
// a path failing this is anomalous and refused rather than normalized.
func withinRoot(p string) bool {
	if p == "" || path.IsAbs(p) {
		return false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return false
	}
	return true
}

// rmCommands batches `rm -f -- <paths>` so a single command line stays under
// maxLen. Each path is joined to the root and shell-quoted; `--` stops rm from
// treating a leading-dash filename as a flag.
func rmCommands(root string, paths []string, maxLen int) []string {
	const prefix = "rm -f --"
	var cmds []string
	var b strings.Builder
	b.WriteString(prefix)
	for _, p := range paths {
		arg := " " + ssh.ShellQuote(path.Join(root, p))
		if b.Len() > len(prefix) && b.Len()+len(arg) > maxLen {
			cmds = append(cmds, b.String())
			b.Reset()
			b.WriteString(prefix)
		}
		b.WriteString(arg)
	}
	if b.Len() > len(prefix) {
		cmds = append(cmds, b.String())
	}
	return cmds
}

// tarOK maps a relay endpoint's outcome to an error. tolerateWarnings accepts
// exit 1 — correct only for a GNU tar creating from a live site ("file
// changed / unreadable while reading"), the same way the throughput sample
// does; extraction must stay strict because bsdtar reports fatal errors,
// truncated archives included, with exit 1. A transport error is always
// honest failure.
func tarOK(res ssh.Result, err error, what string, tolerateWarnings bool) error {
	if err != nil {
		return err
	}
	if res.ExitCode == 0 || (tolerateWarnings && res.ExitCode == 1) {
		return nil
	}
	return fmt.Errorf("%s failed (exit %d): %s", what, res.ExitCode, ssh.FirstLine(res.Stderr))
}

// degradedEnds names the endpoint(s) whose manifest lacks size/mtime, for the
// degradation warning.
func degradedEnds(srcM, dstM *Manifest, src, dst Endpoint) string {
	switch {
	case !srcM.Complete && !dstM.Complete:
		return src.Target + " and " + dst.Target
	case !srcM.Complete:
		return src.Target
	default:
		return dst.Target
	}
}

// DestManifestFilename derives the persisted destination manifest's basename.
// It mirrors ManifestFilename but keys on the destination identity and marks
// the file "dest-" so a destination baseline never masquerades as the source's
// last-run manifest in the same directory.
func DestManifestFilename(target, root string) string {
	return "dest-" + ManifestFilename(target, root)
}

// countingWriter tallies bytes written through it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// note reports a formatted progress line when a callback was given.
func note(progress func(string), format string, a ...any) {
	if progress != nil {
		progress(fmt.Sprintf(format, a...))
	}
}
