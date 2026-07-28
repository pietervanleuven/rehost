package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/inventory"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/ssh"
	"github.com/pietervanleuven/rehost/internal/state"
	"github.com/pietervanleuven/rehost/internal/transfer"
)

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
			manifestResult(ctx, client, caps.Target(), inst, stateDir, add)
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
// proof that reruns are incremental. It emits exactly one result per site:
// consumers key the JSON report by id.
func manifestResult(ctx context.Context, client *ssh.Client, source string, inst detect.Install, stateDir string, add func(id, title string, sev check.Severity, detail string)) {
	site := inst.Root
	id := "dryrun.manifest:" + site
	m, err := transfer.TakeManifest(ctx, client, inst.Root, recipe.ExcludeSuggestionsFor(inst))
	if err != nil {
		add(id, "File manifest", check.Warning, fmt.Sprintf("%s: %v", site, err))
		return
	}
	manifestPath := filepath.Join(stateDir, "manifests", transfer.ManifestFilename(source, inst.Root))
	sev := check.Ok
	prev, prevErr := transfer.LoadManifest(manifestPath)

	detail := fmt.Sprintf("%s: %d files", site, len(m.Files))
	if m.Complete {
		detail += ", " + inventory.HumanKB(m.TotalBytes()/1024)
	} else {
		detail += " (paths only — no GNU find, deltas degrade to presence)"
	}
	switch {
	case prevErr != nil:
		detail += fmt.Sprintf(" — previous manifest unreadable (%v), delta lost, baseline reset", prevErr)
		sev = check.Warning
	case prev == nil:
		detail += " — first manifest saved"
	default:
		d := transfer.Diff(prev, m)
		detail += fmt.Sprintf(" — since last run: %d to transfer (+%d new, %d changed), %d removed, %d unchanged",
			d.Total(), len(d.Added), len(d.Changed), len(d.Removed), d.Unchanged)
	}
	if err := transfer.SaveManifest(m, manifestPath); err != nil {
		detail += fmt.Sprintf("; saving manifest failed: %v", err)
		sev = check.Warning
	}
	add(id, "File manifest", sev, detail)
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
