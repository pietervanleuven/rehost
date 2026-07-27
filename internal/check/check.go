// Package check is the compatibility gate between source and destination:
// it turns probed capabilities, detected installs and a few measurements into
// a list of pass/warn/block results. It is pure — all remote gathering
// happens in gather.go or in the caller — so every rule is unit-testable.
//
// The model is blockers vs warnings: a blocker means migration cannot work
// (rerun check until green); a warning means it can, with caveats.
package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/recipe"
	"github.com/placeholder/rehost/internal/ssh"
)

// Severity classifies one result.
type Severity string

const (
	Ok      Severity = "ok"
	Info    Severity = "info"    // could not be determined, or worth knowing
	Warning Severity = "warning" // migration works, with caveats
	Blocker Severity = "blocker" // migration cannot work until fixed
)

// Result is one line of the check report.
type Result struct {
	ID       string   `json:"id"` // stable machine identifier, e.g. "php.version"
	Title    string   `json:"title"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail,omitempty"`
}

// Input is everything the rules look at. Zero/nil measurement fields mean
// "unknown" and downgrade the affected rule to Info, never to a false pass.
type Input struct {
	Source      *ssh.Capabilities
	Destination *ssh.Capabilities
	Installs    []detect.Install // detected on the source

	DestPHPExtensions []string // php -m on the destination; nil = unknown
	SourceSitesKB     int64    // total size of the detected install roots; 0 = unknown
	DestFreeKB        int64    // free space at the destination home; 0 = unknown
}

// Run evaluates every rule in stable order.
func Run(in Input) []Result {
	var results []Result
	add := func(id, title string, sev Severity, detail string) {
		results = append(results, Result{ID: id, Title: title, Severity: sev, Detail: detail})
	}

	checkSites(in, add)
	checkTransfer(in, add)
	checkDatabase(in, add)
	checkPHP(in, add)
	checkExtensions(in, add)
	checkDisk(in, add)
	return results
}

// Summarize counts what stands between the user and a green check.
func Summarize(results []Result) (blockers, warnings int) {
	for _, r := range results {
		switch r.Severity {
		case Blocker:
			blockers++
		case Warning:
			warnings++
		}
	}
	return blockers, warnings
}

type addFunc func(id, title string, sev Severity, detail string)

func checkSites(in Input, add addFunc) {
	const title = "Websites on the source"
	if len(in.Installs) == 0 {
		add("sites", title, Warning,
			"nothing detected — rerun 'rehost plan --docroot <path>' to point the scan at the site")
		return
	}
	counts := map[string]int{}
	for _, inst := range in.Installs {
		counts[inst.Framework]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%d %s", counts[name], name))
	}
	add("sites", title, Ok, strings.Join(parts, ", "))
}

func checkTransfer(in Input, add addFunc) {
	const title = "File transfer strategy"
	switch {
	case in.Source.Has("rsync") && in.Destination.Has("rsync"):
		add("transfer.files", title, Ok, "rsync on both hosts — incremental delta sync")
	case in.Source.Has("tar") && in.Destination.Has("tar"):
		detail := "tar stream over SSH"
		if in.Source.Has("gzip") && in.Destination.Has("gzip") {
			detail += " (gzip-compressed)"
		}
		add("transfer.files", title, Info, detail+" — no rsync, reruns re-copy more than a delta sync would")
	default:
		add("transfer.files", title, Warning,
			"no rsync or tar available on both hosts — only the slow SFTP fallback is possible")
	}
	if !in.Source.Has("find") {
		add("transfer.find", "File inventory", Warning,
			"'find' is missing on the source — building the file manifest for incremental reruns will be slow")
	}
}

func checkDatabase(in Input, add addFunc) {
	if !anyNeedsDB(in.Installs) {
		return
	}
	if in.Source.Has("mysqldump") {
		add("db.dump", "Database dump (source)", Ok, "mysqldump available")
	} else {
		add("db.dump", "Database dump (source)", Warning,
			"mysqldump is missing on the source — rehost will fall back to a slower PHP dump helper")
	}
	if in.Destination.Has("mysql") {
		add("db.import", "Database import (destination)", Ok, "mysql client available")
	} else {
		add("db.import", "Database import (destination)", Blocker,
			"the mysql client is missing on the destination — the database cannot be imported")
	}
}

