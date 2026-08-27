package ssh

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// probedTools are the remote binaries every migration strategy cares about.
// Recipes can extend this list in later phases.
var probedTools = []string{"rsync", "mysqldump", "mysql", "tar", "gzip", "php", "wp", "drush", "find"}

// versionCmds print a single identifying line per tool; all best-effort.
var versionCmds = map[string]string{
	"rsync":     "rsync --version",
	"mysqldump": "mysqldump --version",
	"mysql":     "mysql --version",
	"tar":       "tar --version",
	"gzip":      "gzip --version",
	"php":       "php -v",
	"wp":        "wp cli version",
	"drush":     "drush --version",
	"find":      "find --version",
}

// Tool is one probed remote binary.
type Tool struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"` // best-effort first output line
}

// Capabilities is what a remote host offers. It is the input for the Phase 1
// check gate and framework recipes.
type Capabilities struct {
	Host       string          `json:"host"`
	User       string          `json:"user,omitempty"`
	Shell      string          `json:"shell,omitempty"`
	Uname      string          `json:"uname,omitempty"`
	Home       string          `json:"home,omitempty"`
	PHPVersion string          `json:"php_version,omitempty"`
	Tools      map[string]Tool `json:"tools"`
}

// Has reports whether a tool was found on the host.
func (c *Capabilities) Has(tool string) bool {
	t, ok := c.Tools[tool]
	return ok && t.Found
}

// Target is the user@host identity of the probed host, or just the host when
// no user is known. Which sites exist can differ per user (vhost setups), so
// the user is part of a host's identity in reports.
func (c *Capabilities) Target() string {
	if c.User != "" {
		return c.User + "@" + c.Host
	}
	return c.Host
}

// Summary is a compact one-line host descriptor (shell and PHP) for progress
// output; the full picture is in the capability report.
func (c *Capabilities) Summary() string {
	shell := c.Shell
	if shell == "" {
		shell = "unknown shell"
	}
	if c.PHPVersion != "" {
		return shell + ", PHP " + c.PHPVersion
	}
	return shell
}

// ProbedTools returns the canonical display order of probed tools.
func ProbedTools() []string {
	return slices.Clone(probedTools)
}

// sentinel prefixes every probe output line so MOTD banners, profile echo
// and stty noise cannot confuse the parser.
const sentinel = "::REHOST::"

// runner is the slice of Client the probe needs; tests substitute a fake.
type runner interface {
	Run(ctx context.Context, cmd string) (Result, error)
}

// Probe detects the host's capabilities: shell, uname, home, PHP version and
// tool availability. It prefers a single compound POSIX script (one round
// trip — shared hosts are slow and may cap sessions) and degrades to one
// command per session for restricted shells.
func Probe(ctx context.Context, client *Client) (*Capabilities, error) {
	caps, err := probeWith(ctx, client, client.Config.Host)
	if err != nil {
		return nil, err
	}
	caps.User = client.Config.User
	return caps, nil
}

