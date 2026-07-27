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
	"regexp"
	"sort"
	"strings"

	"github.com/placeholder/rehost/internal/db"
	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/dns"
	"github.com/placeholder/rehost/internal/inventory"
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

	// SourceCreds maps install root → credentials extracted on the source
	// (nil value = extraction failed for that site). nil map = not gathered.
	SourceCreds map[string]*db.Credentials

	// SourceDBs maps install root → inspection made with that site's
	// credentials. nil map = not gathered (no credentials or no mysql
	// client on the source).
	SourceDBs map[string]*db.Inspection

	// Domain is the site's public domain from the project file; "" = none
	// configured. DNS is its snapshot; nil with Domain set = lookup failed.
	Domain string
	DNS    *dns.Snapshot
	// SourceIPs are the addresses the source SSH connection actually
	// reached, for "points at the source" comparisons.
	SourceIPs []string
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
	checkCredentials(in, add)
	checkDBConnect(in, add)
	checkCharset(in, add)
	checkPHP(in, add)
	checkExtensions(in, add)
	checkDisk(in, add)
	checkDNS(in, add)
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

// checkCredentials reports whether the source sites' database credentials
// could be read. Details never include a password — only db name, host and
// the extraction layer that worked.
func checkCredentials(in Input, add addFunc) {
	const title = "Database credentials (source)"
	if !anyNeedsDB(in.Installs) {
		return
	}
	if in.SourceCreds == nil {
		add("db.credentials", title, Info, "not gathered")
		return
	}
	var found, missing []string
	for _, inst := range in.Installs {
		if !recipe.RequirementsFor(inst).NeedsDB {
			continue
		}
		creds := in.SourceCreds[inst.Root]
		if creds == nil || creds.Name == "" {
			missing = append(missing, inst.Root)
			continue
		}
		host := creds.Host
		if host == "" {
			host = "localhost"
		}
		found = append(found, fmt.Sprintf("%s: %s@%s (via %s)", inst.Root, creds.Name, host, creds.Method))
	}
	if len(missing) > 0 {
		add("db.credentials", title, Warning,
			"could not read database credentials for: "+strings.Join(missing, ", ")+
				" — migrate cannot dump these databases until the config is readable")
		return
	}
	add("db.credentials", title, Ok, strings.Join(found, "; "))
}

// checkDBConnect reports whether each site's database answered to its
// extracted credentials. A database that cannot even be reached on the
// source cannot be dumped, so a failure blocks.
func checkDBConnect(in Input, add addFunc) {
	const title = "Database connectivity (source)"
	withCreds := installsWithCreds(in)
	if len(withCreds) == 0 {
		return // nothing to connect to; the credentials rule already reported
	}
	if in.SourceDBs == nil {
		add("db.connect", title, Info, "not inspected — no mysql client on the source")
		return
	}
	var ok, failed []string
	for _, inst := range withCreds {
		insp := in.SourceDBs[inst.Root]
		switch {
		case insp == nil:
			failed = append(failed, inst.Root+": not inspected")
		case !insp.Connected:
			failed = append(failed, inst.Root+": "+insp.Reason)
		default:
			detail := fmt.Sprintf("%s: MySQL %s · %d tables · %s", inst.Root, insp.ServerVersion, insp.Tables, humanKB(insp.SizeKB))
			if insp.Charset != "" {
				detail += " · " + insp.Charset
			}
			ok = append(ok, detail)
		}
	}
	if len(failed) > 0 {
		add("db.connect", title, Blocker, strings.Join(failed, "; "))
		return
	}
	add("db.connect", title, Ok, strings.Join(ok, "; "))
}

// checkCharset flags utf8mb4 usage on the source when the destination's
// MySQL tooling predates it (client version as proxy — the destination
// server is not reachable before migration creates its database).
func checkCharset(in Input, add addFunc) {
	const title = "Character set (utf8mb4)"
	var mb4 int
	for _, insp := range in.SourceDBs {
		if insp != nil {
			mb4 += insp.UTF8MB4Tables
		}
	}
	if mb4 == 0 || !in.Destination.Has("mysql") {
		return // nothing to compare, or db.import already blocked
	}
	version := mysqlToolVersion(in.Destination.Tools["mysql"].Version)
	switch {
	case version == "":
		add("db.charset", title, Info,
			fmt.Sprintf("source uses utf8mb4 (%d tables); could not read the destination MySQL version — verify it is 5.5.3+", mb4))
	case !versionAtLeast(version, "5.5.3"):
		add("db.charset", title, Blocker,
			fmt.Sprintf("source uses utf8mb4 (%d tables) but the destination MySQL %s predates utf8mb4 (5.5.3)", mb4, version))
	default:
		add("db.charset", title, Ok,
			fmt.Sprintf("source uses utf8mb4 (%d tables); destination MySQL %s supports it", mb4, version))
	}
}

