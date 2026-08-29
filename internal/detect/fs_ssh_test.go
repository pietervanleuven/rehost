package detect

import (
	"context"
	"strings"
	"testing"

	"github.com/pietervanleuven/go-ssh"
)

// scriptRunner answers remote commands from a function, for exercising sshFS.
type scriptRunner func(cmd string) ssh.Result

func (s scriptRunner) Run(_ context.Context, cmd string) (ssh.Result, error) {
	return s(cmd), nil
}

func TestFindCommand(t *testing.T) {
	cmd := findCommand(
		[]string{"/home/u"},
		[]string{"wp-includes/version.php", "core/lib/Drupal.php"},
		6, nil,
	)
	for _, want := range []string{
		"find '/home/u'",
		"-maxdepth 9", // 6 descent budget + 3 for the deepest marker
		`-name 'node_modules'`,
		`-prune -o`,
		`-path '*/wp-includes/version.php'`,
		`-path '*/core/lib/Drupal.php'`,
		"-print0 2>/dev/null",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("find command missing %q:\n%s", want, cmd)
		}
	}
}

func TestSSHFindParsesOutput(t *testing.T) {
	run := scriptRunner(func(cmd string) ssh.Result {
		if strings.HasPrefix(cmd, "find") {
			// NUL-terminated: a newline inside a path stays one hit.
			return ssh.Result{Stdout: "/home/u/public_html/wp-includes/version.php\x00/home/u/blog\nx/wp-includes/version.php\x00"}
		}
		t.Fatalf("unexpected command: %s", cmd)
		return ssh.Result{}
	})
	fs := sshFS{r: run}
	got, err := fs.Find(context.Background(), []string{"/home/u"}, []string{"wp-includes/version.php"}, FindOptions{})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 2 || got[0] != "/home/u/public_html/wp-includes/version.php" || got[1] != "/home/u/blog\nx/wp-includes/version.php" {
		t.Errorf("parsed hits = %v", got)
	}
}

// List prefers a NUL-separated find so a newline-bearing filename stays one
// entry; hosts without find degrade to ls.
func TestSSHListNULSeparated(t *testing.T) {
	run := scriptRunner(func(cmd string) ssh.Result {
		if strings.HasPrefix(cmd, "find") {
			return ssh.Result{Stdout: "/srv/site/./b.txt\x00/srv/site/./weird\nname.txt\x00/srv/site/./.htaccess\x00"}
		}
		t.Fatalf("unexpected command: %s", cmd)
		return ssh.Result{}
	})
	fs := sshFS{r: run}
	got, err := fs.List(context.Background(), "/srv/site")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != ".htaccess" || got[1] != "b.txt" || got[2] != "weird\nname.txt" {
		t.Errorf("List = %q", got)
	}
}

func TestSSHListFallsBackToLS(t *testing.T) {
	run := scriptRunner(func(cmd string) ssh.Result {
		switch {
		case strings.HasPrefix(cmd, "find"):
			return ssh.Result{Stderr: "sh: find: command not found", ExitCode: 127}
		case strings.HasPrefix(cmd, "ls "):
			return ssh.Result{Stdout: "a.txt\nb.txt\n"}
		default:
			return ssh.Result{ExitCode: 1}
		}
	})
	fs := sshFS{r: run}
	got, err := fs.List(context.Background(), "/srv/site")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a.txt" {
		t.Errorf("List fallback = %q", got)
	}
}

func TestSSHFindFallsBackWhenFindMissing(t *testing.T) {
	// find is absent (127); the walk fallback then uses test/ls to locate the
	// marker directly under the root.
	run := scriptRunner(func(cmd string) ssh.Result {
		switch {
		case strings.HasPrefix(cmd, "find"):
			return ssh.Result{Stderr: "sh: find: command not found", ExitCode: 127}
		case strings.Contains(cmd, "test -e") && strings.Contains(cmd, "marker"):
			return ssh.Result{ExitCode: 0} // marker exists at the root
		case strings.HasPrefix(cmd, "test "):
			return ssh.Result{ExitCode: 1}
		case strings.HasPrefix(cmd, "ls "):
			return ssh.Result{Stdout: ""} // empty dir: no descent
		default:
			return ssh.Result{ExitCode: 1}
		}
	})
	fs := sshFS{r: run}
	got, err := fs.Find(context.Background(), []string{"/srv/site"}, []string{"marker"}, FindOptions{MaxDepth: 2})
	if err != nil {
		t.Fatalf("Find fallback: %v", err)
	}
	if len(got) != 1 || got[0] != "/srv/site/marker" {
		t.Errorf("fallback hits = %v, want [/srv/site/marker]", got)
	}
}
