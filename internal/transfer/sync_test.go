package transfer

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// fakeConn is a scripted endpoint: it answers mkdir/find/rm on Run and records
// the tar relay on StreamPipe. Source and destination use separate instances.
type fakeConn struct {
	printf     string // GNU find -printf listing this end returns for a manifest
	printfExit int
	mkdirExit  int
	rmExit     int

	streamOut []byte // bytes this end writes to the relay (the "archive")
	streamRes ssh.Result
	streamErr error

	mu         sync.Mutex
	runCmds    []string
	streamCmds []string
	stdin      [][]byte
}

func (f *fakeConn) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.mu.Lock()
	f.runCmds = append(f.runCmds, cmd)
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "mkdir -p"):
		return ssh.Result{ExitCode: f.mkdirExit}, nil
	case strings.HasPrefix(cmd, "rm -f"):
		return ssh.Result{ExitCode: f.rmExit}, nil
	case strings.Contains(cmd, "-printf"):
		return ssh.Result{Stdout: f.printf, ExitCode: f.printfExit}, nil
	default: // find fallbacks (print0/print) — unused; report empty clean run
		return ssh.Result{}, nil
	}
}

func (f *fakeConn) StreamPipe(_ context.Context, cmd string, stdin io.Reader, w io.Writer) (ssh.Result, error) {
	f.mu.Lock()
	f.streamCmds = append(f.streamCmds, cmd)
	f.mu.Unlock()
	if stdin != nil {
		data, _ := io.ReadAll(stdin) // drain: the source list, or the dest's archive
		f.mu.Lock()
		f.stdin = append(f.stdin, data)
		f.mu.Unlock()
	}
	if len(f.streamOut) > 0 {
		if _, err := w.Write(f.streamOut); err != nil {
			return f.streamRes, err
		}
	}
	return f.streamRes, f.streamErr
}

func (f *fakeConn) firstStdin() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stdin) == 0 {
		return nil
	}
	return f.stdin[0]
}