func checkPHP(in Input, add addFunc) {
	const title = "PHP on the destination"
	minPHP, needer := strictestMinPHP(in.Installs)
	if minPHP == "" {
		return // only static sites — PHP not needed
	}
	if in.Destination.PHPVersion == "" {
		add("php.version", title, Blocker,
			fmt.Sprintf("PHP was not found, but %s needs at least PHP %s", needer, minPHP))
		return
	}
	if !versionAtLeast(in.Destination.PHPVersion, minPHP) {
		add("php.version", title, Blocker,
			fmt.Sprintf("PHP %s is older than the %s %s needs", in.Destination.PHPVersion, minPHP, needer))
		return
	}
	add("php.version", title, Ok,
		fmt.Sprintf("PHP %s ≥ %s required by %s", in.Destination.PHPVersion, minPHP, needer))

	if in.Source.PHPVersion != "" && majorOf(in.Source.PHPVersion) != majorOf(in.Destination.PHPVersion) {
		add("php.drift", "PHP major version drift", Info,
			fmt.Sprintf("source runs PHP %s, destination %s — test the site after migration",
				in.Source.PHPVersion, in.Destination.PHPVersion))
	}
}

func checkExtensions(in Input, add addFunc) {
	const title = "PHP extensions on the destination"
	minPHP, _ := strictestMinPHP(in.Installs)
	if minPHP == "" || in.Destination.PHPVersion == "" {
		return // no PHP need, or already blocked by php.version
	}
	if in.DestPHPExtensions == nil {
		add("php.extensions", title, Info, "could not list extensions (php -m failed)")
		return
	}
	have := map[string]bool{}
	for _, ext := range in.DestPHPExtensions {
		have[strings.ToLower(ext)] = true
	}
	var missingRequired, missingRecommended []string
	seenReq, seenRec := map[string]bool{}, map[string]bool{}
	for _, inst := range in.Installs {
		req := recipe.RequirementsFor(inst)
		for _, ext := range req.RequiredExt {
			if !have[strings.ToLower(ext)] && !seenReq[ext] {
				seenReq[ext] = true
				missingRequired = append(missingRequired, ext)
			}
		}
		for _, ext := range req.RecommendedExt {
			if !have[strings.ToLower(ext)] && !seenRec[ext] {
				seenRec[ext] = true
				missingRecommended = append(missingRecommended, ext)
			}
		}
	}
	sort.Strings(missingRequired)
	sort.Strings(missingRecommended)
	switch {
	case len(missingRequired) > 0:
		add("php.extensions", title, Blocker,
			"required extension(s) missing: "+strings.Join(missingRequired, ", "))
	case len(missingRecommended) > 0:
		add("php.extensions", title, Warning,
			"recommended extension(s) missing: "+strings.Join(missingRecommended, ", "))
	default:
		add("php.extensions", title, Ok, "all required and recommended extensions present")
	}
}

func checkDisk(in Input, add addFunc) {
	const title = "Disk space on the destination"
	switch {
	case in.SourceSitesKB == 0 || in.DestFreeKB == 0:
		add("disk.space", title, Info, "could not measure site size or free space")
	case in.DestFreeKB < in.SourceSitesKB:
		add("disk.space", title, Blocker,
			fmt.Sprintf("sites need %s but only %s is free", humanKB(in.SourceSitesKB), humanKB(in.DestFreeKB)))
	case in.DestFreeKB < in.SourceSitesKB*3/2:
		add("disk.space", title, Warning,
			fmt.Sprintf("tight: sites need %s, %s free — dumps and temp files need headroom", humanKB(in.SourceSitesKB), humanKB(in.DestFreeKB)))
	default:
		add("disk.space", title, Ok,
			fmt.Sprintf("%s free for %s of site data", humanKB(in.DestFreeKB), humanKB(in.SourceSitesKB)))
	}
}

func anyNeedsDB(installs []detect.Install) bool {
	for _, inst := range installs {
		if recipe.RequirementsFor(inst).NeedsDB {
			return true
		}
	}
	return false
}

// strictestMinPHP returns the highest minimum PHP version any install needs
// and a label for who needs it ("drupal 10.2.6"), or "" if none needs PHP.
func strictestMinPHP(installs []detect.Install) (minPHP, needer string) {
	for _, inst := range installs {
		req := recipe.RequirementsFor(inst)
		if req.MinPHP == "" {
			continue
		}
		if minPHP == "" || versionAtLeast(req.MinPHP, minPHP) && req.MinPHP != minPHP {
			minPHP = req.MinPHP
			needer = strings.TrimSpace(inst.Framework + " " + inst.Version)
		}
	}
	return minPHP, needer
}

// humanKB renders a KiB count for humans.
func humanKB(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%.1f MiB", float64(kb)/1024)
	default:
		return fmt.Sprintf("%d KiB", kb)
	}
}
