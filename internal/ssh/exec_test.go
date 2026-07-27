package ssh

import (
	"context"
	"testing"
	"time"
)

func TestRunContextAppliesDefaultTimeout(t *testing.T) {
	ctx, cancel := runContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("Run's context must carry a deadline")
	}
	if until := time.Until(deadline); until > DefaultRunTimeout || until < DefaultRunTimeout-time.Minute {
		t.Errorf("deadline %v from now, want ~%v", until, DefaultRunTimeout)
	}
}

func TestRunContextKeepsSoonerCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()
	ctx, cancel := runContext(parent)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if time.Until(deadline) > 2*time.Second {
		t.Errorf("a sooner caller deadline must win, got %v from now", time.Until(deadline))
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"mysqldump: error 1044\nmore context\n": "mysqldump: error 1044",
		"  spaced  \n":                          "spaced",
		"":                                      "no error output",
		"\n\n":                                  "no error output",
	}
	for in, want := range cases {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
