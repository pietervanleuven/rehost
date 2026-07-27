package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/ssh"
)

// fakeStreamer writes canned payload bytes to the stream writer.
type fakeStreamer struct {
	payload []byte
	res     ssh.Result
	err     error
	lastCmd string
}

func (f *fakeStreamer) Stream(_ context.Context, cmd string, w io.Writer) (ssh.Result, error) {
	f.lastCmd = cmd
	if f.err != nil {
		return ssh.Result{}, f.err
	}
	if _, err := w.Write(f.payload); err != nil {
		return f.res, err
	}
	return f.res, nil
}

func gzipped(t *testing.T, sql string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(sql)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const completeDump = `-- MySQL dump 10.13
CREATE TABLE wp_posts (id int);
INSERT INTO wp_posts VALUES (1);
CREATE TABLE wp_users (id int);
-- Dump completed on 2026-07-27
`

func TestDumpVerified(t *testing.T) {
	s := &fakeStreamer{payload: gzipped(t, completeDump)}
	var out bytes.Buffer
	stats, err := Dump(context.Background(), s, &Credentials{Name: "wpdb", User: "u", Password: "p"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.FooterOK || stats.Tables != 2 || stats.Bytes != int64(len(completeDump)) {
		t.Errorf("stats = %+v", stats)
	}
	if stats.CompressedBytes != int64(out.Len()) {
		t.Errorf("compressed bytes %d != written %d", stats.CompressedBytes, out.Len())
	}
	if !bytes.Equal(out.Bytes(), s.payload) {
		t.Error("the gzipped payload must reach the writer unmodified")
	}
	// The password may only travel after the heredoc marker.
	argv, _, _ := strings.Cut(s.lastCmd, "<<'REHOST_CNF'")
	if strings.Contains(argv, "password") {
		t.Errorf("credentials leaked into argv: %s", argv)
	}
	if !strings.Contains(s.lastCmd, "--no-tablespaces") || !strings.Contains(s.lastCmd, "--single-transaction") {
		t.Errorf("dump flags missing: %s", s.lastCmd)
	}
}

func TestDumpTruncatedFailsVerification(t *testing.T) {
	full := gzipped(t, completeDump)
	s := &fakeStreamer{payload: full[:len(full)/2]} // cut mid-stream
	var out bytes.Buffer
	stats, err := Dump(context.Background(), s, &Credentials{Name: "wpdb"}, &out)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("truncated dump must fail verification, got err=%v stats=%+v", err, stats)
	}
}

func TestDumpMissingFooterFails(t *testing.T) {
	s := &fakeStreamer{payload: gzipped(t, "-- MySQL dump\nCREATE TABLE t (id int);\n")}
	if _, err := Dump(context.Background(), s, &Credentials{Name: "d"}, io.Discard); err == nil {
		t.Error("dump without completion footer must fail")
	}
}

func TestDumpRemoteFailure(t *testing.T) {
	s := &fakeStreamer{payload: nil, res: ssh.Result{ExitCode: 2, Stderr: "mysqldump: Got error: 1044 access denied with hunter2\n"}}
	_, err := Dump(context.Background(), s, &Credentials{Name: "d", Password: "hunter2"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "1044") {
		t.Fatalf("remote failure should surface stderr, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaked the password: %v", err)
	}
}

func TestDumpTransportFailure(t *testing.T) {
	s := &fakeStreamer{err: errors.New("connection lost")}
	if _, err := Dump(context.Background(), s, &Credentials{Name: "d"}, io.Discard); err == nil {
		t.Error("transport failure must propagate")
	}
}
