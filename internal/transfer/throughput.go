// Package transfer moves site files between hosts. Phase 2 content: a
// tar-pipe throughput measurement for the dry run; the real sync engine
// (rsync / manifest-driven tar / SFTP fallbacks) lands in Phase 3.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/placeholder/rehost/internal/db"
	"github.com/placeholder/rehost/internal/ssh"
)

// ThroughputStats is what a capped measurement observed.
type ThroughputStats struct {
	Bytes    int64         `json:"bytes"` // compressed bytes received
	Duration time.Duration `json:"duration_ns"`
	Capped   bool          `json:"capped"` // stopped at the byte cap, not end of data
}

// BytesPerSec is the observed compressed transfer rate.
func (s ThroughputStats) BytesPerSec() float64 {
	if s.Duration <= 0 {
		return 0
	}
	return float64(s.Bytes) / s.Duration.Seconds()
}

// Defaults bound the dry-run cost on slow shared hosts.
const (
	DefaultByteCap = 32 << 20 // 32 MiB compressed is enough for a rate
	DefaultTimeCap = 15 * time.Second
)

// errCapReached stops the stream once enough bytes arrived.
var errCapReached = errors.New("measurement cap reached")

// Throughput streams a gzipped tar of root (honoring excludes) and measures
// the achievable rate, stopping after byteCap bytes or timeCap. It answers
// two dry-run questions at once: does a tar pipe work on this host, and how
// long would the real copy roughly take.
func Throughput(ctx context.Context, s db.Streamer, root string, excludes []string, byteCap int64, timeCap time.Duration) (*ThroughputStats, error) {
	if byteCap <= 0 {
		byteCap = DefaultByteCap
	}
	if timeCap <= 0 {
		timeCap = DefaultTimeCap
	}
	ctx, cancel := context.WithTimeout(ctx, timeCap)
	defer cancel()

	w := &cappedWriter{cap: byteCap}
	start := time.Now()
	res, err := s.Stream(ctx, tarCmd(root, excludes), w)
	stats := &ThroughputStats{Bytes: w.n, Duration: time.Since(start)}

	switch {
	case errors.Is(err, errCapReached) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		stats.Capped = true
		return stats, nil // stopped on purpose with a valid sample
	case err != nil:
		return stats, err
	case res.ExitCode == 0 || res.ExitCode == 1:
		// 1 is GNU tar's "some files changed/were unreadable while
		// reading" — noise on a live site; the stream itself is a valid
		// sample. Anything worse means the pipeline died mid-run and the
		// sample cannot be trusted, however many bytes arrived.
		return stats, nil
	default:
		return stats, fmt.Errorf("tar failed on the source (exit %d): %s", res.ExitCode, ssh.FirstLine(res.Stderr))
	}
}

// tarCmd builds the remote pipeline. Excludes use the portable
// --exclude=pattern form (GNU and busybox tar).
func tarCmd(root string, excludes []string) string {
	var b strings.Builder
	b.WriteString("cd " + ssh.ShellQuote(root) + " && tar -cf -")
	for _, e := range excludes {
		b.WriteString(" --exclude=" + ssh.ShellQuote("./"+e))
	}
	b.WriteString(" . | gzip")
	return b.String()
}

// cappedWriter counts bytes and aborts the stream at the cap.
type cappedWriter struct {
	n   int64
	cap int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.n >= c.cap {
		return len(p), errCapReached
	}
	return len(p), nil
}

var _ io.Writer = (*cappedWriter)(nil)