func (f *fakeConn) ranAny(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.runCmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// printfLine renders one GNU find -printf record: "<size> <mtime> <path>\0".
func printfLine(size, mtime int, path string) string {
	return itoa(size) + " " + itoa(mtime) + ".0 " + path + "\x00"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func endpoints(src, dst *fakeConn) (Endpoint, Endpoint) {
	return Endpoint{Conn: src, Root: "/home/u/site", Target: "u@source"},
		Endpoint{Conn: dst, Root: "/var/www/site", Target: "u@dest"}
}

func TestSyncSendsDelta(t *testing.T) {
	src := &fakeConn{
		printf:    printfLine(10, 100, "a.txt") + printfLine(20, 200, "b.txt") + printfLine(30, 300, "c.txt"),
		streamOut: []byte("ARCHIVE-BYTES"),
	}
	dst := &fakeConn{
		// a.txt identical; b.txt has a different mtime (changed); c.txt absent.
		printf: printfLine(10, 100, "a.txt") + printfLine(20, 999, "b.txt"),
	}
	s, d := endpoints(src, dst)

	var phases []string
	stats, err := Sync(context.Background(), s, d, []string{"cache"},
		Options{Compress: true, NullList: true}, func(m string) { phases = append(phases, m) })
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesSent != 2 || stats.BytesSent != 50 {
		t.Errorf("stats = %+v (want 2 files, 50 bytes)", stats)
	}
	if stats.WireBytes != int64(len(src.streamOut)) {
		t.Errorf("WireBytes = %d, want %d", stats.WireBytes, len(src.streamOut))
	}
	// Only the added + changed files travel over the source stdin, NUL-joined.
	list := string(src.firstStdin())
	if list != "c.txt\x00b.txt\x00" {
		t.Errorf("send list = %q", list)
	}
	if len(src.streamCmds) != 1 || !strings.Contains(src.streamCmds[0], "tar -c --null -T - -f -") ||
		!strings.Contains(src.streamCmds[0], "| gzip") {
		t.Errorf("source tar cmd = %v", src.streamCmds)
	}
	if len(dst.streamCmds) != 1 || !strings.Contains(dst.streamCmds[0], "gzip -dc | tar -x -p -f - -C '/var/www/site'") {
		t.Errorf("dest untar cmd = %v", dst.streamCmds)
	}
	if !dst.ranAny("mkdir -p '/var/www/site'") {
		t.Error("destination root should be ensured with mkdir -p")
	}
	if stats.DestManifest == nil {
		t.Error("post-sync destination manifest should be returned")
	}
	if !hasPhase(phases, "source: building file manifest") || !hasPhase(phases, "sending 2 files") {
		t.Errorf("phases = %v", phases)
	}
}

func TestSyncUncompressedCmds(t *testing.T) {
	src := &fakeConn{printf: printfLine(1, 1, "a"), streamOut: []byte("x")}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	if _, err := Sync(context.Background(), s, d, nil, Options{Compress: false, NullList: true}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src.streamCmds[0], "gzip") {
		t.Errorf("uncompressed source should not pipe gzip: %s", src.streamCmds[0])
	}
	if strings.Contains(dst.streamCmds[0], "gzip") {
		t.Errorf("uncompressed dest should not gunzip: %s", dst.streamCmds[0])
	}
}

func TestSyncEmptyDeltaSkipsTransfer(t *testing.T) {
	// Identical manifests: the convergence property at the logic level — a
	// second run has nothing to send.
	listing := printfLine(10, 100, "a.txt") + printfLine(20, 200, "b.txt")
	src := &fakeConn{printf: listing, streamOut: []byte("SHOULD-NOT-BE-SENT")}
	dst := &fakeConn{printf: listing}
	s, d := endpoints(src, dst)

	stats, err := Sync(context.Background(), s, d, nil, Options{Compress: true, NullList: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesSent != 0 || stats.WireBytes != 0 {
		t.Errorf("empty delta should transfer nothing: %+v", stats)
	}
	if len(src.streamCmds) != 0 {
		t.Errorf("no tar session should open for an empty delta: %v", src.streamCmds)
	}
}

func TestSyncNullFilenamesSurvive(t *testing.T) {
	src := &fakeConn{
		printf:    printfLine(1, 1, "weird\nname.txt") + printfLine(2, 2, "a b.jpg"),
		streamOut: []byte("z"),
	}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	if _, err := Sync(context.Background(), s, d, nil, Options{NullList: true}, nil); err != nil {
		t.Fatal(err)
	}
	list := string(src.firstStdin())
	if !strings.Contains(list, "weird\nname.txt\x00") || !strings.Contains(list, "a b.jpg\x00") {
		t.Errorf("NUL list must keep odd filenames byte-exact: %q", list)
	}
	if !strings.Contains(src.streamCmds[0], "--null") {
		t.Errorf("NUL mode should use --null: %s", src.streamCmds[0])
	}
}

func TestSyncNewlineListWhenNotNull(t *testing.T) {
	src := &fakeConn{printf: printfLine(1, 1, "a.txt") + printfLine(2, 2, "b.txt"), streamOut: []byte("z")}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	if _, err := Sync(context.Background(), s, d, nil, Options{NullList: false}, nil); err != nil {
		t.Fatal(err)
	}
	if list := string(src.firstStdin()); list != "a.txt\nb.txt\n" {
		t.Errorf("non-NUL list should be newline-delimited: %q", list)
	}
	if strings.Contains(src.streamCmds[0], "--null") {
		t.Errorf("non-NUL mode must not pass --null: %s", src.streamCmds[0])
	}
}

func TestSyncDeleteMode(t *testing.T) {
	src := &fakeConn{printf: printfLine(10, 100, "a.txt")}
	dst := &fakeConn{
		// a.txt matches; x/gone and sub/y are destination-only; ../evil is unsafe.
		printf: printfLine(10, 100, "a.txt") + printfLine(1, 1, "x/gone") +
			printfLine(2, 2, "sub/y") + printfLine(3, 3, "../evil"),
	}
	s, d := endpoints(src, dst)

	var phases []string
	stats, err := Sync(context.Background(), s, d, nil, Options{Delete: true}, func(m string) { phases = append(phases, m) })
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesDeleted != 2 || stats.UnsafePaths != 1 || stats.DestOnlyRemaining != 1 {
		t.Errorf("delete stats = %+v (want 2 deleted, 1 unsafe, 1 remaining)", stats)
	}
	if !dst.ranAny("rm -f -- ") || !dst.ranAny("'/var/www/site/x/gone'") || !dst.ranAny("'/var/www/site/sub/y'") {
		t.Errorf("rm should target the joined, quoted dest-only paths: %v", dst.runCmds)
	}
	if dst.ranAny("evil") {
		t.Errorf("unsafe path must never reach an rm: %v", dst.runCmds)
	}
	if !hasPhase(phases, "refusing to delete unsafe path") || !hasPhase(phases, "deleting 2 files") {
		t.Errorf("phases = %v", phases)
	}
}

func TestSyncAdditiveKeepsDestOnly(t *testing.T) {
	src := &fakeConn{printf: printfLine(10, 100, "a.txt")}
	dst := &fakeConn{printf: printfLine(10, 100, "a.txt") + printfLine(1, 1, "x") + printfLine(2, 2, "y")}
	s, d := endpoints(src, dst)

	stats, err := Sync(context.Background(), s, d, nil, Options{Delete: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesDeleted != 0 || stats.DestOnlyRemaining != 2 {
		t.Errorf("additive stats = %+v (want 0 deleted, 2 remaining)", stats)
	}
	if dst.ranAny("rm ") {
		t.Errorf("additive mode must not delete: %v", dst.runCmds)
	}
}

func TestSyncDestExtractFailureSurfaces(t *testing.T) {
	src := &fakeConn{printf: printfLine(1, 1, "a"), streamOut: []byte("data")}
	dst := &fakeConn{streamRes: ssh.Result{ExitCode: 2, Stderr: "tar: Unexpected EOF in archive\n"}}
	s, d := endpoints(src, dst)
	_, err := Sync(context.Background(), s, d, nil, Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "destination extract failed (exit 2)") {
		t.Errorf("truncated extraction should surface, got %v", err)
	}
}

func TestSyncSourceTransportFailureSurfaces(t *testing.T) {
	src := &fakeConn{printf: printfLine(1, 1, "a"), streamErr: errors.New("connection lost")}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	_, err := Sync(context.Background(), s, d, nil, Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("source transport failure should surface, got %v", err)
	}
}

func TestSyncToleratesTarExit1(t *testing.T) {
	// GNU tar exit 1 on the source (a file changed while reading) is noise on a
	// live site, not a failure.
	src := &fakeConn{printf: printfLine(1, 1, "a"), streamOut: []byte("d"),
		streamRes: ssh.Result{ExitCode: 1, Stderr: "tar: ./x: file changed as we read it\n"}}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	if _, err := Sync(context.Background(), s, d, nil, Options{}, nil); err != nil {
		t.Errorf("tar exit 1 should be tolerated: %v", err)
	}
}

func TestSyncMkdirFailureSurfaces(t *testing.T) {
	src := &fakeConn{printf: printfLine(1, 1, "a")}
	dst := &fakeConn{mkdirExit: 1}
	s, d := endpoints(src, dst)
	_, err := Sync(context.Background(), s, d, nil, Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "creating destination root") {
		t.Errorf("mkdir failure should abort the sync, got %v", err)
	}
}

func TestSyncSourceManifestErrorSurfaces(t *testing.T) {
	// A find killed mid-listing (exit >1 with partial output) must not be
	// treated as an authoritative manifest.
	src := &fakeConn{printf: printfLine(1, 1, "a"), printfExit: 137}
	dst := &fakeConn{}
	s, d := endpoints(src, dst)
	_, err := Sync(context.Background(), s, d, nil, Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "source manifest") {
		t.Errorf("source manifest failure should surface, got %v", err)
	}
}

func TestSyncPersistsDestManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DestManifestFilename("u@dest", "/var/www/site"))
	src := &fakeConn{printf: printfLine(1, 1, "a"), streamOut: []byte("d")}
	dst := &fakeConn{printf: printfLine(1, 1, "a")}
	s, d := endpoints(src, dst)
	stats, err := Sync(context.Background(), s, d, nil, Options{DestManifestPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil || loaded == nil {
		t.Fatalf("post-sync manifest should be persisted: %v, %v", loaded, err)
	}
	if stats.DestManifest == nil || len(loaded.Files) != 1 {
		t.Errorf("persisted manifest = %+v", loaded)
	}
}

func TestRmCommandsBatch(t *testing.T) {
	paths := []string{"a", "b", "c", "d"}
	// A tiny cap forces one path per command.
	cmds := rmCommands("/root", paths, len("rm -f --")+len(" '/root/a'")+1)
	if len(cmds) != 4 {
		t.Fatalf("expected 4 batched commands, got %d: %v", len(cmds), cmds)
	}
	for _, c := range cmds {
		if !strings.HasPrefix(c, "rm -f -- '/root/") {
			t.Errorf("batch not shaped as expected: %q", c)
		}
	}
	// A generous cap fits everything in one command.
	if one := rmCommands("/root", paths, 1<<20); len(one) != 1 {
		t.Errorf("generous cap should batch into one command: %v", one)
	}
}

func TestWithinRoot(t *testing.T) {
	cases := map[string]bool{
		"a.txt":        true,
		"sub/dir/a":    true,
		"a/b/../c":     true, // stays inside
		"":             false,
		"/etc/passwd":  false,
		"../escape":    false,
		"a/../../evil": false,
		".":            false,
		"..":           false,
	}
	for p, want := range cases {
		if got := withinRoot(p); got != want {
			t.Errorf("withinRoot(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDestManifestFilename(t *testing.T) {
	got := DestManifestFilename("u@dest", "/var/www/site")
	if !strings.HasPrefix(got, "dest-") {
		t.Errorf("destination manifest should be marked: %s", got)
	}
	if got == ManifestFilename("u@dest", "/var/www/site") {
		t.Error("destination and source manifests must not collide in one directory")
	}
	if got != DestManifestFilename("u@dest", "/var/www/site") {
		t.Error("filename must be deterministic")
	}
}

func hasPhase(phases []string, sub string) bool {
	for _, p := range phases {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}
