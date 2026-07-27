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
	var docroots []string
	cmd := &cobra.Command{
		Use:   "plan [user@host[:port]]",
		Short: "Connect to the hosts and report their capabilities",
		Long: `plan connects to the source (and destination, when configured), probes
what the hosts offer (shell type, PHP version, availability of rsync,
mysqldump, tar, gzip, wp, drush and find), and detects the websites installed
on them — including multiple sites under one account.

By default it searches recursively from the SSH account's home directory. Pass
--docroot to point the search at a specific path instead (repeatable). A host
target may be given directly (rehost plan user@host) or read from the project
file. The deep source scan and dry-run land in a later phase.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, opts, args, docroots)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	return cmd
}

// target is one host to probe, labeled with its migration role.
type target struct {
	role string
	cfg  ssh.Config
}

func runPlan(cmd *cobra.Command, opts *options, args []string, docroots []string) error {
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

	// Progress goes to stderr so the report on stdout stays clean; JSON mode
	// stays silent so nothing but the document reaches a consumer.
	progress := func(format string, a ...any) {
		if mode != tui.ModeJSON {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		}
	}

	reports := make([]tui.HostReport, len(targets))
	probeOne := func(ctx context.Context, i int) error {
		t := targets[i]

		// 1. Connect — the step most likely to fail or need a prompt.
		progress("%s: connecting to %s…", t.role, targetLabel(t.cfg))
		client, err := ssh.Dial(ctx, t.cfg, prompter)
		if err != nil {
			return fmt.Errorf("%s: %w", t.role, err)
		}
		defer client.Close()

		// 2. Probe capabilities — quick, one round trip.
		caps, err := ssh.Probe(ctx, client)
		if err != nil {
			return fmt.Errorf("%s: %w", t.role, err)
		}
		progress("%s: connected to %s (%s) — scanning for websites…", t.role, caps.Target(), caps.Summary())

		// 3. Scan for sites — the potentially slow recursive step.
		fsys := detect.NewSSHFS(client)
		startRoots := docroots
		if len(startRoots) == 0 {
			startRoots = []string{homeOrDot(caps.Home)}
		}
		installs, err := detect.Discover(ctx, fsys, startRoots, recipe.All(),
			detect.FindOptions{Prune: detect.DefaultPrune})
		if err != nil {
			return fmt.Errorf("%s: detecting frameworks: %w", t.role, err)
		}
		progress("%s: found %s on %s", t.role, pluralizeSites(len(installs)), caps.Target())

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

// targetLabel is the user@host identity for pre-connect progress, or just the
// host when no user was given (the resolved user appears once connected).
func targetLabel(cfg ssh.Config) string {
	if cfg.User != "" {
		return cfg.User + "@" + cfg.Host
	}
	return cfg.Host
}

// pluralizeSites renders a site count for progress output.
func pluralizeSites(n int) string {
	if n == 1 {
		return "1 site"
	}
	return fmt.Sprintf("%d sites", n)
}

// homeOrDot is the recursive-search start root: the probed account home, or
// the current directory when the host did not report one.
func homeOrDot(home string) string {
	if home == "" {
		return "."
	}
	return home
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
