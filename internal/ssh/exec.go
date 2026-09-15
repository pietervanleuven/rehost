package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	cryptossh "golang.org/x/crypto/ssh"

	"github.com/pietervanleuven/rehost/internal/ssh/remote"
)

// Result is the outcome of one remote command. The type lives in the remote
// subpackage so consumers of command output need not depend on the dial
// stack; the alias keeps the root API complete and the types identical.
type Result = remote.Result

// DefaultRunTimeout bounds every buffered command (Run): those are bounded
// by intent — probes, listings, size measurements — and a hung one must not
// hang a non-interactive run forever. Streamed transfers (Stream) carry no
// default deadline: dumps and tar pipes legitimately run for hours.
const DefaultRunTimeout = 10 * time.Minute

// Run executes one command in a fresh session. A non-zero exit status is not
// a Go error — capability probes expect commands to fail; only transport and
// session failures are returned as errors. Commands are cut off after
// DefaultRunTimeout (or the caller's earlier deadline).
func (c *Client) Run(ctx context.Context, cmd string) (Result, error) {
	ctx, cancel := runContext(ctx)
	defer cancel()
	var stdout bytes.Buffer
	res, err := c.Stream(ctx, cmd, &stdout)
	res.Stdout = stdout.String()
	return res, err
}

// runContext applies DefaultRunTimeout; a caller deadline that is already
// sooner wins (context.WithTimeout keeps the earlier of the two).
func runContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, DefaultRunTimeout)
}

// FirstLine returns the trimmed first line of command output; see
// remote.FirstLine.
func FirstLine(s string) string { return remote.FirstLine(s) }

// ShellQuote makes s survive a POSIX shell unchanged; see remote.ShellQuote.
func ShellQuote(s string) string { return remote.ShellQuote(s) }

// Stream executes cmd, writing its stdout to w as the data arrives — for
// payloads (database dumps, tar streams) that must never be buffered whole.
// Result.Stdout stays empty; stderr is captured for diagnostics. Cancelling
// ctx closes the session (how a capped measurement stops a stream); the
// bytes written before that remain valid.
func (c *Client) Stream(ctx context.Context, cmd string, w io.Writer) (Result, error) {
	return c.StreamPipe(ctx, cmd, nil, w)
}

// StreamPipe is Stream with the command's stdin fed from a reader — the
// primitive for relays that pipe bytes *into* a remote process (a tar
// extraction, a NUL-delimited file list). A nil stdin leaves the remote
// stdin closed, which is exactly Stream.
func (c *Client) StreamPipe(ctx context.Context, cmd string, stdin io.Reader, w io.Writer) (Result, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("opening session on %s: %w", c.Config.Host, err)
	}
	defer func() { _ = sess.Close() }()

	var stderr bytes.Buffer
	sess.Stdin = stdin
	sess.Stdout = w
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = sess.Close()
		// sess.Close does not interrupt a Read blocked in the caller's stdin
		// reader, so the session goroutine may never finish — bound the wait
		// instead of hanging the cancellation forever. An abandoned goroutine
		// exits when its stdin read eventually returns; done is buffered, so
		// its send never blocks. On timeout the goroutine still owns stderr,
		// so it is not read.
		select {
		case <-done:
			return Result{Stderr: stderr.String(), ExitCode: -1}, ctx.Err()
		case <-time.After(5 * time.Second):
			return Result{ExitCode: -1}, ctx.Err()
		}
	case err := <-done:
		res := Result{Stderr: stderr.String()}
		var exitErr *cryptossh.ExitError
		var missingErr *cryptossh.ExitMissingError
		switch {
		case err == nil:
			return res, nil
		case errors.As(err, &exitErr):
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		case errors.As(err, &missingErr):
			res.ExitCode = -1
			return res, nil
		default:
			res.ExitCode = -1
			return res, fmt.Errorf("running %q on %s: %w", cmd, c.Config.Host, err)
		}
	}
}
