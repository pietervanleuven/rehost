package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/placeholder/rehost/internal/ssh"
)

// fakeStreamer emits chunks until the writer aborts, mimicking a session
// whose stdout copy stops on a writer error.
type fakeStreamer struct {
	chunk   []byte
	chunks  int
	res     ssh.Result
	lastCmd string
}

func (f *fakeStreamer) Stream(_ context.Context, cmd string, w io.Writer) (ssh.Result, error) {
	f.lastCmd = cmd
	for i := 0; i < f.chunks; i++ {
		if _, err := w.Write(f.chunk); err != nil {
			return f.res, err
		}
	}
	return f.res, nil
}

func TestThroughputCapped(t *testing.T) {
	s := &fakeStreamer{chunk: bytes.Repeat([]byte("x"), 1<<20), chunks: 100}
	stats, err := Throughput(context.Background(), s, "/home/u/site", []string{"wp-content/cache"}, 4<<20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Capped || stats.Bytes < 4<<20 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.BytesPerSec() <= 0 {
		t.Error("rate should be positive")
	}
	if !strings.Contains(s.lastCmd, "--exclude='./wp-content/cache'") {
		t.Errorf("exclude missing from tar command: %s", s.lastCmd)
	}
	if !strings.Contains(s.lastCmd, "| gzip") {
		t.Errorf("stream should be compressed: %s", s.lastCmd)
	}
}

func TestThroughputSmallSiteCompletes(t *testing.T) {
	s := &fakeStreamer{chunk: bytes.Repeat([]byte("y"), 100<<10), chunks: 1}
	stats, err := Throughput(context.Background(), s, "/site", nil, DefaultByteCap, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Capped || stats.Bytes != 100<<10 {
		t.Errorf("small site should stream to completion: %+v", stats)
	}
}

func TestThroughputTarFailure(t *testing.T) {
	s := &fakeStreamer{chunks: 0, res: ssh.Result{ExitCode: 127, Stderr: "sh: tar: not found\n"}}
	_, err := Throughput(context.Background(), s, "/site", nil, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "tar") {
		t.Errorf("tar failure should surface, got %v", err)
	}
}

func TestThroughputTransportFailure(t *testing.T) {
	failing := streamerFunc(func(context.Context, string, io.Writer) (ssh.Result, error) {
		return ssh.Result{}, errors.New("connection lost")
	})
	if _, err := Throughput(context.Background(), failing, "/site", nil, 0, 0); err == nil {
		t.Error("transport failure must propagate")
	}
}

type streamerFunc func(ctx context.Context, cmd string, w io.Writer) (ssh.Result, error)

func (f streamerFunc) Stream(ctx context.Context, cmd string, w io.Writer) (ssh.Result, error) {
	return f(ctx, cmd, w)
}
