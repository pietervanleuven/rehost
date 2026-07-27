package detect

import (
	"context"
	"fmt"
	"strings"

	"github.com/placeholder/rehost/internal/ssh"
)

// maxReadBytes bounds ReadFile so a stray huge file cannot exhaust memory.
// Framework config and version files are a few KB at most.
const maxReadBytes = 1 << 20 // 1 MiB

// runner is the slice of *ssh.Client the SSH filesystem needs.
type runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// sshFS reads a remote host over shell commands.
type sshFS struct{ r runner }

// NewSSHFS returns an FS backed by an SSH client.
func NewSSHFS(client *ssh.Client) FS { return sshFS{r: client} }

func (f sshFS) Exists(ctx context.Context, p string) (bool, error) {
	return f.test(ctx, "-e", p)
}

func (f sshFS) IsDir(ctx context.Context, p string) (bool, error) {
	return f.test(ctx, "-d", p)
}

// test runs `test <flag> <path>`: exit 0 means true, exit 1 means false, and
// anything else (or a transport error) is a real failure.
func (f sshFS) test(ctx context.Context, flag, p string) (bool, error) {
	res, err := f.r.Run(ctx, fmt.Sprintf("test %s %s", flag, shellQuote(p)))
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("test %s %s failed (exit %d): %s", flag, p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

func (f sshFS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	// head -c bounds the transfer; a missing file yields a non-zero exit.
	res, err := f.r.Run(ctx, fmt.Sprintf("head -c %d %s", maxReadBytes, shellQuote(p)))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("reading %s (exit %d): %s", p, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return []byte(res.Stdout), nil
}

func (f sshFS) List(ctx context.Context, dir string) ([]string, error) {
	// -A lists dotfiles but omits . and ..; -1 is one per line.
	res, err := f.r.Run(ctx, fmt.Sprintf("ls -1A %s", shellQuote(dir)))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("listing %s (exit %d): %s", dir, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if name := strings.TrimRight(line, "\r"); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// Find locates markers under the roots with a single remote `find`, falling
// back to a manual walk when `find` is unavailable (jailed shells) or errors.
func (f sshFS) Find(ctx context.Context, roots, markers []string, opts FindOptions) ([]string, error) {
	if len(roots) == 0 || len(markers) == 0 {
		return nil, nil
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}

	res, err := f.r.Run(ctx, findCommand(roots, markers, maxDepth, opts.Prune))
	if err != nil {
		return nil, err
	}
	// find returns non-zero when it hits unreadable paths even though it
	// printed usable matches; only treat "command not found" (127) and
	// misuse (usually a shell that lacks find's predicates) as a reason to
	// fall back to the manual walk.
	if res.ExitCode == 127 || res.ExitCode == 126 {
		return WalkFind(ctx, f, roots, markers, opts)
	}
	var hits []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if p := strings.TrimRight(line, "\r"); p != "" {
			hits = append(hits, p)
		}
	}
	// A total blank with a hard error and no output: the predicates may be
	// unsupported (busybox variants). Fall back to be safe.
	if len(hits) == 0 && res.ExitCode != 0 {
		return WalkFind(ctx, f, roots, markers, opts)
	}
	return hits, nil
}

// findCommand builds one POSIX `find` that prunes heavy directories and prints
// paths ending in any marker, depth-bounded, with permission noise silenced.
// find's -maxdepth counts full path components, so allow for the markers' own
// depth on top of the directory-descent budget.
func findCommand(roots, markers []string, maxDepth int, prune []string) string {
	if len(prune) == 0 {
		prune = DefaultPrune
	}
	var b strings.Builder
	b.WriteString("find")
	for _, r := range roots {
		b.WriteString(" " + shellQuote(r))
	}
	fmt.Fprintf(&b, " -maxdepth %d", maxDepth+markerDepth(markers))

	if len(prune) > 0 {
		b.WriteString(` \(`)
		for i, name := range prune {
			if i > 0 {
				b.WriteString(" -o")
			}
			b.WriteString(" -name " + shellQuote(name))
		}
		b.WriteString(` \) -prune -o`)
	}

	b.WriteString(` \(`)
	for i, m := range markers {
		if i > 0 {
			b.WriteString(" -o")
		}
		b.WriteString(" -path " + shellQuote("*/"+m))
	}
	b.WriteString(` \) -print 2>/dev/null`)
	return b.String()
}

// markerDepth is the largest number of path components among the markers, used
// to widen find's -maxdepth so a marker at the depth budget is still reached.
func markerDepth(markers []string) int {
	max := 1
	for _, m := range markers {
		if n := strings.Count(m, "/") + 1; n > max {
			max = n
		}
	}
	return max
}

// shellQuote wraps a path in single quotes, escaping any embedded ones, so
// spaces and shell metacharacters in paths are inert.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
