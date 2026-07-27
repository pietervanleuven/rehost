package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/ssh"
)

// rule maps the first matching substring (checked in order) to a result.
type rule struct {
	substr string
	res    ssh.Result
}

type fakeRunner struct {
	rules []rule
	err   error
}

func (f fakeRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
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

func TestTake(t *testing.T) {
	root := "/home/u/public_html"
	r := fakeRunner{rules: []rule{
		{"/*/", ssh.Result{Stdout: "912000\t/home/u/public_html/wp-content/\n61000\t/home/u/public_html/wp-includes/\n2000\t/home/u/public_html/wp-admin/\n0\t/home/u/public_html/empty/\n"}},
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
	if len(inv.Top) != 3 || inv.Top[0].Path != "wp-content" || inv.Top[0].SizeKB != 912000 {
		t.Errorf("Top = %+v", inv.Top)
	}
	if len(inv.Suggested) != 1 || inv.Suggested[0].Path != "wp-content/cache" || inv.Suggested[0].SizeKB != 200000 {
		t.Errorf("Suggested = %+v", inv.Suggested)
	}
}

func TestTakeWithoutTools(t *testing.T) {
	inv, err := Take(context.Background(), fakeRunner{}, "/root", []string{"cache"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.TotalKB != 0 || inv.Top != nil || inv.Suggested != nil {
		t.Errorf("no-tools host should yield an empty inventory, got %+v", inv)
	}
}

func TestTakeTransportError(t *testing.T) {
	if _, err := Take(context.Background(), fakeRunner{err: errors.New("gone")}, "/r", nil); err == nil {
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
