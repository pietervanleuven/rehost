package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// destIDPrefix keys each destination-state result by its docroot so the
// refusal error can name the offending docroots without a parallel structure.
const destIDPrefix = "migrate.dest:"

// preflightNotice is the honest-stop message a green pre-flight prints: the
// migration itself does not run yet because the execution steps are not wired.
const preflightNotice = "Pre-flight passed. File sync, database import and cutover are not wired yet, so nothing on the destination was changed. This is Phase 3 work — see docs/PLAN.md §6."

// errPreflightOnly is the non-zero exit a green pre-flight returns: the checks
// passed but the migration deliberately did not happen.
var errPreflightOnly = errors.New("pre-flight passed but the migration did not run: file sync, database import and cutover are not wired yet")

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
	cmd.Flags().BoolVar(&del, "delete", false, "delete destination files with no source counterpart (rsync-style; additive by default)")
	return cmd
}

// siteDest pairs a detected source install with the destination docroot
// migrate would populate for it.
type siteDest struct {
	install  detect.Install
	destRoot string
}

// migratePlan is the green-pre-flight hand-off to the sync engine: the live
// connections, the per-site source→destination mapping, and the convergence
// flags. It is the single argument the Phase 3 transfer wiring consumes.
type migratePlan struct {
	source       *ssh.Client
	dest         *ssh.Client
	sites        []siteDest
	delete       bool
	ontoExisting bool
}

// runSync is the single seam Phase 3 wiring fills to converge each site onto
// the destination (files, database import, config rewrite, cutover). It is nil
// today, so a green pre-flight stops honestly without changing anything on the
// destination. The parent session assigns the internal/transfer-backed engine
// here; nothing else in this package touches it.
var runSync func(ctx context.Context, p migratePlan) error

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

	// 4. Combine into one report and decide the outcome.
	view, outcome := buildPreflight(checks, destState)
	if err := u.renderer.MigratePreflight(view); err != nil {
		return err
	}
	if outcome != nil {
		return outcome
	}

	// 5. Green pre-flight. Hand off to the sync engine when it exists; until
	//    then, stop honestly — nothing on the destination changed.
	plan := migratePlan{
		source:       h.source.client,
		dest:         h.dest.client,
		sites:        sites,
		delete:       del,
		ontoExisting: ontoExisting,
	}
	if runSync == nil {
		return errPreflightOnly
	}
	return runSync(cmd.Context(), plan)
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
