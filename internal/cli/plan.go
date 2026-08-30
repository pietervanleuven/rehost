package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/inventory"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/tui"
)

func newPlanCmd(opts *options) *cobra.Command {
	var docroots []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "plan [user@host[:port]]",
		Short: "Connect to the hosts and report their capabilities",
		Long: `plan connects to the source (and destination, when configured), probes
what the hosts offer (shell type, PHP version, availability of rsync,
mysqldump, tar, gzip, wp, drush and find), detects the websites installed
on them — including multiple sites under one account — and measures each
site's size with suggested transfer exclusions.

By default it searches recursively from the SSH account's home directory. Pass
--docroot to point the search at a specific path instead (repeatable). A host
target may be given directly (rehost plan user@host) or read from the project
file; with neither, an interactive terminal asks for the connection details.

--dry-run additionally proves the collection pipeline without touching a
destination: it records a file manifest of every detected site into
.rehost/manifests/ next to the project file (reruns report the
added/changed/removed delta against the previous run), streams a verified
database dump into .rehost/dumps/, and measures the achievable tar-pipe
transfer rate (capped sample).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, opts, args, docroots, dryRun)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "record a file manifest and a verified DB dump per site under .rehost/, and measure transfer throughput")
	return cmd
}

// target is one host to probe, labeled with its migration role.
type target struct {
	role string
	cfg  ssh.Config
}

func runPlan(cmd *cobra.Command, opts *options, args []string, docroots []string, dryRun bool) error {
	u := newUI(cmd, opts)

	targets, projFile, err := planTargets(opts.projectFile, args, u.interactive, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	reports := make([]tui.HostReport, len(targets))
	dryResults := make([][]check.Result, len(targets))
	probeOne := func(ctx context.Context, i int) error {
		t := targets[i]

		client, caps, err := dialProbe(ctx, t.cfg, t.role, u)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		u.progress("%s: connected to %s (%s) — scanning for websites…", t.role, caps.Target(), caps.Summary())

		// 3. Scan for sites — the potentially slow recursive step.
		fsys := detect.NewShellFS(client)
		startRoots := docroots
		if len(startRoots) == 0 {
			startRoots = []string{homeOrDot(caps.Home)}
		}
		installs, err := detect.Discover(ctx, fsys, startRoots, recipe.All(),
			detect.FindOptions{Prune: detect.DefaultPrune})
		if err != nil {
			return fmt.Errorf("%s: detecting frameworks: %w", t.role, err)
		}
		u.progress("%s: found %s on %s — measuring sizes…", t.role, pluralizeSites(len(installs)), caps.Target())

		// 4. Inventory each install: total size, largest directories, and
		// the framework caches/backups worth excluding from transfer.
		inventories := map[string]*inventory.Inventory{}
		for _, inst := range installs {
			inv, err := inventory.Take(ctx, client, inst.Root, recipe.ExcludeSuggestionsFor(inst))
			if err != nil {
				return fmt.Errorf("%s: measuring %s: %w", t.role, inst.Root, err)
			}
			inventories[inst.Root] = inv
		}

		reports[i] = tui.HostReport{Role: t.role, Caps: caps, Installs: installs, Inventories: inventories}

		// 5. Dry-run collection (source only): verified DB dump + tar-pipe
		// throughput, while the connection is still open.
		if dryRun && t.role == "source" {
			results, err := collectDryRun(ctx, client, caps, installs, opts.projectFile, u.progress)
			if err != nil {
				return fmt.Errorf("%s: dry run: %w", t.role, err)
			}
			dryResults[i] = results
		}
		return nil
	}

	if u.interactive {
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
			return u.fail(err)
		}
	}

	// Persist what was detected: later commands (check, migrate) read the
	// sites from the project file. Only when plan ran from one.
	if projFile != nil {
		if updated, err := writeSites(projFile, opts.projectFile, reports); err != nil {
			return fmt.Errorf("updating %s: %w", opts.projectFile, err)
		} else if updated {
			u.progress("updated %s with the detected sites", opts.projectFile)
		}
	}
	var all []check.Result
	for _, r := range dryResults {
		all = append(all, r...)
	}
	if err := u.renderer.PlanReport(reports, all, dryRun); err != nil {
		return err
	}
	if projFile != nil {
		u.progress("next: review the sites in %s (set dest_root and dest_db where needed), then run 'rehost check'", opts.projectFile)
	} else {
		u.progress("probed directly — nothing was written to %s; run 'rehost init' for a project file the rest of the flow can use", opts.projectFile)
	}
	return nil
}

// writeSites refreshes the project file's sites section from the source
// scan. It reports whether a write happened — an unchanged detection result
// leaves the file untouched. Hand-added per-site config (dest_root, dest_db)
// is carried over by root: detection only knows framework/root/version, so
// rebuilding the list from scratch would silently drop the destination
// mapping the user configured, and the next migrate would skip the database.
func writeSites(f *project.File, path string, reports []tui.HostReport) (bool, error) {
	existing := map[string]project.Site{}
	for _, s := range f.Sites {
		existing[s.Root] = s
	}
	var sites []project.Site
	for _, hr := range reports {
		if hr.Role != "source" {
			continue
		}
		for _, inst := range hr.Installs {
			site := project.Site{Framework: inst.Framework, Root: inst.Root, Version: inst.Version}
			if prev, ok := existing[inst.Root]; ok {
				site.DestRoot = prev.DestRoot
				site.DestDB = prev.DestDB
			}
			sites = append(sites, site)
		}
	}
	if slices.Equal(f.Sites, sites) {
		return false, nil
	}
	f.Sites = sites
	if err := f.Save(path); err != nil {
		return false, err
	}
	return true, nil
}

// planTargets resolves what to probe: an explicit CLI target, the source
// (+ destination) from the project file, or — interactively — a host form
// when neither exists. The project file is returned when the targets came
// from one, so plan can write its findings back.
func planTargets(projectFile string, args []string, interactive bool, errOut io.Writer) ([]target, *project.File, error) {
	if len(args) == 1 {
		cfg, err := parseTarget(args[0])
		if err != nil {
			return nil, nil, err
		}
		return []target{{role: "source", cfg: cfg}}, nil, nil
	}
	f, err := project.Load(projectFile)
	if errors.Is(err, fs.ErrNotExist) {
		if interactive {
			_, _ = fmt.Fprintf(errOut, "No project file at %s — asking for a host instead ('rehost init' writes one).\n", projectFile)
			var h project.Host
			if err := tui.HostForm("source", &h); err != nil {
				if errors.Is(err, tui.ErrAborted) {
					return nil, nil, errors.New("plan cancelled — no host to probe")
				}
				return nil, nil, err
			}
			return []target{{role: "source", cfg: h.SSHConfig()}}, nil, nil
		}
		_, _ = fmt.Fprintf(errOut, "No project file at %s. Create one like this:\n\n%s\n…or probe a host directly: rehost plan user@host\n\n", projectFile, project.Example())
		return nil, nil, fmt.Errorf("no project file at %s", projectFile)
	}
	if err != nil {
		return nil, nil, err
	}
	targets := []target{{role: "source", cfg: f.Source.SSHConfig()}}
	if f.Destination != nil {
		targets = append(targets, target{role: "destination", cfg: f.Destination.SSHConfig()})
	}
	return targets, f, nil
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
	} else if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
		cfg.Host = rest[1 : len(rest)-1] // bracketed IPv6 without a port
	} else {
		cfg.Host = rest // hostname, or a bare IPv6 literal
	}
	if cfg.Host == "" || strings.Contains(cfg.Host, "@") {
		return cfg, fmt.Errorf("invalid target %q — expected user@host[:port]", s)
	}
	return cfg, nil
}
