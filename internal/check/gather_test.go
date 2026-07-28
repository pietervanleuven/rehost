package check

import (
	"context"
	"errors"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// fakeRunner maps command strings to canned results.
type fakeRunner struct {
	results map[string]ssh.Result
	err     error
}

func (f fakeRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	if f.err != nil {
		return ssh.Result{}, f.err
	}
	res, ok := f.results[cmd]
	if !ok {
		return ssh.Result{ExitCode: 127}, nil
	}
	return res, nil
}

func TestPHPExtensions(t *testing.T) {
	r := fakeRunner{results: map[string]ssh.Result{
		"php -m": {Stdout: "[PHP Modules]\ncurl\ngd\nmysqli\n\n[Zend Modules]\nZend OPcache\n"},
	}}
	exts := PHPExtensions(context.Background(), r)
	want := []string{"curl", "gd", "mysqli", "Zend OPcache"}
	if len(exts) != len(want) {
		t.Fatalf("PHPExtensions = %v, want %v", exts, want)
	}
	for i := range want {
		if exts[i] != want[i] {
			t.Errorf("PHPExtensions[%d] = %q, want %q", i, exts[i], want[i])
		}
	}

	if exts := PHPExtensions(context.Background(), fakeRunner{err: errors.New("boom")}); exts != nil {
		t.Errorf("transport failure should yield nil, got %v", exts)
	}
	if exts := PHPExtensions(context.Background(), fakeRunner{}); exts != nil {
		t.Errorf("missing php should yield nil, got %v", exts)
	}
}

func TestFreeKB(t *testing.T) {
	r := fakeRunner{results: map[string]ssh.Result{
		"df -P -k '/home/u'": {Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1000000 400000 600000 40% /home\n"},
	}}
	if kb := FreeKB(context.Background(), r, "/home/u"); kb != 600000 {
		t.Errorf("FreeKB = %d, want 600000", kb)
	}
	if kb := FreeKB(context.Background(), fakeRunner{}, "/home/u"); kb != 0 {
		t.Errorf("failed df should yield 0, got %d", kb)
	}
}

func TestDirsSizeKB(t *testing.T) {
	r := fakeRunner{results: map[string]ssh.Result{
		"du -sk '/a' 2>/dev/null": {Stdout: "1024\t/a\n"},
		// /b prints a total despite exit 1 (permission warnings) — still counted.
		"du -sk '/b' 2>/dev/null": {Stdout: "2048\t/b\n", ExitCode: 1},
	}}
	if kb := DirsSizeKB(context.Background(), r, []string{"/a", "/b", "/missing"}); kb != 3072 {
		t.Errorf("DirsSizeKB = %d, want 3072", kb)
	}
}

func TestShQuote(t *testing.T) {
	if got := shQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("shQuote = %s", got)
	}
}
