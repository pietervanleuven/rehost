package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	cryptossh "golang.org/x/crypto/ssh"
)

// Result is the outcome of one remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int // -1 when the session ended without reporting a status
}

// Run executes one command in a fresh session. A non-zero exit status is not
// a Go error — capability probes expect commands to fail; only transport and
// session failures are returned as errors.
func (c *Client) Run(ctx context.Context, cmd string) (Result, error) {
	var stdout bytes.Buffer
	res, err := c.Stream(ctx, cmd, &stdout)
	res.Stdout = stdout.String()
	return res, err
}

// Stream executes cmd, writing its stdout to w as the data arrives — for
// payloads (database dumps, tar streams) that must never be buffered whole.
// Result.Stdout stays empty; stderr is captured for diagnostics. Cancelling
// ctx closes the session (how a capped measurement stops a stream); the
// bytes written before that remain valid.
func (c *Client) Stream(ctx context.Context, cmd string, w io.Writer) (Result, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("opening session on %s: %w", c.Config.Host, err)
	}
	defer sess.Close()

	var stderr bytes.Buffer
	sess.Stdout = w
	sess.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		sess.Close()
		<-done
		return Result{Stderr: stderr.String(), ExitCode: -1}, ctx.Err()
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