// installsWithCreds returns the DB-backed installs whose credentials were
// successfully extracted.
func installsWithCreds(in Input) []detect.Install {
	var out []detect.Install
	for _, inst := range in.Installs {
		if !recipe.RequirementsFor(inst).NeedsDB {
			continue
		}
		if c := in.SourceCreds[inst.Root]; c != nil && c.Name != "" {
			out = append(out, inst)
		}
	}
	return out
}

// mysqlToolVersion pulls the server-ish version out of a mysql client
// version line. MariaDB's client reports its own version first
// ("Ver 15.1 Distrib 10.6.18-MariaDB"), so the last number wins.
func mysqlToolVersion(line string) string {
	matches := versionPattern.FindAllString(line, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

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
	var dbKB int64
	for _, insp := range in.SourceDBs {
		if insp != nil {
			dbKB += insp.SizeKB
		}
	}
	needed := in.SourceSitesKB + dbKB
	what := fmt.Sprintf("%s of site data", humanKB(in.SourceSitesKB))
	if dbKB > 0 {
		what = fmt.Sprintf("%s of site data + %s of database", humanKB(in.SourceSitesKB), humanKB(dbKB))
	}
	switch {
	case in.SourceSitesKB == 0 || in.DestFreeKB == 0:
		add("disk.space", title, Info, "could not measure site size or free space")
	case in.DestFreeKB < needed:
		add("disk.space", title, Blocker,
			fmt.Sprintf("%s needed but only %s is free", what, humanKB(in.DestFreeKB)))
	case in.DestFreeKB < needed*3/2:
		add("disk.space", title, Warning,
			fmt.Sprintf("tight: %s needed, %s free — dumps and temp files need headroom", what, humanKB(in.DestFreeKB)))
	default:
		add("disk.space", title, Ok,
			fmt.Sprintf("%s free for %s", humanKB(in.DestFreeKB), what))
	}
}

// checkDNS reports where the domain points today and whether mail lives on
// the source — the warning that makes a naive full DNS cutover break email.
// rehost never changes DNS (MVP scope guard); these results inform the
// cutover report.
func checkDNS(in Input, add addFunc) {
	if in.Domain == "" {
		add("dns.domain", "DNS snapshot", Info,
			"no domain in migrate.yaml — add 'domain:' to enable DNS checks and the cutover report")
		return
	}
	if in.DNS == nil {
		add("dns.domain", "DNS snapshot", Info,
			fmt.Sprintf("could not look up %s — check the domain spelling and network", in.Domain))
		return
	}

	addrs := in.DNS.Addresses()
	source := map[string]bool{}
	for _, ip := range in.SourceIPs {
		source[ip] = true
	}
	pointsAtSource := false
	for _, a := range addrs {
		if source[a] {
			pointsAtSource = true
		}
	}
	switch {
	case len(addrs) == 0:
		add("dns.domain", "DNS snapshot", Warning,
			fmt.Sprintf("%s has no A/AAAA records — is the domain right?", in.Domain))
	case pointsAtSource:
		add("dns.domain", "DNS snapshot", Ok,
			fmt.Sprintf("%s points at the source (%s)", in.Domain, strings.Join(addrs, ", ")))
	default:
		add("dns.domain", "DNS snapshot", Warning,
			fmt.Sprintf("%s resolves to %s, not the source host (%s) — wrong domain, or a proxy/CDN in front?",
				in.Domain, strings.Join(addrs, ", "), strings.Join(in.SourceIPs, ", ")))
	}

	switch {
	case !in.DNS.HasMX():
		add("dns.mail", "Mail (MX)", Ok, "no MX records — no mail hosting to worry about")
	case in.DNS.MailPointsAt(in.SourceIPs):
		add("dns.mail", "Mail (MX)", Warning,
			"MX points at the source — mail is hosted there and rehost migrates web only; plan mail before changing DNS")
	default:
		add("dns.mail", "Mail (MX)", Ok, "mail is hosted elsewhere — unaffected by this migration")
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
func humanKB(kb int64) string { return inventory.HumanKB(kb) }
