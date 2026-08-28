package cli

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/searchreplace"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/transfer"
	"github.com/pietervanleuven/rehost/internal/tui"
)

// destDBIDPrefix keys each destination-database pre-flight row by the site's
// source root, parallel to destIDPrefix for docroots.
const destDBIDPrefix = "migrate.destdb:"

// Seams for the database choreography, package vars so tests can substitute
// fakes the same way syncFn works.
var (
	dumpFn    = db.Dump
	dumpPHPFn = db.DumpPHP
	importFn  = db.Import
	inspectFn = db.Inspect
)

// dbIdentity keys credential prompting and inspection so sites sharing one
// panel database are asked about and probed once. Host and port are
// normalized first: "" vs localhost and 0 vs 3306 spell the same database,
// and a cosmetic migrate.yaml edit must not make a rerun refuse its own
// prior import.
func dbIdentity(c *project.SiteDB) string {
	return identityKey(c.Name, c.User, c.Host, c.Port)
}

// dbCredIdentity is dbIdentity over resolved credentials. destDBResults gates
// its overwrite guard on this, and migrateSiteDB records it after a successful
// import, so the two agree on which database rehost has filled.
func dbCredIdentity(c *db.Credentials) string {
	return identityKey(c.Name, c.User, c.Host, c.Port)
}

func identityKey(name, user, host string, port int) string {
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = 3306
	}
	return name + "\x00" + user + "\x00" + host + "\x00" + strconv.Itoa(port)
}

// destDBCredentials resolves each dest_db site to runtime credentials,
// prompting once per distinct database identity. The password never touches
// migrate.yaml — this prompt is its only source.
func destDBCredentials(sites []siteDest, password func(string) (string, error)) (map[string]*db.Credentials, error) {
	prompted := map[string]string{}
	var out map[string]*db.Credentials
	for _, s := range sites {
		cfg := s.destDB
		if cfg == nil {
			continue
		}
		id := dbIdentity(cfg)
		pw, seen := prompted[id]
		if !seen {
			label := cfg.Name
			if cfg.User != "" {
				label = cfg.User + "@" + cfg.Name
			}
			var err error
			pw, err = password("Password for destination database " + label)
			if err != nil {
				return nil, fmt.Errorf("destination database %s: %w — the panel-created database's password is prompted at runtime; run migrate interactively, or remove dest_db to migrate files only", cfg.Name, err)
			}
			prompted[id] = pw
		}
		if out == nil {
			out = map[string]*db.Credentials{}
		}
		out[s.install.Root] = &db.Credentials{
			Name: cfg.Name, User: cfg.User, Host: cfg.Host, Port: cfg.Port, Password: pw,
		}
	}
	return out, nil
}

// destDBResults verifies each declared destination database over the
// destination connection — verify, never create (PLAN.md §7) — and applies
// the same refuse-by-default policy as docroots: a non-empty database rehost
// has no record of filling is refused unless --onto-existing, because the
// import's DROP TABLE statements overwrite it. A site without dest_db gets a
// warning row: files will sync, the database will not.
func destDBResults(ctx context.Context, r db.Runner, sites []siteDest, creds map[string]*db.Credentials, migratedDBs map[string]bool, ontoExisting bool) ([]check.Result, error) {
	const title = "Destination database"
	inspected := map[string]*db.Inspection{}
	var rows []check.Result
	for _, s := range sites {
		root := s.install.Root
		id := destDBIDPrefix + root
		c := creds[root]
		if c == nil {
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Warning,
				Detail: fmt.Sprintf("%s has no dest_db in migrate.yaml — files will sync, the database will NOT be migrated", root)})
			continue
		}
		key := dbCredIdentity(c)
		insp, seen := inspected[key]
		if !seen {
			var err error
			insp, err = inspectFn(ctx, r, c)
			if err != nil {
				return nil, fmt.Errorf("inspecting destination database %s: %w", c.Name, err)
			}
			inspected[key] = insp
		}
		switch verdict := destPolicy(insp.Tables > 0, migratedDBs[key], ontoExisting); {
		case !insp.Connected:
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Blocker,
				Detail: fmt.Sprintf("cannot use %s: %s — create the database in the hosting panel and check dest_db's name/user, then rerun", c.Name, insp.Reason)})
		case verdict == destRefuse:
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Blocker,
				Detail: fmt.Sprintf("%s already holds %d tables and rehost has no record of filling it — the import would overwrite them; empty the database or rerun with --onto-existing", c.Name, insp.Tables)})
		case verdict == destOverride:
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Warning,
				Detail: fmt.Sprintf("%s holds %d tables — overwriting because --onto-existing was set; there is no rollback yet", c.Name, insp.Tables)})
		case verdict == destRerun:
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s reachable (MySQL %s) — %d tables from a previous rehost run, import re-converges them", c.Name, insp.ServerVersion, insp.Tables)})
		default: // destFresh
			rows = append(rows, check.Result{ID: id, Title: title, Severity: check.Ok,
				Detail: fmt.Sprintf("%s reachable (MySQL %s)", c.Name, insp.ServerVersion)})
		}
	}
	return rows, nil
}

