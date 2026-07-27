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

// shellQuote wraps a path in single quotes, escaping any embedded ones, so
// spaces and shell metacharacters in paths are inert.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
