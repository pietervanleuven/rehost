package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/placeholder/rehost/internal/check"
	"github.com/placeholder/rehost/internal/db"
	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/inventory"
	"github.com/placeholder/rehost/internal/project"
	"github.com/placeholder/rehost/internal/recipe"
	"github.com/placeholder/rehost/internal/ssh"
	"github.com/placeholder/rehost/internal/state"
	"github.com/placeholder/rehost/internal/transfer"
	"github.com/placeholder/rehost/internal/tui"
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
destination: it streams a verified database dump of every detected site into
.rehost/dumps/ next to the project file, and measures the achievable tar-pipe
transfer rate (capped sample). With --json this prints a second JSON document
(schema rehost.dryrun-report.v1) after the capability report.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, opts, args, docroots, dryRun)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "stream a verified DB dump per site into .rehost/dumps/ and measure transfer throughput")
	return cmd
}

// target is one host to probe, labeled with its migration role.
type target struct {
	role string
	cfg  ssh.Config
}

func runPlan(cmd *cobra.Command, opts *options, args []string, docroots []string, dryRun bool) error {
	mode := opts.outputMode()
	renderer := tui.New(mode, cmd.OutOrStdout())

	// Prompts (passwords, TOFU, the host form) only exist on an interactive
	// terminal; in plain/JSON mode nothing may ever block a pipe.
	interactive := mode == tui.ModeStyled

	targets, projFile, err := planTargets(opts.projectFile, args, interactive, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
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
	dryResults := make([][]check.Result, len(targets))
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
		progress("%s: found %s on %s — measuring sizes…", t.role, pluralizeSites(len(installs)), caps.Target())

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
			results, err := collectDryRun(ctx, client, caps, installs, opts.projectFile, progress)
			if err != nil {
				return fmt.Errorf("%s: dry run: %w", t.role, err)
			}
			dryResults[i] = results
		}
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

	// Persist what was detected: later commands (check, migrate) read the
	// sites from the project file. Only when plan ran from one.
	if projFile != nil {
		if updated, err := writeSites(projFile, opts.projectFile, reports); err != nil {
			return fmt.Errorf("updating %s: %w", opts.projectFile, err)
		} else if updated {
			progress("updated %s with the detected sites", opts.projectFile)
		}
	}
	if err := renderer.CapabilityReport(reports); err != nil {
		return err
	}
	if dryRun {
		var all []check.Result
		for _, r := range dryResults {
			all = append(all, r...)
		}
		if mode != tui.ModeJSON {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return renderer.DryRunReport(all)
	}
	return nil
}

// collectDryRun proves the collection pipeline for each detected site: a
// capped tar-pipe throughput sample, and a streamed, verified database dump
// written under .rehost/dumps/ next to the project file. Failures become
// warnings in the report — a dry run informs, it does not gate.
func collectDryRun(ctx context.Context, client *ssh.Client, caps *ssh.Capabilities,
	installs []detect.Install, projectFile string, progress func(string, ...any)) ([]check.Result, error) {

	var results []check.Result
	add := func(id, title string, sev check.Severity, detail string) {
		results = append(results, check.Result{ID: id, Title: title, Severity: sev, Detail: detail})
	}
	fsys := detect.NewSSHFS(client)
	stateDir := filepath.Join(filepath.Dir(projectFile), ".rehost")
	dumpDir := filepath.Join(stateDir, "dumps")

	for _, inst := range installs {
		site := inst.Root

		// File manifest: the convergence bookkeeping a rerun diffs against.
		if caps.Has("find") {
			progress("source: building file manifest for %s…", site)
			manifestResult(ctx, client, inst, stateDir, add)
		} else {
			add("dryrun.manifest:"+site, "File manifest", check.Warning,
				site+": no find on the source — reruns cannot compute deltas")
		}

		// Throughput sample over the tar pipe the real migration would use.
		if caps.Has("tar") {
			progress("source: sampling transfer rate for %s…", site)
			st, err := transfer.Throughput(ctx, client, inst.Root, recipe.ExcludeSuggestionsFor(inst), 0, 0)
			if err != nil {
				add("dryrun.transfer:"+site, "Transfer rate", check.Warning, fmt.Sprintf("%s: %v", site, err))
			} else {
				detail := fmt.Sprintf("%s: %s compressed in %.1fs (~%s/s)", site,
					inventory.HumanKB(st.Bytes/1024), st.Duration.Seconds(), inventory.HumanKB(int64(st.BytesPerSec())/1024))
				if st.Capped {
					detail += ", sampled"
				}
				add("dryrun.transfer:"+site, "Transfer rate", check.Ok, detail)
			}
		} else {
			add("dryrun.transfer:"+site, "Transfer rate", check.Warning, site+": no tar on the source — cannot sample")
		}

		// Verified database dump.
		ex := recipe.ExtractorFor(inst.Framework)
		if ex == nil {
			continue // static site: no database to dump
		}
		creds, err := ex.ExtractCredentials(ctx, db.Host{Run: client, FS: fsys, Caps: caps}, inst)
		if err != nil {
			return nil, err
		}
		switch {
		case creds == nil || creds.Name == "":
			add("dryrun.dump:"+site, "Database dump", check.Warning, site+": credentials not readable — cannot dump")
		case !caps.Has("mysqldump") && !caps.Has("php"):
			add("dryrun.dump:"+site, "Database dump", check.Warning, site+": neither mysqldump nor php on the source — cannot dump")
		default:
			// mysqldump when present; the PHP helper is the fallback for
			// hosts that only have PHP.
			dump, method := db.Dump, "mysqldump"
			if !caps.Has("mysqldump") {
				dump, method = db.DumpPHP, "php fallback"
			}
			progress("source: dumping database %s (%s)…", creds.Name, method)
			detail, ok := dumpToFile(ctx, client, creds, dumpDir, dump)
			sev := check.Ok
			if !ok {
				sev = check.Warning
			}
			add("dryrun.dump:"+site, "Database dump", sev, fmt.Sprintf("%s: %s (%s)", site, detail, method))
		}
	}

	// Leave a trace in the source's hidden state folder: the history the
	// status/history commands will read in Phase 3.
	_, warnings := check.Summarize(results)
	entry := state.Entry{Event: "dry-run", Details: map[string]string{
		"sites":    strconv.Itoa(len(installs)),
		"warnings": strconv.Itoa(warnings),
	}}
	if err := state.Record(ctx, client, caps.Home, entry); err != nil {
		add("dryrun.state", "Run history (source)", check.Warning,
			fmt.Sprintf("could not record the run in %s: %v", state.Dir(caps.Home), err))
	}
	return results, nil
}

// manifestResult takes the site's file manifest, reports the delta against
// the previous run when one exists, and persists the new manifest — the
// proof that reruns are incremental.
func manifestResult(ctx context.Context, client *ssh.Client, inst detect.Install, stateDir string, add func(id, title string, sev check.Severity, detail string)) {
	site := inst.Root
	id := "dryrun.manifest:" + site
	m, err := transfer.TakeManifest(ctx, client, inst.Root, recipe.ExcludeSuggestionsFor(inst))
	if err != nil {
		add(id, "File manifest", check.Warning, fmt.Sprintf("%s: %v", site, err))
		return
	}
	manifestPath := filepath.Join(stateDir, "manifests", transfer.ManifestFilename(inst.Root))
	prev, err := transfer.LoadManifest(manifestPath)
	if err != nil {
		add(id, "File manifest", check.Warning, fmt.Sprintf("%s: previous manifest unreadable: %v", site, err))
		prev = nil
	}

	detail := fmt.Sprintf("%s: %d files", site, len(m.Files))
	if m.Complete {
		detail += ", " + inventory.HumanKB(m.TotalBytes()/1024)
	} else {
		detail += " (paths only — no GNU find, deltas degrade to presence)"
	}
	switch {
	case prev == nil:
		detail += " — first manifest saved"
	default:
		d := transfer.Diff(prev, m)
		detail += fmt.Sprintf(" — since last run: %d to transfer (+%d new, %d changed), %d removed, %d unchanged",
			d.Total(), len(d.Added), len(d.Changed), len(d.Removed), d.Unchanged)
	}
	if err := transfer.SaveManifest(m, manifestPath); err != nil {
		add(id, "File manifest", check.Warning, fmt.Sprintf("%s: saving manifest: %v", site, err))
		return
	}
	add(id, "File manifest", check.Ok, detail)
}

// dumpToFile streams one verified dump to disk (0600 — it holds site data)
// and describes the outcome. A failed verification removes the file so a
// truncated dump can never be mistaken for a good one.
func dumpToFile(ctx context.Context, client *ssh.Client, creds *db.Credentials, dumpDir string,
	dump func(context.Context, db.Streamer, *db.Credentials, io.Writer) (*db.DumpStats, error)) (detail string, ok bool) {
	if err := os.MkdirAll(dumpDir, 0o700); err != nil {
		return err.Error(), false
	}
	path := filepath.Join(dumpDir, creds.Name+".sql.gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err.Error(), false
	}
	stats, dumpErr := dump(ctx, client, creds, f)
	closeErr := f.Close()
	if dumpErr != nil || closeErr != nil {
		os.Remove(path)
		if dumpErr == nil {
			dumpErr = closeErr
		}
		return dumpErr.Error(), false
	}
	return fmt.Sprintf("wrote %s — %s SQL (%s compressed), %d tables, verified, %.1fs",
		path, inventory.HumanKB(stats.Bytes/1024), inventory.HumanKB(stats.CompressedBytes/1024),
		stats.Tables, stats.Duration.Seconds()), true
}

// writeSites refreshes the project file's sites section from the source
// scan. It reports whether a write happened — an unchanged detection result
// leaves the file untouched.
func writeSites(f *project.File, path string, reports []tui.HostReport) (bool, error) {
	var sites []project.Site
	for _, hr := range reports {
		if hr.Role != "source" {
			continue
		}
		for _, inst := range hr.Installs {
			sites = append(sites, project.Site{Framework: inst.Framework, Root: inst.Root, Version: inst.Version})
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
			fmt.Fprintf(errOut, "No project file at %s — asking for a host instead ('rehost init' writes one).\n", projectFile)
			var h project.Host
			if err := tui.HostForm("source", &h); err != nil {
				if errors.Is(err, tui.ErrAborted) {
					return nil, nil, errors.New("plan cancelled — no host to probe")
				}
				return nil, nil, err
			}
			return []target{{role: "source", cfg: h.SSHConfig()}}, nil, nil
		}
		fmt.Fprintf(errOut, "No project file at %s. Create one like this:\n\n%s\n…or probe a host directly: rehost plan user@host\n\n", projectFile, project.Example())
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