// migrateSiteDB runs one site's database choreography after its bulk file
// sync converged: maintenance on (write-ahead record first) → final dump →
// file delta pass → serialized-safe rewrite of the dump → import + verify —
// with maintenance lifted on every exit path. A skipped database (no
// dest_db, no source credentials) is a described outcome, not an error;
// failed is true only when a step the site depends on broke.
func migrateSiteDB(ctx context.Context, u ui, p migratePlan, s siteDest, src, dst transfer.Endpoint, opts transfer.Options) (d tui.SiteDBResult, delta *transfer.SyncStats, warnings []string, failed bool) {
	root := s.install.Root
	destCreds := p.destCreds[root]
	if destCreds == nil {
		d.Skipped = "no dest_db configured"
		return d, nil, nil, false
	}
	d.Name = destCreds.Name
	srcCreds := p.srcCreds[root]
	if srcCreds == nil {
		d.Skipped = "source database credentials unavailable"
		return d, nil, []string{fmt.Sprintf("%s: dest_db is set but the source credentials could not be extracted — database not migrated", root)}, false
	}

	// Maintenance window. The lock record precedes the lock so a crash
	// between the two still leaves a trace for unlock's history hint; the
	// live probe corrects any overshoot.
	maintainer := recipe.MaintainerFor(s.install.Framework)
	locked := false
	if maintainer == nil {
		d.Maintenance = "unavailable: unknown framework"
		warnings = append(warnings, fmt.Sprintf("%s: no maintenance strategy — dumping the live site; writes during the window may be lost", root))
	} else {
		if err := state.Record(ctx, p.source, p.srcHome, state.MaintenanceEntry(root, true)); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not write the maintenance lock record (%v)", root, err))
		}
		res, err := maintainer.EnableMaintenance(ctx, p.srcHost, s.install)
		switch {
		case errors.Is(err, recipe.ErrMaintenanceTool):
			d.Maintenance = "failed: " + err.Error()
			warnings = append(warnings, fmt.Sprintf("%s: maintenance mode could not be enabled — dumping the live site; writes during the window may be lost", root))
		case err != nil:
			d.Err = fmt.Sprintf("enabling maintenance mode: %v", err)
			return d, nil, warnings, true
		case !res.Supported:
			d.Maintenance = "unsupported: " + res.Note
			warnings = append(warnings, fmt.Sprintf("%s: %s — dumping the live site", root, res.Note))
		default:
			d.Maintenance = res.Method
			locked = true
		}
	}
	defer func() {
		if maintainer == nil {
			return
		}
		// Lift maintenance on every exit path, on a context that survives
		// cancellation — a Ctrl-C mid-import must not strand the source in
		// maintenance mode. Disable is safe when never enabled.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		if _, err := maintainer.DisableMaintenance(cctx, p.srcHost, s.install); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not clear maintenance mode (%v) — run 'rehost unlock'", root, err))
			return
		}
		if locked {
			u.progress("  maintenance mode off")
		}
		if err := state.Record(cctx, p.source, p.srcHome, state.MaintenanceEntry(root, false)); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not record the maintenance unlock (%v)", root, err))
		}
	}()
	if locked {
		u.progress("  maintenance mode on (%s)", d.Maintenance)
	}

	// Final dump, inside the window, into the same .rehost/dumps/ layout the
	// dry run uses. The inspected charset rides along so the dump connection
	// matches how the data is actually stored.
	if insp := p.srcDBs[root]; insp != nil && insp.Charset != "" && srcCreds.Charset == "" {
		c := *srcCreds
		c.Charset = insp.Charset
		srcCreds = &c
	}
	u.progress("  dumping %s…", srcCreds.Name)
	dumpPath, stats, err := dumpForImport(ctx, p, srcCreds)
	if err != nil {
		d.Err = fmt.Sprintf("dumping %s: %v", srcCreds.Name, err)
		return d, nil, warnings, true
	}
	d.DumpSQLBytes = stats.Bytes

	// Delta file pass: whatever changed on the live site between the bulk
	// sync and the maintenance lock.
	u.progress("  file delta pass…")
	delta, err = syncFn(ctx, src, dst, recipe.ExcludeSuggestionsFor(s.install), opts, func(msg string) { u.progress("    %s", msg) })
	if err != nil {
		d.Err = fmt.Sprintf("delta file pass: %v", err)
		return d, delta, warnings, true
	}

	// Local dump finalize, before the data reaches the destination: strip
	// DEFINER clauses (always — a cross-account import aborts on a foreign
	// definer) and apply the serialized-safe docroot rewrite. Same-domain
	// migrations yield no docroot pairs, but the DEFINER pass still runs.
	pairs := searchreplace.Pairs(searchreplace.PlanInput{SourceDocroot: root, DestDocroot: s.destRoot})
	if len(pairs) > 0 {
		u.progress("  rewriting paths in the dump…")
	}
	rstats, err := finalizeDumpFile(dumpPath, pairs)
	if err != nil {
		d.Err = fmt.Sprintf("finalizing the dump: %v", err)
		return d, delta, warnings, true
	}
	d.Replacements = rstats.ValuesChanged
	d.Unparseable = rstats.Unparseable
	if rstats.Unparseable > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: %d serialized-looking values could not be parsed and were left untouched — inspect them after cutover", root, rstats.Unparseable))
	}

	// Import + verification.
	charset := ""
	if insp := p.srcDBs[root]; insp != nil {
		charset = insp.Charset
	}
	lastPct := -1
	res, err := importFn(ctx, p.destConn, destCreds, dumpPath, db.ImportOptions{
		RemoteGunzip: p.destGzip,
		Charset:      charset,
		Progress: func(sent, total int64) {
			if total <= 0 {
				return
			}
			if pct := int(sent * 100 / total); pct >= lastPct+25 || (pct == 100 && lastPct != 100) {
				lastPct = pct
				u.progress("  importing into %s… %d%%", destCreds.Name, pct)
			}
		},
	})
	if err != nil {
		d.Err = fmt.Sprintf("importing into %s: %v", destCreds.Name, err)
		return d, delta, warnings, true
	}
	d.SourceTables = res.SourceTables
	d.DestTables = res.DestTables
	if res.DestTables < res.SourceTables {
		warnings = append(warnings, fmt.Sprintf("%s: destination has %d tables but the dump carried %d — inspect before cutover", destCreds.Name, res.DestTables, res.SourceTables))
	}

	// Record that this destination database is now rehost-filled, keyed by its
	// identity, so a rerun re-converges it instead of refusing it — and, unlike
	// the docroot record, only after the import actually ran. A failed record is
	// a warning: the next run would need --onto-existing, never data loss.
	if err := state.Record(ctx, p.dest, p.destHome, state.DatabaseMigratedEntry(dbCredIdentity(destCreds))); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: could not record the database migration on the destination (%v) — the next run will need --onto-existing to re-import into %s", root, err, destCreds.Name))
	}

	// Config rewrite: point the synced config at the imported database. An
	// unsupported rewrite degrades to guidance — files and data converged,
	// the user can edit one file by hand — but a transport failure is real.
	rw := recipe.RewriterFor(s.install.Framework)
	switch {
	case rw == nil:
		d.ConfigNote = "no config-rewrite strategy for " + s.install.Framework + " — point its config at " + destCreds.Name + " by hand"
		warnings = append(warnings, fmt.Sprintf("%s: %s", root, d.ConfigNote))
	case s.install.ConfigFile == "":
		d.ConfigNote = "no config file detected — point the site at " + destCreds.Name + " by hand"
		warnings = append(warnings, fmt.Sprintf("%s: %s", root, d.ConfigNote))
	default:
		u.progress("  rewriting the destination config…")
		res, err := rw.RewriteConfig(ctx, p.destHost, recipe.ConfigRewrite{
			SourceConfig: s.install.ConfigFile,
			SourceRoot:   root,
			DestRoot:     s.destRoot,
			DB:           *destCreds,
		})
		switch {
		case err != nil:
			d.Err = fmt.Sprintf("rewriting the destination config: %v", err)
			return d, delta, warnings, true
		case !res.Supported:
			d.ConfigNote = res.Note
			warnings = append(warnings, fmt.Sprintf("%s: config not rewritten — %s", root, res.Note))
		default:
			d.ConfigPath = res.Path
			d.PostSteps = res.PostSteps
		}
	}

	// The destination must never be left in maintenance mode. WordPress's
	// .maintenance file is excluded from the sync, but Drupal's flag rides in
	// the dumped database, so clear it on the destination now that the config
	// points there. Best-effort: a failure is a warning and one manual command,
	// never a failed migration. Skipped when the config was not rewritten —
	// without it the destination config still points at the source DB, so a
	// clear could not reach the right database.
	if maintainer != nil && d.ConfigPath != "" {
		destInstall := s.install
		destInstall.Root = s.destRoot
		destInstall.ConfigFile = d.ConfigPath
		res, err := maintainer.DisableMaintenance(ctx, p.destHost, destInstall)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("%s: could not clear maintenance mode on the destination (%v) — clear it there by hand before cutover", s.destRoot, err))
		case !res.Supported:
			warnings = append(warnings, fmt.Sprintf("%s: could not clear maintenance mode on the destination — %s; clear it there by hand before cutover", s.destRoot, res.Note))
		}
	}
	return d, delta, warnings, false
}

