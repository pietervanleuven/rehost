package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// rule maps the first matching substring (checked in order) to a result.
type rule struct {
	substr string
	res    ssh.Result
}

type fakeRunner struct {
	rules []rule
	err   error
	cmds  []string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	f.cmds = append(f.cmds, cmd)
	if f.err != nil {
		return ssh.Result{}, f.err
	}
	for _, r := range f.rules {
		if strings.Contains(cmd, r.substr) {
			return r.res, nil
		}
	}
	return ssh.Result{ExitCode: 127}, nil
}

func TestTakeNULFirst(t *testing.T) {
	root := "/home/u/public_html"
	// GNU du -0: NUL-terminated records; a directory name containing a
	// newline and a dot-directory both survive.
	nulOut := "912000\t/home/u/public_html/wp-content/\x00" +
		"5000\t/home/u/public_html/odd\ndir/\x00" +
		"61000\t/home/u/public_html/.git/\x00" +
		"0\t/home/u/public_html/empty/\x00"
	r := &fakeRunner{rules: []rule{
		{"-0", ssh.Result{Stdout: nulOut}},
		{"wp-content/cache", ssh.Result{Stdout: "200000\t/home/u/public_html/wp-content/cache\n"}},
		{"'/home/u/public_html'", ssh.Result{Stdout: "1200000\t/home/u/public_html\n"}},
	}}
	inv, err := Take(context.Background(), r, root, []string{"wp-content/cache", "wp-content/updraft"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalKB != 1200000 {
		t.Errorf("TotalKB = %d", inv.TotalKB)
	}
	if len(inv.Top) != 3 || inv.Top[0].Path != "wp-content" || inv.Top[1].Path != ".git" || inv.Top[2].Path != "odd\ndir" {
		t.Errorf("Top = %+v", inv.Top)
	}
	if len(inv.Suggested) != 1 || inv.Suggested[0].Path != "wp-content/cache" || inv.Suggested[0].SizeKB != 200000 {
		t.Errorf("Suggested = %+v", inv.Suggested)
	}
	// The breakdown glob must include dot-directories.
	var breakdownCmd string
	for _, cmd := range r.cmds {
		if strings.Contains(cmd, "-0") {
			breakdownCmd = cmd
		}
	}
	if !strings.Contains(breakdownCmd, `/.[!.]*/`) {
		t.Errorf("dot-directories missing from the breakdown glob: %s", breakdownCmd)
	}
}

func TestTakeNewlineFallbackKeepsPathsByteExact(t *testing.T) {
	// No du -0 (busybox/BSD): exit 1, no output → newline pipeline. A
	// directory with leading/trailing spaces is a real directory.
	r := &fakeRunner{rules: []rule{
		{"-0", ssh.Result{ExitCode: 1, Stderr: "du: unrecognized option: 0\n"}},
		{"sort -rn", ssh.Result{Stdout: "912000\t/site/wp-content/\n300\t/site/ spacey /\n"}},
		{"'/site'", ssh.Result{Stdout: "1000000\t/site\n"}},
	}}
	inv, err := Take(context.Background(), r, "/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Top) != 2 || inv.Top[1].Path != " spacey " {
		t.Errorf("whitespace directory names must survive: %+v", inv.Top)
	}
}

func TestTakeKilledDuYieldsNothing(t *testing.T) {
	// A du killed mid-run (exit above 1) printed a size that measured only
	// part of the tree — it must not be reported.
	r := &fakeRunner{rules: []rule{
		{"-0", ssh.Result{ExitCode: 137, Stdout: "912000\t/site/wp-content/\x00"}},
		{"sort -rn", ssh.Result{ExitCode: 137, Stdout: "912000\t/site/wp-content/\n"}},
		{"'/site'", ssh.Result{ExitCode: 137, Stdout: "1200000\t/site\n"}},
	}}
	inv, err := Take(context.Background(), r, "/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalKB != 0 || len(inv.Top) != 0 {
		t.Errorf("killed du must yield an empty inventory, got %+v", inv)
	}
}

func TestTakePermissionNoiseAccepted(t *testing.T) {
	// Exit 1 with output is du's unreadable-subdir case: the totals it did
	// print are real.
	r := &fakeRunner{rules: []rule{
		{"'/site'", ssh.Result{Stdout: "800\t/site\n", Stderr: "du: /site/private: Permission denied\n", ExitCode: 1}},
	}}
	inv, err := Take(context.Background(), r, "/site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalKB != 800 {
		t.Errorf("TotalKB = %d, want 800", inv.TotalKB)
	}
}

func TestTakeWithoutTools(t *testing.T) {
	inv, err := Take(context.Background(), &fakeRunner{}, "/root", []string{"cache"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalKB != 0 || inv.Top != nil || inv.Suggested != nil {
		t.Errorf("no-tools host should yield an empty inventory, got %+v", inv)
	}
}

func TestTakeTransportError(t *testing.T) {
	if _, err := Take(context.Background(), &fakeRunner{err: errors.New("gone")}, "/r", nil); err == nil {
		t.Error("transport failure must propagate")
	}
}

func TestHumanKB(t *testing.T) {
	cases := map[int64]string{
		512:         "512 KiB",
		2048:        "2.0 MiB",
		3 * 1048576: "3.0 GiB",
	}
	for kb, want := range cases {
		if got := HumanKB(kb); got != want {
			t.Errorf("HumanKB(%d) = %q, want %q", kb, got, want)
		}
	}
}
