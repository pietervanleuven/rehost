package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/transfer"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// destIDPrefix keys each destination-state result by its docroot so the
// refusal error can name the offending docroots without a parallel structure.
const destIDPrefix = "migrate.dest:"

// preflightNotice is the message a green pre-flight carries into the combined
// report: the gate passed and file sync runs now, but the later execution steps
// are not wired yet.
const preflightNotice = "Pre-flight passed — proceeding to file sync. Database import, config rewrite and cutover come later."

// migrateIncompleteNotice is the honest-stop message printed after file sync
// converges: the destination now holds the site's files, but the migration is
// not a finished, working site yet.
const migrateIncompleteNotice = "File sync converged, but the migration is NOT complete: database import, config rewrite, maintenance mode and cutover are not wired yet (Phase 3 — see docs/PLAN.md §6). The destination has the site's files but is not yet a working migrated site."

// errMigrateIncomplete is the non-zero exit returned after a successful file
// sync: the files converged, but the remaining migration steps do not exist
// yet, so the migration deliberately did not finish.
var errMigrateIncomplete = errors.New("file sync converged but the migration is not complete: database import, config rewrite and cutover are not wired yet")

func newMigrateCmd(opts *options) *cobra.Command {
	var docroots []string
	var ontoExisting, del bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Execute the migration; idempotent, rerunning converges",
		Long: `migrate runs the pre-flight for a real migration: it connects to both
hosts, re-runs the compatibility gate (the same rules as 'rehost check'),
confirms each source database is reachable, and enforces the
destination-state policy — it refuses to touch a non-empty destination
docroot that rehost did not itself create, unless --onto-existing is given.

The file sync, database import and cutover steps are not wired yet, so a
green pre-flight stops honestly without changing anything on the destination
and exits non-zero (the migration did not happen).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMigrate(cmd, opts, docroots, ontoExisting, del)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	cmd.Flags().BoolVar(&ontoExisting, "onto-existing", false, "converge onto a non-empty destination docroot rehost did not create (no rollback yet)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete destination-only files during file sync (rsync --delete; off by default, sync is additive)")
	return cmd
}

// siteDest pairs a detected source install with the destination docroot
// migrate would populate for it.
type siteDest struct {
	install  detect.Install
	destRoot string
}

// migratePlan is the green-pre-flight hand-off to the sync engine: the live
// connections, the per-site source→destination mapping, the convergence flags,
// and the capability facts decided once from the probe so the sync engine never
// re-probes (compress needs gzip on both ends; nullList needs a GNU source tar).
type migratePlan struct {
	source       transfer.Conn
	dest         transfer.Conn
	sites        []siteDest
	delete       bool
	ontoExisting bool

	compress   bool   // gzip on both hosts — pipe the relay through it
	nullList   bool   // source tar is GNU — feed it a NUL-delimited file list
	srcTarget  string // source user@host, for messages
	destTarget string // destination user@host, keys the persisted dest manifest
	srcHome    string // source account home, for the source-side history record
	destHome   string // destination account home, for the EventMigrate record
	stateDir   string // local .rehost dir next to the project file
}

// syncFn is the file-sync primitive, a package var so tests can substitute a
// fake that captures endpoints/options without a real tar pipe. Production uses
// the internal/transfer engine.
var syncFn = transfer.Sync

func runMigrate(cmd *cobra.Command, opts *options, docroots []string, ontoExisting, del bool) error {
	u := newUI(cmd, opts)

	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	if f.Destination == nil {
		return fmt.Errorf("%s has no destination — migrate needs one; rerun 'rehost init' or add a destination section", opts.projectFile)
	}

	// 1. Connect to both hosts and gather what the gate needs. Source DB
	//    reachability is part of this: gatherHosts extracts each site's
	//    credentials and inspects its database, and the gate blocks on a
	//    database that cannot be reached.
	h, err := gatherHosts(cmd.Context(), u, f, docroots)
	if err != nil {
		if u.mode == tui.ModeJSON {
			u.renderer.Error(err) // keep stdout machine-readable
		}
		return err
	}
	defer h.close()

	// 2. Compatibility gate — the same rules 'rehost check' runs.
	checks := check.Run(h.input)

	// 3. Destination-state policy, per site, over the destination connection.
	sites := migrateSites(f, h.source.installs)
	destState, err := destStateResults(cmd.Context(), h.dest.client, h.dest.caps.Home, sites, ontoExisting)
	if err != nil {
		if u.mode == tui.ModeJSON {
			u.renderer.Error(err)
		}
		return err
	}

	// 4. Combine into one report and decide the outcome. A non-green pre-flight
	//    is terminal: render it standalone (nothing was synced) and return the
	//    refusal/blocker as the exit reason.
	view, outcome := buildPreflight(checks, destState)
	if outcome != nil {
		if err := u.renderer.MigratePreflight(view); err != nil {
			return err
		}
		return outcome
	}

	// 5. Green pre-flight — converge each site's files onto the destination.
	//    Capability facts are decided once here from the probe so the sync
	//    engine never re-probes: compress needs gzip on both ends; nullList
	//    needs a GNU source tar.
	compress, nullList := syncOptions(h.source.caps, h.dest.caps)
	if h.source.caps.Has("tar") && h.source.caps.Tools["tar"].Version == "" {
		// The probe found tar but did not capture its version banner, so its
		// GNU-ness is unknown. Settle it with one cheap check rather than
		// assuming the portable (newline) degradation.
		nullList = sourceTarIsGNU(cmd.Context(), h.source.client)
	}
	plan := migratePlan{
		source:       h.source.client,
		dest:         h.dest.client,
		sites:        sites,
		delete:       del,
		ontoExisting: ontoExisting,
		compress:     compress,
		nullList:     nullList,
		srcTarget:    h.source.caps.Target(),
		destTarget:   h.dest.caps.Target(),
		srcHome:      h.source.caps.Home,
		destHome:     h.dest.caps.Home,
		stateDir:     filepath.Join(filepath.Dir(opts.projectFile), ".rehost"),
	}
	return runSync(cmd.Context(), u, view, plan)
}

// syncOptions decides the two capability-gated Sync options from the two hosts'
// probes: Compress needs gzip on the source and the destination; NullList (a
// NUL-delimited, byte-exact file list) needs a GNU source tar, recognized by
// its version banner. Pure over the probes, so the gating is unit-tested
// directly.
func syncOptions(srcCaps, dstCaps *ssh.Capabilities) (compress, nullList bool) {
	compress = srcCaps.Has("gzip") && dstCaps.Has("gzip")
	nullList = srcCaps.Has("tar") && strings.Contains(srcCaps.Tools["tar"].Version, "GNU")
	return compress, nullList
}

// sourceTarIsGNU is the fallback GNU-tar check when the probe found tar but did
// not capture its version banner: it asks tar itself, once. A transport failure
// or a non-GNU tar both come back false — the safe answer, since a false
// NullList only costs byte-exactness on the rare newline-in-filename.
func sourceTarIsGNU(ctx context.Context, r stateRunner) bool {
	res, err := r.Run(ctx, "tar --version 2>/dev/null | head -n 1")
	if err != nil {
		return false
	}
	return strings.Contains(res.Stdout, "GNU")
}

// runSync converges each site's files onto the destination in order, collects
// the per-site stats, records run history after every site succeeds, renders
// the combined report, and returns the honest-incomplete exit. A sync failure
// stops before the next site and surfaces the error (no history is recorded —
// the run did not converge).
func runSync(ctx context.Context, u ui, preflight tui.MigratePreflightView, p migratePlan) error {
	report := tui.MigrateReportView{Preflight: preflight}

	var syncErr error
	for _, s := range p.sites {
		u.progress("migrate: syncing %s → %s…", s.install.Root, s.destRoot)
		excludes := recipe.ExcludeSuggestionsFor(s.install) // same excludes as dry-run, so deltas line up
		src := transfer.Endpoint{Conn: p.source, Root: s.install.Root, Target: p.srcTarget}
		dst := transfer.Endpoint{Conn: p.dest, Root: s.destRoot, Target: p.destTarget}
		opts := transfer.Options{
			Delete:           p.delete,
			Compress:         p.compress,
			NullList:         p.nullList,
			DestManifestPath: filepath.Join(p.stateDir, "manifests", transfer.DestManifestFilename(p.destTarget, s.destRoot)),
		}
		stats, err := syncFn(ctx, src, dst, excludes, opts, func(msg string) { u.progress("  %s", msg) })
		report.Sites = append(report.Sites, siteSyncResult(s, stats, err))
		if err != nil {
			syncErr = fmt.Errorf("migrate: syncing %s: %w", s.install.Root, err)
			break // stop before the next site; do not record a partial run
		}
	}

	if syncErr != nil {
		if rerr := u.renderer.MigrateReport(report); rerr != nil {
			return rerr
		}
		return syncErr
	}

	// Every site converged. Record the run — a warning, never a migration
	// failure, if it cannot be written.
	report.Warnings = recordMigrateHistory(ctx, u, p, report.Sites)
	report.Notice = migrateIncompleteNotice
	if err := u.renderer.MigrateReport(report); err != nil {
		return err
	}
	return errMigrateIncomplete
}

// siteSyncResult folds one site's stats (possibly nil/partial on error) into
// the report row.
func siteSyncResult(s siteDest, stats *transfer.SyncStats, err error) tui.SiteSyncResult {
	row := tui.SiteSyncResult{Site: s.install.Root, DestRoot: s.destRoot}
	if stats != nil {
		row.Compressed = stats.Compressed
		row.FilesSent = stats.FilesSent
		row.BytesSent = stats.BytesSent
		row.WireBytes = stats.WireBytes
		row.FilesDeleted = stats.FilesDeleted
		row.DestOnlyRemaining = stats.DestOnlyRemaining
		row.UnsafePaths = stats.UnsafePaths
		row.Duration = stats.Duration
	}
	if err != nil {
		row.Err = err.Error()
	}
	return row
}

// recordMigrateHistory writes the run's paper trail after a successful sync and
// returns any warnings (history failures never fail the migration). It records:
//   - one EventMigrate per site on the DESTINATION host keyed by destination
//     docroot — this is what makes the next run's destination-state policy treat
//     the docroot as rehost-created (an idempotent rerun) instead of refusing
//     it. A failed destination record is surfaced prominently because it means
//     the next run will not recognize the docroot and will require
//     --onto-existing.
//   - one summary EventMigrate on the SOURCE host with aggregate stats,
//     consistent with how dry-run leaves its trace on the source.
func recordMigrateHistory(ctx context.Context, u ui, p migratePlan, rows []tui.SiteSyncResult) []string {
	var warnings []string

	for _, row := range rows {
		u.progress("migrate: recording %s on the destination…", row.DestRoot)
		entry := state.Entry{Event: state.EventMigrate, Site: row.DestRoot}
		if err := state.Record(ctx, p.dest, p.destHome, entry); err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"could not record the migration of %s on the destination (%v) — the next run will not recognize it as rehost-created and will need --onto-existing to converge onto it",
				row.DestRoot, err))
		}
	}

	var files, deleted int
	var bytes int64
	for _, row := range rows {
		files += row.FilesSent
		bytes += row.BytesSent
		deleted += row.FilesDeleted
	}
	summary := state.Entry{Event: state.EventMigrate, Details: map[string]string{
		"sites":         strconv.Itoa(len(rows)),
		"files_sent":    strconv.Itoa(files),
		"bytes_sent":    strconv.FormatInt(bytes, 10),
		"files_deleted": strconv.Itoa(deleted),
	}}
	if err := state.Record(ctx, p.source, p.srcHome, summary); err != nil {
		warnings = append(warnings, fmt.Sprintf("could not record the run on the source: %v", err))
	}
	return warnings
}

// migrateSites maps each detected source install to the destination docroot
// migrate would populate: the project file's per-site dest_root when set,
// otherwise the same path on the destination account.
func migrateSites(f *project.File, installs []detect.Install) []siteDest {
	destByRoot := map[string]string{}
	for _, s := range f.Sites {
		destByRoot[s.Root] = s.DestinationRoot()
	}
	sites := make([]siteDest, 0, len(installs))
	for _, inst := range installs {
		dest := destByRoot[inst.Root]
		if dest == "" {
			dest = inst.Root
		}
		sites = append(sites, siteDest{install: inst, destRoot: dest})
	}
	return sites
}

// buildPreflight combines the compatibility-gate results and the
// destination-state decisions into one report view and decides the outcome. It
// dials nothing, so it is unit-tested directly. A nil error means the
// pre-flight is green; the caller then hands off to the sync engine (or, until
// that is wired, stops honestly). A non-nil error is the exit reason and
// leaves view.Passed false.
func buildPreflight(checks, destState []check.Result) (tui.MigratePreflightView, error) {
	results := make([]check.Result, 0, len(checks)+len(destState))
	results = append(results, checks...)
	results = append(results, destState...)
	view := tui.MigratePreflightView{Results: results}

	// A compatibility-gate blocker is the "fix and rerun check" path.
	if b, _ := check.Summarize(checks); b > 0 {
		return view, fmt.Errorf("pre-flight found %d compatibility blocker(s) — fix them and rerun 'rehost check'", b)
	}
	// A destination-state blocker is a refusal to touch a non-empty docroot
	// rehost did not create.
	if refused := blockingRoots(destState); len(refused) > 0 {
		return view, fmt.Errorf("refusing to migrate onto non-empty destination docroot(s) rehost did not create: %s — rerun with --onto-existing to converge onto them anyway (there is no rollback yet)",
			strings.Join(refused, ", "))
	}

	view.Passed = true
	view.Notice = preflightNotice
	return view, nil
}

// blockingRoots returns the destination docroots whose state result is a
// blocker, recovered from each result's id.
func blockingRoots(destState []check.Result) []string {
	var roots []string
	for _, r := range destState {
		if r.Severity == check.Blocker {
			roots = append(roots, strings.TrimPrefix(r.ID, destIDPrefix))
		}
	}
	return roots
}

// destStateResults enforces the destination-state policy for each site:
//   - a missing or empty destination docroot is fine (migrate will create it);
//   - a non-empty docroot rehost itself populated before (an EventMigrate
//     record on the destination names it) is a safe idempotent rerun;
//   - a non-empty docroot rehost did not create is refused (blocker), unless
//     ontoExisting overrides it (warning: there is no rollback yet).
//
// r drives both the docroot stat and the destination history read, so the
// whole policy is unit-tested with a fake runner and no SSH. It never writes
// anything on the destination and never touches the public docroot itself.
func destStateResults(ctx context.Context, r stateRunner, destHome string, sites []siteDest, ontoExisting bool) ([]check.Result, error) {
	if len(sites) == 0 {
		return nil, nil
	}
	entries, err := state.History(ctx, r, destHome)
	if err != nil {
		return nil, fmt.Errorf("reading destination run history: %w", err)
	}
	migrated := state.MigratedSites(entries)

	const title = "Destination docroot"
	var results []check.Result
	for _, s := range sites {
		root := s.destRoot
		id := destIDPrefix + root

		nonEmpty, err := docrootNonEmpty(ctx, r, root)
		if err != nil {
			return nil, fmt.Errorf("checking destination docroot %s: %w", root, err)
		}
		switch {
		case !nonEmpty:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s is empty or absent — rehost will create the site there", root)})
		case migrated[root]:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s is not empty, but rehost migrated it before — converging (idempotent rerun)", root)})
		case ontoExisting:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Warning,
				Detail: fmt.Sprintf("%s is not empty and rehost did not create it — converging because --onto-existing was set; there is no rollback yet, so back up the destination first", root)})
		default:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Blocker,
				Detail: fmt.Sprintf("%s is not empty and rehost has no record of migrating it — refusing to touch it; rerun with --onto-existing to converge onto it anyway", root)})
		}
	}
	return results, nil
}

// docrootNonEmpty reports whether the destination docroot holds anything. A
// missing directory and an empty one both come back false — both are safe to
// create into: `ls -A` lists nothing for an empty directory and errors to an
// empty stdout for a missing one. Any listed entry (file or subdirectory)
// makes it non-empty. Only a transport failure is an error.
func docrootNonEmpty(ctx context.Context, r stateRunner, dir string) (bool, error) {
	res, err := r.Run(ctx, "ls -A -- "+ssh.ShellQuote(dir)+" 2>/dev/null")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}