// dumpForImport streams one footer-verified dump (mysqldump, or the PHP
// helper when the source lacks it) to .rehost/dumps/<db>.sql.gz. A failed
// verification removes the file so a truncated dump can never be imported.
func dumpForImport(ctx context.Context, p migratePlan, creds *db.Credentials) (string, *db.DumpStats, error) {
	dir := filepath.Join(p.stateDir, "dumps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, dumpFileName(creds.Name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", nil, err
	}
	dump := dumpFn
	if !p.srcMysqldump {
		dump = dumpPHPFn
	}
	stats, dumpErr := dump(ctx, p.srcStream, creds, f)
	closeErr := f.Close()
	if dumpErr == nil {
		dumpErr = closeErr
	}
	if dumpErr != nil {
		_ = os.Remove(path)
		return "", stats, dumpErr
	}
	return path, stats, nil
}

// finalizeDumpFile post-processes a gzipped dump in place (temp file + rename,
// so an interrupt leaves the original intact): it strips DEFINER clauses from
// the DDL, then applies the docroot replacement pairs inside string literals.
// Both stages run over one gunzip→gzip pass; empty pairs still get the DEFINER
// strip.
func finalizeDumpFile(path string, pairs []searchreplace.Pair) (*searchreplace.Stats, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gzIn, err := gzip.NewReader(in)
	if err != nil {
		_ = in.Close()
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".rewrite-*.sql.gz.tmp")
	if err != nil {
		_ = gzIn.Close()
		_ = in.Close()
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = gzIn.Close()
		_ = in.Close()
		return nil, err
	}
	gzOut := gzip.NewWriter(tmp)

	// DEFINER stripping feeds the rewrite through a pipe so the two stages
	// stream rather than buffering the whole dump. A strip-side error closes
	// the pipe with it, surfacing on the rewrite's read.
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(db.StripDefiners(gzIn, pw)) }()
	stats, err := searchreplace.RewriteDump(pr, gzOut, pairs)
	_ = pr.CloseWithError(err)
	if err == nil {
		err = gzOut.Close()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	// The source must be closed before the rename: Windows refuses to
	// replace a file that is still open.
	_ = gzIn.Close()
	_ = in.Close()
	if err != nil {
		return stats, err
	}
	return stats, os.Rename(tmp.Name(), path)
}
