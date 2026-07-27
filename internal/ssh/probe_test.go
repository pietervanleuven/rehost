package ssh

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const fullHostOutput = `::REHOST::shell::-bash
::REHOST::uname::Linux x86_64
::REHOST::home::/home/u12345
::REHOST::tool::rsync::/usr/bin/rsync
::REHOST::tool::mysqldump::/usr/bin/mysqldump
::REHOST::tool::mysql::/usr/bin/mysql
::REHOST::tool::tar::/bin/tar
::REHOST::tool::gzip::/bin/gzip
::REHOST::tool::php::/usr/bin/php
::REHOST::tool::wp::/usr/local/bin/wp
::REHOST::tool::drush::MISSING
::REHOST::ver::rsync::rsync  version 3.2.7  protocol version 31
::REHOST::ver::php::PHP 8.3.11 (cli) (built: Sep 26 2024 10:00:00) (NTS)
::REHOST::ver::wp::WP-CLI 2.11.0
::REHOST::ver::drush::
::REHOST::done
`

func TestParseFullHost(t *testing.T) {
	caps, err := parseProbeOutput("source.example.com", fullHostOutput)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	if caps.Shell != "bash" {
		t.Errorf("Shell = %q, want bash (normalized from -bash)", caps.Shell)
	}
	if caps.Uname != "Linux x86_64" || caps.Home != "/home/u12345" {
		t.Errorf("uname/home wrong: %q %q", caps.Uname, caps.Home)
	}
	if caps.PHPVersion != "8.3.11" {
		t.Errorf("PHPVersion = %q, want 8.3.11", caps.PHPVersion)
	}
	if !caps.Has("rsync") || !caps.Has("wp") {
		t.Error("rsync and wp should be found")
	}
	if caps.Has("drush") {
		t.Error("drush should be missing")
	}
	if got := caps.Tools["rsync"]; got.Path != "/usr/bin/rsync" || !strings.Contains(got.Version, "3.2.7") {
		t.Errorf("rsync tool = %+v", got)
	}
	if got := caps.Tools["drush"]; got.Version != "" {
		t.Errorf("missing tool must not get a version, got %q", got.Version)
	}
}

func TestParseIgnoresBannerNoise(t *testing.T) {
	noisy := "Welcome to SharedHost 3000!\n* Disk quota: 93%\nstty: standard input: Inappropriate ioctl\n" +
		"::REHOST::shell::/bin/sh\n::REHOST::tool::php::MISSING\n::REHOST::done\n"
	caps, err := parseProbeOutput("h", noisy)
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	if caps.Shell != "sh" {
		t.Errorf("Shell = %q, want sh", caps.Shell)
	}
	if caps.Has("php") {
		t.Error("php should be missing")
	}
}

func TestParsePartialRestrictedShellOutput(t *testing.T) {
	partial := "::REHOST::shell::rbash\n::REHOST::tool::tar::/bin/tar\n" // died mid-script
	caps, err := parseProbeOutput("h", partial)
	if err != nil {
		t.Fatalf("partial output should still parse: %v", err)
	}
	if caps.Shell != "rbash" || !caps.Has("tar") {
		t.Errorf("caps = %+v", caps)
	}
}

func TestCapabilitiesTarget(t *testing.T) {
	if got := (&Capabilities{Host: "h", User: "u"}).Target(); got != "u@h" {
		t.Errorf("Target() = %q, want u@h", got)
	}
	if got := (&Capabilities{Host: "h"}).Target(); got != "h" {
		t.Errorf("Target() without user = %q, want h", got)
	}
}

func TestParseNoSentinelsFails(t *testing.T) {
	if _, err := parseProbeOutput("h", "bash: syntax error near unexpected token `('\n"); err == nil {
		t.Fatal("output without markers must fail so the sequential fallback kicks in")
	}
}

// fakeRunner scripts responses per command substring.
type fakeRunner struct {
	compoundErr bool // reject the compound probe script
	calls       []string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (Result, error) {
	f.calls = append(f.calls, cmd)
	if strings.Contains(cmd, "for t in") {
		if f.compoundErr {
			return Result{Stderr: "sh: for: not allowed", ExitCode: 2}, nil
		}
		return Result{Stdout: fullHostOutput}, nil
	}
	switch {
	case cmd == "echo $0":
		return Result{Stdout: "rbash\n"}, nil
	case cmd == "uname -sm":
		return Result{Stdout: "Linux x86_64\n"}, nil
	case cmd == "echo $HOME":
		return Result{Stdout: "/home/u1\n"}, nil
	case strings.HasPrefix(cmd, "command -v "):
		tool := strings.TrimPrefix(cmd, "command -v ")
		if tool == "tar" || tool == "php" {
			return Result{Stdout: "/bin/" + tool + "\n"}, nil
		}
		return Result{ExitCode: 1}, nil
	case strings.HasPrefix(cmd, "which "):
		return Result{ExitCode: 1}, nil
	case cmd == "php -v":
		return Result{Stdout: "PHP 7.4.33 (cli) (built: Nov  2 2022)\n"}, nil
	case cmd == "tar --version":
		return Result{Stdout: "tar (GNU tar) 1.34\n"}, nil
	}
	return Result{ExitCode: 127}, nil
}

func TestProbePrefersCompoundScript(t *testing.T) {
	r := &fakeRunner{}
	caps, err := probeWith(context.Background(), r, "h")
	if err != nil {
		t.Fatalf("probeWith: %v", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("compound probe should be a single round trip, got %d calls", len(r.calls))
	}
	if !caps.Has("rsync") || caps.PHPVersion != "8.3.11" {
		t.Errorf("caps = %+v", caps)
	}
}

func TestProbeFallsBackToSequential(t *testing.T) {
	r := &fakeRunner{compoundErr: true}
	caps, err := probeWith(context.Background(), r, "h")
	if err != nil {
		t.Fatalf("probeWith: %v", err)
	}
	if caps.Shell != "rbash" {
		t.Errorf("Shell = %q, want rbash", caps.Shell)
	}
	if !caps.Has("tar") || !caps.Has("php") || caps.Has("rsync") {
		t.Errorf("tool detection wrong: %+v", caps.Tools)
	}
	if caps.PHPVersion != "7.4.33" {
		t.Errorf("PHPVersion = %q, want 7.4.33", caps.PHPVersion)
	}
	if got := caps.Tools["tar"].Version; !strings.Contains(got, "1.34") {
		t.Errorf("tar version = %q", got)
	}
}

type deadRunner struct{}

func (deadRunner) Run(context.Context, string) (Result, error) {
	return Result{}, errors.New("connection lost")
}

func TestProbeTransportFailure(t *testing.T) {
	if _, err := probeWith(context.Background(), deadRunner{}, "h"); err == nil {
		t.Fatal("transport failure must surface as an error")
	}
}
