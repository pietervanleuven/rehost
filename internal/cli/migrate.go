package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/db"
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
// report: the gate passed and execution starts now; the remaining unbuilt
// steps are named honestly.
const preflightNotice = "Pre-flight passed — proceeding to file sync, database migration and config rewrite. The cutover report comes later."

// migrateConvergedNotice closes a converged run: the destination matches the
// source; what remains — DNS, mail, SSL, cron — is deliberately manual and
// scripted by the cutover report.
const migrateConvergedNotice = "Migration converged: files, databases and config are on the destination. Next: run 'rehost cutover' for the verified go-live checklist (DNS, mail, SSL, cron) — do not touch DNS before walking it."

func newMigrateCmd(opts *options) *cobra.Command {
	var docroots []string
	var ontoExisting, del bool
	var dbPasswordFile string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Execute the migration; idempotent, rerunning converges",
		Long: `migrate runs the pre-flight — it connects to both hosts, re-runs the
compatibility gate (the same rules as 'rehost check'), confirms each source
database is reachable, and enforces the destination-state policy: a
non-empty destination docroot or dest_db database rehost did not itself
fill is refused unless --onto-existing is given.

When the pre-flight is green it converges each site onto the destination:
files through a manifest-driven tar pipe (additive by default; --delete
removes destination-only files), then — for sites with a dest_db in
migrate.yaml — the database: maintenance mode on the source, a final
verified dump, a file delta pass, a serialized-safe rewrite of docroot
paths, an import into the panel-created destination database (its
password is prompted at runtime, never stored), and a config rewrite
pointing the synced wp-config.php / settings.php at that database
(hash_salt and everything else preserved; 'drush cr' runs when drush is
present). Rerunning converges incrementally: only deltas transfer, the
import re-converges deterministically. A converged run exits 0 — then run
'rehost cutover' for the go-live checklist before touching DNS.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMigrate(cmd, opts, docroots, ontoExisting, del, dbPasswordFile)
		},
	}
	cmd.Flags().StringArrayVar(&docroots, "docroot", nil, "website root(s) to search instead of the account home (repeatable)")
	cmd.Flags().BoolVar(&ontoExisting, "onto-existing", false, "converge onto a non-empty destination docroot rehost did not create (no rollback yet)")
	cmd.Flags().BoolVar(&del, "delete", false, "delete destination-only files during file sync (rsync --delete; off by default, sync is additive)")
	cmd.Flags().StringVar(&dbPasswordFile, "db-password-file", "", "file holding the destination database password (also: REHOST_DB_PASSWORD); enables non-interactive runs")
	return cmd
}

// siteDest pairs a detected source install with the destination docroot
// migrate would populate for it.
type siteDest struct {
	install  detect.Install
	destRoot string
	destDB   *project.SiteDB // nil = files only, database not migrated
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

	srcStream    db.Streamer // dump exec on the source; nil = database step disabled
	destConn     db.Conn     // import sessions on the destination
	srcHost      db.Host     // maintenance toggles on the source
	destHost     db.Host     // config rewrite + post-steps on the destination
	srcCreds     map[string]*db.Credentials
	srcDBs       map[string]*db.Inspection
	destCreds    map[string]*db.Credentials
	srcMysqldump bool // source has mysqldump (else the PHP dump helper)
	destGzip     bool // destination gunzips the relayed dump itself

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

func runMigrate(cmd *cobra.Command, opts *options, docroots []string, ontoExisting, del bool, dbPasswordFile string) error {
	u := newUI(cmd, opts)

	f, err := loadProject(opts.projectFile)
	if err != nil {
		return err
	}
	if err := requireDestination(f, opts.projectFile, "migrate"); err != nil {
		return err
	}

	// 1. Connect to both hosts and gather what the gate needs. Source DB
	//    reachability is part of this: gatherHosts extracts each site's
	//    credentials and inspects its database, and the gate blocks on a
	//    database that cannot be reached.
	h, err := gatherHosts(cmd.Context(), u, f, docroots)
	if err != nil {
		return u.fail(err)
	}
	defer h.close()

	// 1b. Advisory cross-run lock on the destination: two concurrent runs
	//     would interleave tar pipes into the same docroots and race the
	//     history compaction. Released on every exit; a crash leaves a stale
	//     lock whose error message says how to clear it.
	if err := state.AcquireLock(cmd.Context(), h.dest.client, h.dest.caps.Home); err != nil {
		return u.fail(err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 30*time.Second)
		defer cancel()
		if err := state.ReleaseLock(cctx, h.dest.client, h.dest.caps.Home); err != nil {
			u.progress("warning: %v", err)
		}
	}()

	// 2. Compatibility gate — the same rules 'rehost check' runs.
	checks := check.Run(h.input)

	// 3. Destination-state policy, per site, over the destination connection.
	//    The destination run history feeds both the docroot and the database
	//    policy, so it is read once here.
	sites := migrateSites(f, h.source.installs, h.source.caps.Home, h.dest.caps.Home)
	entries, err := state.History(cmd.Context(), h.dest.client, h.dest.caps.Home)
	if err != nil {
		return u.fail(fmt.Errorf("reading destination run history: %w", err))
	}
	migrated := state.MigratedSites(entries)
	migratedDBs := state.MigratedDatabases(entries)
	destState, err := destStateResults(cmd.Context(), h.dest.client, sites, migrated, ontoExisting)
	if err != nil {
		return u.fail(err)
	}

	// 3b. Gate on compatibility and docroot policy BEFORE any password is
	//     asked for: the operator must not type secrets into a run that is
	//     about to refuse. A non-green pre-flight is terminal: render it
	//     standalone (nothing was synced) and return the refusal/blocker as
	//     the exit reason.
	view, outcome := buildPreflight(checks, destState)
	if outcome != nil {
		if err := u.renderer.MigratePreflight(view); err != nil {
			return err
		}
		return outcome
	}

	// 3c. Destination databases, prompted last: the panel passwords (runtime
	//     only, never stored — --db-password-file/REHOST_DB_PASSWORD are the
	//     non-interactive channel) and a verify pass per database — rehost
	//     never runs CREATE DATABASE.
	passwordFn, err := dbPasswordSource(u, dbPasswordFile)
	if err != nil {
		return u.fail(err)
	}
	destCreds, err := destDBCredentials(sites, passwordFn)
	if err != nil {
		return u.fail(err)
	}
	dbState, err := destDBResults(cmd.Context(), h.dest.client, sites, destCreds, migratedDBs, ontoExisting)
	if err != nil {
		return u.fail(err)
	}
	destState = append(destState, dbState...)

	// 4. Re-combine with the database rows and decide the final outcome.
	view, outcome = buildPreflight(checks, destState)
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
	plan := migratePlan{
		source:       h.source.client,
		dest:         h.dest.client,
		srcStream:    h.source.client,
		destConn:     h.dest.client,
		srcHost:      db.Host{Run: h.source.client, FS: detect.NewSSHFS(h.source.client), Caps: h.source.caps},
		destHost:     db.Host{Run: h.dest.client, FS: detect.NewSSHFS(h.dest.client), Caps: h.dest.caps},
		srcCreds:     h.source.creds,
		srcDBs:       h.source.dbs,
		destCreds:    destCreds,
		srcMysqldump: h.source.caps.Has("mysqldump"),
		destGzip:     h.dest.caps.Has("gzip"),
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

// dbPasswordSource returns the password callback for destination databases:
// --db-password-file, then REHOST_DB_PASSWORD, then the interactive prompt.
// The file/env channels apply to every dest_db identity and are what makes a
// --json/automation run possible at all — the non-interactive prompter
// always errors. Nothing is ever written back to migrate.yaml.
func dbPasswordSource(u ui, file string) (func(string) (string, error), error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading --db-password-file: %w", err)
		}
		pw := strings.TrimRight(string(raw), "\r\n")
		return func(string) (string, error) { return pw, nil }, nil
	}
	if pw, ok := os.LookupEnv("REHOST_DB_PASSWORD"); ok {
		return func(string) (string, error) { return pw, nil }, nil
	}
	return u.prompter.Password, nil
}

// syncOptions decides the two capability-gated Sync options from the two hosts'
// probes: Compress needs gzip on the source and the destination; NullList (a
// NUL-delimited, byte-exact file list) needs a GNU source tar, recognized by
// its version banner. A tar with no captured banner counts as non-GNU — the
// safe default, since a false NullList only costs byte-exactness on the rare
// newline-in-filename. Pure over the probes.
func syncOptions(srcCaps, dstCaps *ssh.Capabilities) (compress, nullList bool) {
	compress = srcCaps.Has("gzip") && dstCaps.Has("gzip")
	nullList = srcCaps.Has("tar") && strings.Contains(srcCaps.Tools["tar"].Version, "GNU")
	return compress, nullList
}

// runSync converges each site's files onto the destination in order, collects
// the per-site stats, records run history, renders the combined report, and
// returns the honest-incomplete exit. A sync failure stops before the next
// site and surfaces the error. Each converged site is recorded on the
// destination immediately — if a later site fails, the rerun's
// destination-state policy must still recognize the finished docroots as
// rehost-created, or the promised resume-by-rerun would refuse its own work.
func runSync(ctx context.Context, u ui, preflight tui.MigratePreflightView, p migratePlan) error {
	report := tui.MigrateReportView{Preflight: preflight}

	var syncErr error
	var warnings []string
	succeeded := 0
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
			break
		}
		succeeded++
		warnings = append(warnings, recordSiteMigrated(ctx, u, p, s.destRoot)...)

		// Database choreography, after the files converged (the docroot
		// record above must exist whatever happens next). srcStream is nil
		// only in file-only unit fixtures.
		if p.srcStream != nil {
			dbRes, delta, dbWarnings, dbFailed := migrateSiteDB(ctx, u, p, s, src, dst, opts)
			row := &report.Sites[len(report.Sites)-1]
			row.DB = &dbRes
			foldDelta(row, delta)
			warnings = append(warnings, dbWarnings...)
			if dbFailed {
				syncErr = fmt.Errorf("migrate: database for %s: %s", s.install.Root, dbRes.Err)
				break
			}
		}
	}
	if succeeded > 0 {
		warnings = append(warnings, recordSourceSummary(ctx, p, report.Sites[:succeeded])...)
	}
	report.Warnings = warnings

	if syncErr != nil {
		if rerr := u.renderer.MigrateReport(report); rerr != nil {
			return rerr
		}
		return syncErr
	}
	report.Complete = true
	report.Notice = migrateConvergedNotice
	return u.renderer.MigrateReport(report)
}

// foldDelta adds the maintenance-window delta pass to a site's already
// rendered bulk-sync numbers, so the row reports the whole transfer.
func foldDelta(row *tui.SiteSyncResult, d *transfer.SyncStats) {
	if d == nil {
		return
	}
	row.FilesSent += d.FilesSent
	row.BytesSent += d.BytesSent
	row.WireBytes += d.WireBytes
	row.FilesDeleted += d.FilesDeleted
	row.DestOnlyRemaining = d.DestOnlyRemaining
	row.UnsafePaths += d.UnsafePaths
	row.Duration += d.Duration
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

// recordSiteMigrated writes one EventMigrate on the DESTINATION host keyed by
// destination docroot — what makes the next run's destination-state policy
// treat the docroot as rehost-created (an idempotent rerun) instead of
// refusing it. A failure is a prominent warning, never a migration failure:
// it means the next run will not recognize the docroot and will require
// --onto-existing.
func recordSiteMigrated(ctx context.Context, u ui, p migratePlan, destRoot string) []string {
	u.progress("migrate: recording %s on the destination…", destRoot)
	entry := state.Entry{Event: state.EventMigrate, Site: destRoot}
	if err := state.Record(ctx, p.dest, p.destHome, entry); err != nil {
		return []string{fmt.Sprintf(
			"could not record the migration of %s on the destination (%v) — the next run will not recognize it as rehost-created and will need --onto-existing to converge onto it",
			destRoot, err)}
	}
	_ = state.Compact(ctx, p.dest, p.destHome) // best-effort: bound the history file
	return nil
}

// recordSourceSummary writes one aggregate EventMigrate on the SOURCE host over
// the converged sites, consistent with how dry-run leaves its trace there.
func recordSourceSummary(ctx context.Context, p migratePlan, rows []tui.SiteSyncResult) []string {
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
		return []string{fmt.Sprintf("could not record the run on the source: %v", err)}
	}
	_ = state.Compact(ctx, p.source, p.srcHome) // best-effort: bound the history file
	return nil
}

// migrateSites maps each detected source install to the destination docroot
// migrate would populate: the project file's per-site dest_root when set,
// otherwise the source path re-rooted under the destination home. A source
// root outside the source home has no portable equivalent and keeps its own
// path — set dest_root in migrate.yaml to override.
func migrateSites(f *project.File, installs []detect.Install, srcHome, destHome string) []siteDest {
	byRoot := map[string]project.Site{}
	for _, s := range f.Sites {
		byRoot[s.Root] = s
	}
	sites := make([]siteDest, 0, len(installs))
	for _, inst := range installs {
		cfg := byRoot[inst.Root]
		dest := cfg.DestRoot
		if dest == "" {
			dest = mapDestRoot(inst.Root, srcHome, destHome)
		}
		sites = append(sites, siteDest{install: inst, destRoot: dest, destDB: cfg.DestDB})
	}
	return sites
}

// mapDestRoot rebases a source docroot onto the destination account: the same
// home-relative path under the destination home. The old default — the source's
// absolute path verbatim — pointed a cross-account migration at another user's
// home (unwritable at best, an unserved directory at worst).
func mapDestRoot(srcRoot, srcHome, destHome string) string {
	srcHome = strings.TrimSuffix(srcHome, "/")
	destHome = strings.TrimSuffix(destHome, "/")
	if srcHome == "" || destHome == "" || srcHome == destHome {
		return srcRoot
	}
	if srcRoot == srcHome {
		return destHome
	}
	if rel, ok := strings.CutPrefix(srcRoot, srcHome+"/"); ok {
		return path.Join(destHome, rel)
	}
	return srcRoot
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
		return view, GateError{fmt.Errorf("pre-flight found %d compatibility blocker(s) — fix them and rerun 'rehost check'", b)}
	}
	// A destination-state blocker is a refusal to touch a non-empty docroot
	// rehost did not create.
	if refused := blockingIDs(destState, destIDPrefix); len(refused) > 0 {
		return view, GateError{fmt.Errorf("refusing to migrate onto non-empty destination docroot(s) rehost did not create: %s — rerun with --onto-existing to converge onto them anyway (there is no rollback yet)",
			strings.Join(refused, ", "))}
	}
	// A database blocker: the declared destination database is unusable or
	// would be overwritten.
	if blocked := blockingIDs(destState, destDBIDPrefix); len(blocked) > 0 {
		return view, GateError{fmt.Errorf("destination database not ready for: %s — see the pre-flight rows above", strings.Join(blocked, ", "))}
	}

	view.Passed = true
	view.Notice = preflightNotice
	return view, nil
}

// blockingIDs recovers the identifiers (docroots or site roots) of the
// destination-policy rows with the given id prefix that came back blockers.
func blockingIDs(destState []check.Result, prefix string) []string {
	var ids []string
	for _, r := range destState {
		if r.Severity == check.Blocker && strings.HasPrefix(r.ID, prefix) {
			ids = append(ids, strings.TrimPrefix(r.ID, prefix))
		}
	}
	return ids
}

// destVerdict is the destination-state decision shared by the docroot and
// database policies: refuse-by-default on a non-empty destination rehost has
// no record of filling.
type destVerdict int

const (
	destFresh    destVerdict = iota // empty or absent — safe to create into
	destRerun                       // non-empty, but rehost filled it before (idempotent rerun)
	destOverride                    // non-empty, foreign — converging because --onto-existing
	destRefuse                      // non-empty, foreign — refused
)

func destPolicy(nonEmpty, migratedBefore, ontoExisting bool) destVerdict {
	switch {
	case !nonEmpty:
		return destFresh
	case migratedBefore:
		return destRerun
	case ontoExisting:
		return destOverride
	default:
		return destRefuse
	}
}

// destStateResults enforces the destination-state policy for each site:
//   - a missing or empty destination docroot is fine (migrate will create it);
//   - a non-empty docroot rehost itself populated before (an EventMigrate
//     record on the destination names it) is a safe idempotent rerun;
//   - a non-empty docroot rehost did not create is refused (blocker), unless
//     ontoExisting overrides it (warning: there is no rollback yet).
//
// migrated is the docroot set recovered from the destination's run history
// (the caller reads it once for this and the database policy). r drives the
// docroot stat, so the whole policy is unit-tested with a fake runner and no
// SSH. It never writes anything on the destination and never touches the
// public docroot itself.
func destStateResults(ctx context.Context, r stateRunner, sites []siteDest, migrated map[string]bool, ontoExisting bool) ([]check.Result, error) {
	if len(sites) == 0 {
		return nil, nil
	}
	const title = "Destination docroot"
	var results []check.Result
	for _, s := range sites {
		root := s.destRoot
		id := destIDPrefix + root

		nonEmpty, err := docrootNonEmpty(ctx, r, root)
		if err != nil {
			return nil, fmt.Errorf("checking destination docroot %s: %w", root, err)
		}
		switch destPolicy(nonEmpty, migrated[root], ontoExisting) {
		case destFresh:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s is empty or absent — rehost will create the site there", root)})
		case destRerun:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s is not empty, but rehost migrated it before — converging (idempotent rerun)", root)})
		case destOverride:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Warning,
				Detail: fmt.Sprintf("%s is not empty and rehost did not create it — converging because --onto-existing was set; there is no rollback yet, so back up the destination first", root)})
		case destRefuse:
			results = append(results, check.Result{ID: id, Title: title, Severity: check.Blocker,
				Detail: fmt.Sprintf("%s is not empty and rehost has no record of migrating it — refusing to touch it; rerun with --onto-existing to converge onto it anyway", root)})
		}
	}
	return results, nil
}

// docrootAbsentExit distinguishes "the docroot does not exist" (safe to
// create into) from an ls that failed on an existing directory (refuse to
// guess) without parsing stdout for sentinels.
const docrootAbsentExit = 44

// docrootNonEmpty reports whether the destination docroot holds anything. A
// missing directory and an empty one both come back false — both are safe to
// create into. An existing directory that cannot be listed is an error, not
// "empty": treating it as empty would wave an unreadable-but-writable live
// docroot straight past the refuse-by-default destination-state policy.
func docrootNonEmpty(ctx context.Context, r stateRunner, dir string) (bool, error) {
	q := ssh.ShellQuote(dir)
	res, err := r.Run(ctx, fmt.Sprintf("if [ -e %s ]; then ls -A -- %s; else exit %d; fi", q, q, docrootAbsentExit))
	if err != nil {
		return false, err
	}
	switch {
	case res.ExitCode == docrootAbsentExit:
		return false, nil
	case res.ExitCode != 0:
		return false, fmt.Errorf("cannot inspect %s (exit %d): %s — fix its permissions so rehost can tell whether it is safe to migrate into",
			dir, res.ExitCode, ssh.FirstLine(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}
