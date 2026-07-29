package check

import (
	"context"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/ssh"
)

// runner is the slice of ssh.Client the gatherers need; tests use a fake.
type runner interface {
	Run(ctx context.Context, cmd string) (ssh.Result, error)
}

// PHPExtensions lists the loaded PHP modules on a host via `php -m`, or nil
// when that fails — every gatherer is best-effort, unknown never blocks.
func PHPExtensions(ctx context.Context, r runner) []string {
	res, err := r.Run(ctx, "php -m")
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	var exts []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		// Skip blanks and the "[PHP Modules]" / "[Zend Modules]" headers.
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		exts = append(exts, line)
	}
	return exts
}

// FreeKB measures the free space of the filesystem holding path via POSIX
// `df -P -k`, or 0 when unknown.
func FreeKB(ctx context.Context, r runner, path string) int64 {
	res, err := r.Run(ctx, "df -P -k "+ssh.ShellQuote(path))
	if err != nil || res.ExitCode != 0 {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) < 2 {
		return 0
	}
	// Last line, 4th column: Filesystem 1024-blocks Used Available Capacity Mounted
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0
	}
	return kb
}

// DirsSizeKB sums the sizes of dirs via `du -sk`, skipping any that fail;
// 0 means nothing could be measured. du often exits non-zero over permission
// warnings while still printing a usable total, so only the output counts.
func DirsSizeKB(ctx context.Context, r runner, dirs []string) int64 {
	var total int64
	for _, dir := range dirs {
		res, err := r.Run(ctx, "du -sk "+ssh.ShellQuote(dir)+" 2>/dev/null")
		if err != nil {
			continue
		}
		fields := strings.Fields(res.Stdout)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		total += kb
	}
	return total
}
