package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/placeholder/rehost/internal/check"
	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/project"
	"github.com/placeholder/rehost/internal/recipe"
	"github.com/placeholder/rehost/internal/ssh"
	"github.com/placeholder/rehost/internal/tui"
)

func newCheckCmd(opts *options) *cobra.Command {
	var docroots []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Compatibility gate between source and destination, rerunnable until green",
		Long: `check connects to both hosts and verifies the destination can actually run
what the source hosts: PHP version and extensions per detected framework,
database tooling, a workable file-transfer strategy, and disk space.

Blockers mean migration cannot work and must be fixed; warnings mean it can
proceed with caveats. check changes nothing on either host — rerun it until
it is green. The exit status is non-zero while blockers remain.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, opts, docroots)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	return cmd
}

func runCheck(cmd *cobra.Command, opts *options, docroots []string) error {
	mode := opts.outputMode()
	renderer := tui.New(mode, cmd.OutOrStdout())
	interactive := mode == tui.ModeStyled

	f, err := project.Load(opts.projectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no project file at %s — run 'rehost init' first", opts.projectFile)
	}
	if err != nil {
		return err
	}
	if f.Destination == nil {
		return fmt.Errorf("%s has no destination — rerun 'rehost init' or add a destination section", opts.projectFile)
	}

	var prompter ssh.Prompter
	if interactive {
		prompter = tui.HuhPrompter{}
	} else {
		prompter = tui.NonInteractivePrompter{}
	}
	progress := func(format string, a ...any) {
		if mode != tui.ModeJSON {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		}
	}

	in := check.Input{}

	gatherSource := func(ctx context.Context) error {
		cfg := f.Source.SSHConfig()
		progress("source: connecting to %s…", targetLabel(cfg))
		client, err := ssh.Dial(ctx, cfg, prompter)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		defer client.Close()
		caps, err := ssh.Probe(ctx, client)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		progress("source: connected to %s (%s) — scanning for websites…", caps.Target(), caps.Summary())

		startRoots := docroots
		if len(startRoots) == 0 {
			startRoots = []string{homeOrDot(caps.Home)}
		}
		installs, err := detect.Discover(ctx, detect.NewSSHFS(client), startRoots, recipe.All(),
			detect.FindOptions{Prune: detect.DefaultPrune})
		if err != nil {
			return fmt.Errorf("source: detecting frameworks: %w", err)
		}
		progress("source: found %s — measuring size…", pluralizeSites(len(installs)))

		roots := make([]string, 0, len(installs))
		for _, inst := range installs {
			roots = append(roots, inst.Root)
		}
		in.Source, in.Installs = caps, installs
		in.SourceSitesKB = check.DirsSizeKB(ctx, client, roots)
		return nil
	}

	gatherDest := func(ctx context.Context) error {
		cfg := f.Destination.SSHConfig()
		progress("destination: connecting to %s…", targetLabel(cfg))
		client, err := ssh.Dial(ctx, cfg, prompter)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}
		defer client.Close()
		caps, err := ssh.Probe(ctx, client)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}
		progress("destination: connected to %s (%s) — checking PHP and disk space…", caps.Target(), caps.Summary())

		in.Destination = caps
		if caps.PHPVersion != "" {
			in.DestPHPExtensions = check.PHPExtensions(ctx, client)
		}
		in.DestFreeKB = check.FreeKB(ctx, client, homeOrDot(caps.Home))
		return nil
	}

	if interactive {
		// Sequential so prompts from two hosts never interleave.
		for _, gather := range []func(context.Context) error{gatherSource, gatherDest} {
			if err := gather(cmd.Context()); err != nil {
				return err
			}
		}
	} else {
		g, ctx := errgroup.WithContext(cmd.Context())
		g.Go(func() error { return gatherSource(ctx) })
		g.Go(func() error { return gatherDest(ctx) })
		if err := g.Wait(); err != nil {
			if mode == tui.ModeJSON {
				renderer.Error(err) // keep stdout machine-readable
			}
			return err
		}
	}

	results := check.Run(in)
	if err := renderer.CheckReport(results); err != nil {
		return err
	}
	if blockers, _ := check.Summarize(results); blockers > 0 {
		return fmt.Errorf("check found %d blocker(s) — fix them and rerun 'rehost check'", blockers)
	}
	return nil
}
