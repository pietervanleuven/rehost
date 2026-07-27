package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/project"
	"github.com/placeholder/rehost/internal/recipe"
	"github.com/placeholder/rehost/internal/ssh"
	"github.com/placeholder/rehost/internal/tui"
)

func newPlanCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "plan [user@host[:port]]",
		Short: "Connect to the hosts and report their capabilities",
		Long: `plan connects to the source (and destination, when configured) and probes
what the hosts offer: shell type, PHP version, and availability of rsync,
mysqldump, tar, gzip, wp, drush and friends.

A target may be given directly (rehost plan user@host) or read from the
project file. The deep source scan and dry-run land in a later phase.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, opts, args)
		},
	}
}

// target is one host to probe, labeled with its migration role.
type target struct {
	role string
	cfg  ssh.Config
}

func runPlan(cmd *cobra.Command, opts *options, args []string) error {
	mode := opts.outputMode()
	renderer := tui.New(mode, cmd.OutOrStdout())

	targets, err := planTargets(opts.projectFile, args, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	// Prompts (passwords, TOFU) only exist on an interactive terminal; in
	// plain/JSON mode nothing may ever block a pipe.
	interactive := mode == tui.ModeStyled
	var prompter ssh.Prompter
	if interactive {
		prompter = tui.HuhPrompter{}
	} else {
		prompter = tui.NonInteractivePrompter{}
	}

	reports := make([]tui.HostReport, len(targets))
	probeOne := func(ctx context.Context, i int) error {
		t := targets[i]
		if mode != tui.ModeJSON {
			fmt.Fprintf(cmd.ErrOrStderr(), "connecting to %s (%s)…\n", t.cfg.Host, t.role)
		}
		client, err := ssh.Dial(ctx, t.cfg, prompter)
		if err != nil {
			return fmt.Errorf("%s: %w", t.role, err)
		}
		defer client.Close()
		caps, err := ssh.Probe(ctx, client)
		if err != nil {
			return fmt.Errorf("%s: %w", t.role, err)
		}
		fsys := detect.NewSSHFS(client)
		installs, err := detect.Scan(ctx, fsys, detect.DocrootCandidates(caps.Home), recipe.All())
		if err != nil {
			return fmt.Errorf("%s: detecting frameworks: %w", t.role, err)
		}
		reports[i] = tui.HostReport{Role: t.role, Caps: caps, Installs: installs}
		return nil
	}

	if interactive {
		// Sequential so prompts from two hosts never interleave.
		for i := range targets {
			if err := probeOne(cmd.Context(), i); err != nil {
				return err
			}
		}
	} else {
		g, ctx := errgroup.WithContext(cmd.Context())
		for i := range targets {
			g.Go(func() error { return probeOne(ctx, i) })
		}
		if err := g.Wait(); err != nil {
			if mode == tui.ModeJSON {
				renderer.Error(err) // keep stdout machine-readable
			}
			return err
		}
	}
	return renderer.CapabilityReport(reports)
}

// planTargets resolves what to probe: an explicit CLI target, or the
// source (+ destination) from the project file.
func planTargets(projectFile string, args []string, errOut io.Writer) ([]target, error) {
	if len(args) == 1 {
		cfg, err := parseTarget(args[0])
		if err != nil {
			return nil, err
		}
		return []target{{role: "source", cfg: cfg}}, nil
	}
	f, err := project.Load(projectFile)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(errOut, "No project file at %s. Create one like this:\n\n%s\n…or probe a host directly: rehost plan user@host\n\n", projectFile, project.Example())
		return nil, fmt.Errorf("no project file at %s", projectFile)
	}
	if err != nil {
		return nil, err
	}
	targets := []target{{role: "source", cfg: f.Source.SSHConfig()}}
	if f.Destination != nil {
		targets = append(targets, target{role: "destination", cfg: f.Destination.SSHConfig()})
	}
	return targets, nil
}

// parseTarget parses user@host[:port] (user and port optional).
func parseTarget(s string) (ssh.Config, error) {
	cfg := ssh.Config{}
	rest := s
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		cfg.User = rest[:i]
		rest = rest[i+1:]
	}
	if host, port, err := net.SplitHostPort(rest); err == nil {
		p, perr := strconv.Atoi(port)
		if perr != nil || p < 1 || p > 65535 {
			return cfg, fmt.Errorf("invalid port %q in target %q", port, s)
		}
		cfg.Host, cfg.Port = host, p
	} else {
		cfg.Host = rest
	}
	if cfg.Host == "" || strings.Contains(cfg.Host, "@") {
		return cfg, fmt.Errorf("invalid target %q — expected user@host[:port]", s)
	}
	return cfg, nil
}

// outputMode picks the renderer: --json wins, then plain for non-TTY or
// suppressed color, styled otherwise.
func (o *options) outputMode() tui.Mode {
	if o.json {
		return tui.ModeJSON
	}
	if o.noColor || os.Getenv("NO_COLOR") != "" {
		return tui.ModePlain
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return tui.ModePlain
	}
	return tui.ModeStyled
}