func probeWith(ctx context.Context, r runner, host string) (*Capabilities, error) {
	res, err := r.Run(ctx, probeScript())
	if err == nil {
		if caps, perr := parseProbeOutput(host, res.Stdout); perr == nil {
			return caps, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Compound script rejected (restricted shell) or output unusable:
	// fall back to simple one-command sessions.
	return probeSequential(ctx, r, host)
}

// probeScript builds the compound POSIX script. `command -v` is POSIX;
// `which` is the in-script fallback for ancient shells.
func probeScript() string {
	var b strings.Builder
	b.WriteString(`echo "` + sentinel + `shell::$0"; `)
	b.WriteString(`echo "` + sentinel + `uname::$(uname -sm 2>/dev/null)"; `)
	b.WriteString(`echo "` + sentinel + `home::$HOME"; `)
	b.WriteString(`for t in ` + strings.Join(probedTools, " ") + `; do `)
	b.WriteString(`p=$(command -v "$t" 2>/dev/null) || p=$(which "$t" 2>/dev/null); `)
	b.WriteString(`echo "` + sentinel + `tool::$t::${p:-MISSING}"; done; `)
	for _, t := range probedTools {
		b.WriteString(`echo "` + sentinel + `ver::` + t + `::$(` + versionCmds[t] + ` 2>/dev/null | head -n 1)"; `)
	}
	b.WriteString(`echo "` + sentinel + `done"`)
	return b.String()
}

// parseProbeOutput is the pure core of the probe: it scans stdout for
// sentinel lines and ignores everything else.
func parseProbeOutput(host, out string) (*Capabilities, error) {
	caps := &Capabilities{Host: host, Tools: map[string]Tool{}}
	sawSentinel := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		idx := strings.Index(line, sentinel)
		if idx < 0 {
			continue
		}
		sawSentinel = true
		kind, payload, _ := strings.Cut(line[idx+len(sentinel):], "::")
		switch kind {
		case "shell":
			caps.Shell = shellName(payload)
		case "uname":
			caps.Uname = strings.TrimSpace(payload)
		case "home":
			caps.Home = strings.TrimSpace(payload)
		case "tool":
			name, path, ok := strings.Cut(payload, "::")
			if !ok {
				continue
			}
			path = strings.TrimSpace(path)
			tool := Tool{Name: name, Found: path != "" && path != "MISSING"}
			if tool.Found {
				tool.Path = path
			}
			caps.Tools[name] = tool
		case "ver":
			name, verLine, ok := strings.Cut(payload, "::")
			if !ok {
				continue
			}
			if tool, exists := caps.Tools[name]; exists && tool.Found {
				tool.Version = strings.TrimSpace(verLine)
				caps.Tools[name] = tool
			}
		}
	}
	if !sawSentinel {
		return nil, fmt.Errorf("probe output from %s contained no recognizable markers", host)
	}
	caps.finalize()
	return caps, nil
}

// probeSequential is the degradation path: one simple command per session,
// no compound syntax, no redirects (stderr is captured separately anyway).
func probeSequential(ctx context.Context, r runner, host string) (*Capabilities, error) {
	caps := &Capabilities{Host: host, Tools: map[string]Tool{}}

	// The first command doubles as the shell-works check; a transport-level
	// failure here means the host is unusable for probing.
	res, err := r.Run(ctx, `echo $0`)
	if err != nil {
		return nil, fmt.Errorf("probing %s: %w", host, err)
	}
	caps.Shell = shellName(strings.TrimSpace(firstLine(res.Stdout)))

	if res, err := r.Run(ctx, "uname -sm"); err == nil && res.ExitCode == 0 {
		caps.Uname = strings.TrimSpace(firstLine(res.Stdout))
	}
	if res, err := r.Run(ctx, "echo $HOME"); err == nil && res.ExitCode == 0 {
		caps.Home = strings.TrimSpace(firstLine(res.Stdout))
	}

	for _, t := range probedTools {
		path := ""
		if res, err := r.Run(ctx, "command -v "+t); err == nil && res.ExitCode == 0 {
			path = strings.TrimSpace(firstLine(res.Stdout))
		}
		tool := Tool{Name: t, Found: path != ""}
		if tool.Found {
			tool.Path = path
			if res, err := r.Run(ctx, versionCmds[t]); err == nil && res.ExitCode == 0 {
				tool.Version = strings.TrimSpace(firstLine(res.Stdout))
			}
		}
		caps.Tools[t] = tool
	}
	caps.finalize()
	return caps, nil
}

var phpVersionRe = regexp.MustCompile(`PHP ([0-9]+(?:\.[0-9]+)*)`)

// finalize derives cross-field values, currently the PHP version number out
// of php's version banner.
func (c *Capabilities) finalize() {
	if php, ok := c.Tools["php"]; ok && php.Found {
		if m := phpVersionRe.FindStringSubmatch(php.Version); m != nil {
			c.PHPVersion = m[1]
		}
	}
}

// shellName normalizes $0 forms like "-bash" or "/bin/bash" to "bash".
func shellName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "-")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
