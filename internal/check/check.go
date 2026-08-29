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

	"github.com/pietervanleuven/go-dns"
	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/inventory"
	"github.com/pietervanleuven/rehost/internal/recipe"
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
	Source      *remote.Capabilities
	Destination *remote.Capabilities
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

	// DestDBs maps install root → true when migrate.yaml names a dest_db
	// for that site. nil map = not gathered (the rule stays silent); an
	// empty map means gathered and nothing configured.
	DestDBs map[string]bool

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
	checkMultisite(in, add)
	checkTransfer(in, add)
	checkDatabase(in, add)
	checkEngine(in, add)
	checkDestDB(in, add)
	checkCredentials(in, add)
	checkDBConnect(in, add)
	checkCharset(in, add)
	checkPHP(in, add)
	checkExtensions(in, add)
	checkDisk(in, add)
	checkDNS(in, add)
	checkDNSTTL(in, add)
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

// checkTransfer mirrors what internal/transfer actually does: every sync is
// a manifest-driven tar pipe, so tar and find are needed on both hosts and
// there is no other transport to fall back to.
func checkTransfer(in Input, add addFunc) {
	const title = "File transfer strategy"
	if missing := hostsMissing(in, "tar"); missing != "" {
		add("transfer.files", title, Blocker,
			"tar is missing on the "+missing+" — rehost streams files through a tar pipe over SSH and has no other transport; migrate cannot sync files")
	} else {
		detail := "tar pipe over SSH — manifest-driven, reruns transfer only the delta"
		if in.Source.Has("gzip") && in.Destination.Has("gzip") {
			detail += " (gzip-compressed)"
		}
		add("transfer.files", title, Ok, detail)
	}
	if missing := hostsMissing(in, "find"); missing != "" {
		add("transfer.find", "File inventory", Blocker,
			"'find' is missing on the "+missing+" — the file manifests that drive the sync cannot be built; migrate cannot tell what to transfer")
	}
}

// hostsMissing names the hosts lacking a tool ("source", "destination",
// "source and destination"), or "" when both have it.
func hostsMissing(in Input, tool string) string {
	var missing []string
	if !in.Source.Has(tool) {
		missing = append(missing, "source")
	}
	if !in.Destination.Has(tool) {
		missing = append(missing, "destination")
	}
	return strings.Join(missing, " and ")
}

// installDriver returns the normalized driver of an install, read from its
// extracted credentials; a site whose extraction failed defaults to mysql,
// the shared-hosting overwhelming default.
func installDriver(in Input, inst detect.Install) string {
	if c := in.SourceCreds[inst.Root]; c != nil {
		return db.NormalizeDriver(c.Driver)
	}
	return db.DriverMySQL
}

// neededDrivers partitions the DB-backed installs by driver family.
func neededDrivers(in Input) (mysqlSites, pgSites []string) {
	for _, inst := range in.Installs {
		if !recipe.RequirementsFor(inst).NeedsDB {
			continue
		}
		if installDriver(in, inst) == db.DriverPostgres {
			pgSites = append(pgSites, inst.Root)
		} else {
			mysqlSites = append(mysqlSites, inst.Root)
		}
	}
	return mysqlSites, pgSites
}

// destMySQLTool returns the destination's mysql-family client: hosts with
// modern MariaDB packages ship `mariadb` without the mysql-named symlink.
func destMySQLTool(in Input) (remote.Tool, bool) {
	if in.Destination.Has("mysql") {
		return in.Destination.Tools["mysql"], true
	}
	if in.Destination.Has("mariadb") {
		return in.Destination.Tools["mariadb"], true
	}
	return remote.Tool{}, false
}

func checkDatabase(in Input, add addFunc) {
	mysqlSites, pgSites := neededDrivers(in)
	if len(mysqlSites) > 0 {
		switch {
		case in.Source.Has("mysqldump"):
			add("db.dump", "Database dump (source)", Ok, "mysqldump available")
		case in.Source.Has("mariadb-dump"):
			add("db.dump", "Database dump (source)", Ok, "mariadb-dump available")
		case in.Source.Has("php"):
			add("db.dump", "Database dump (source)", Warning,
				"mysqldump is missing on the source — rehost will fall back to a slower PHP dump helper")
		default:
			add("db.dump", "Database dump (source)", Blocker,
				"the source has neither mysqldump/mariadb-dump nor a PHP CLI — there is no way to dump the database; migrate would fail at the dump step")
		}
		if _, ok := destMySQLTool(in); ok {
			add("db.import", "Database import (destination)", Ok, "mysql client available")
		} else {
			add("db.import", "Database import (destination)", Blocker,
				"no mysql/mariadb client on the destination — the database cannot be imported")
		}
	}
	if len(pgSites) > 0 {
		if in.Source.Has("pg_dump") {
			add("db.dump.pgsql", "Database dump (source, PostgreSQL)", Ok, "pg_dump available")
		} else {
			add("db.dump.pgsql", "Database dump (source, PostgreSQL)", Blocker,
				"pg_dump is missing on the source — there is no PHP fallback for PostgreSQL, so "+
					strings.Join(pgSites, ", ")+" cannot be dumped")
		}
		if in.Destination.Has("psql") {
			add("db.import.pgsql", "Database import (destination, PostgreSQL)", Ok, "psql available")
		} else {
			add("db.import.pgsql", "Database import (destination, PostgreSQL)", Blocker,
				"psql is missing on the destination — "+strings.Join(pgSites, ", ")+
					" store content in PostgreSQL, and rehost never converts between database engines; pick a destination that offers PostgreSQL")
		}
	}
}

// checkEngine advises on the MySQL↔MariaDB pairing. The two are one
// toolchain but not one engine: a cross-migration generally works for
// typical sites, yet features do not fully overlap (and MariaDB→MySQL is
// the riskier direction), so a mismatch is worth a warning, never silence —
// and never a conversion attempt. PostgreSQL needs no row here: the import
// rule already blocks when the destination lacks it.
func checkEngine(in Input, add addFunc) {
	const title = "Database engine"
	tool, ok := destMySQLTool(in)
	if !ok {
		return // no mysql-family destination — nothing to compare against
	}
	destVersion := mysqlToolVersion(tool.Version)
	destEngine := "MySQL"
	if strings.Contains(tool.Version, "MariaDB") || tool.Name == "mariadb" {
		destEngine = "MariaDB"
	}

	seen := map[string]bool{}
	for _, inst := range in.Installs {
		if !recipe.RequirementsFor(inst).NeedsDB || installDriver(in, inst) == db.DriverPostgres {
			continue
		}
		insp := in.SourceDBs[inst.Root]
		if insp == nil || !insp.Connected || insp.ServerVersion == "" {
			continue // cannot tell MySQL from MariaDB without an inspection
		}
		srcEngine := "MySQL"
		if strings.Contains(insp.ServerVersion, "MariaDB") {
			srcEngine = "MariaDB"
		}
		key := srcEngine + "→" + destEngine
		if seen[key] {
			continue
		}
		seen[key] = true
		switch {
		case srcEngine == destEngine:
			add("db.engine", title, Ok,
				fmt.Sprintf("source and destination both run %s (%s → %s)", srcEngine, insp.ServerVersion, destVersion))
		case srcEngine == "MariaDB" && destEngine == "MySQL":
			add("db.engine", title, Warning,
				fmt.Sprintf("source runs MariaDB %s but the destination runs MySQL %s — rehost imports the data as-is (no conversion); this usually works for typical sites, but MariaDB-only features (its JSON/sequence handling, some collations) do not exist on MySQL — test the site thoroughly after migration", insp.ServerVersion, destVersion))
		default:
			add("db.engine", title, Warning,
				fmt.Sprintf("source runs MySQL %s but the destination runs MariaDB %s — rehost imports the data as-is (no conversion); MariaDB accepts MySQL dumps for typical sites, but the engines have diverged (JSON is stored differently, some collations differ) — test the site after migration", insp.ServerVersion, destVersion))
		}
	}
}

// checkDestDB reports which database-backed sites name a destination
// database. migrate only migrates a site's database when migrate.yaml has a
// dest_db block for it — surfacing the gap here means the user learns while
// rerunning check, not from a warning buried in migrate's pre-flight.
func checkDestDB(in Input, add addFunc) {
	const title = "Destination database (dest_db)"
	if !anyNeedsDB(in.Installs) || in.DestDBs == nil {
		return
	}
	var missing []string
	for _, inst := range in.Installs {
		if recipe.RequirementsFor(inst).NeedsDB && !in.DestDBs[inst.Root] {
			missing = append(missing, inst.Root)
		}
	}
	if len(missing) > 0 {
		add("db.dest", title, Warning,
			"no dest_db in migrate.yaml for: "+strings.Join(missing, ", ")+
				" — migrate would sync these sites' files only; create the database in the destination panel and add a dest_db block to migrate it")
		return
	}
	add("db.dest", title, Ok, "every database-backed site names a destination database")
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
			detail := fmt.Sprintf("%s: %s %s · %d tables · %s", inst.Root,
				db.EngineLabel(installDriver(in, inst), insp.ServerVersion), insp.ServerVersion, insp.Tables, humanKB(insp.SizeKB))
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
	tool, ok := destMySQLTool(in)
	if mb4 == 0 || !ok {
		return // nothing to compare, or db.import already blocked
	}
	version := mysqlToolVersion(tool.Version)
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
// ("Ver 15.1 Distrib 10.6.18-MariaDB"), so the number after Distrib wins
// when present. Otherwise the first number after Ver is the one — never a
// later match, which on distro-packaged clients is the package revision
// ("Ver 8.0.36-0ubuntu0.22.04.1" must read as 8.0.36, not 0.22.04).
func mysqlToolVersion(line string) string {
	if i := strings.Index(line, "Distrib"); i >= 0 {
		return versionPattern.FindString(line[i:])
	}
	if i := strings.Index(line, "Ver"); i >= 0 {
		return versionPattern.FindString(line[i:])
	}
	return versionPattern.FindString(line)
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
	// Only the site files land under the destination home; the imported
	// database lives on the MySQL server's storage, which df of the home
	// does not measure — counting it here produced false blockers (and
	// false confidence) against the wrong quota. It gets its own row below.
	needed := in.SourceSitesKB
	what := fmt.Sprintf("%s of site data", humanKB(in.SourceSitesKB))
	switch {
	case in.SourceSitesKB == 0 || in.DestFreeKB == 0:
		add("disk.space", title, Info, "could not measure site size or free space")
	case in.DestFreeKB < needed:
		add("disk.space", title, Blocker,
			fmt.Sprintf("%s needed but only %s is free", what, humanKB(in.DestFreeKB)))
	case in.DestFreeKB < needed*3/2:
		add("disk.space", title, Warning,
			fmt.Sprintf("tight: %s needed, %s free — temp files need headroom", what, humanKB(in.DestFreeKB)))
	default:
		add("disk.space", title, Ok,
			fmt.Sprintf("%s free for %s", humanKB(in.DestFreeKB), what))
	}

	var dbKB int64
	for _, insp := range in.SourceDBs {
		if insp != nil {
			dbKB += insp.SizeKB
		}
	}
	if dbKB > 0 {
		add("disk.db", "Database size", Info,
			fmt.Sprintf("%s of database data will be imported — it lands on the destination's MySQL storage (not the home quota), and the dump is staged on THIS machine first: keep at least that much free locally", humanKB(dbKB)))
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

// ttlWarnAboveSeconds is the TTL that triggers "lower it now" advice — above
// an hour, a DNS cutover on migration day takes too long to propagate.
// ttlLowEnoughSeconds is what a cutover-ready TTL looks like.
const (
	ttlWarnAboveSeconds = 3600
	ttlLowEnoughSeconds = 300
)

// checkDNSTTL advises lowering high TTLs well before migration day, so the
// eventual cutover (repointing A/AAAA/CNAME at the destination) propagates
// quickly instead of leaving stragglers on the source for hours. It never
// blocks — TTL is advisory, not a migration blocker — and stays silent
// unless a domain is configured, its DNS snapshot was read, and there is
// something concrete to say (either a high TTL to flag or confirmation the
// TTLs are already cutover-ready).
func checkDNSTTL(in Input, add addFunc) {
	const title = "DNS TTL (cutover readiness)"
	if in.Domain == "" || in.DNS == nil {
		return
	}

	var offending []string
	var maxTTL uint32
	var haveRecords bool
	for _, r := range in.DNS.Records {
		switch r.Type {
		case "A", "AAAA", "CNAME":
		default:
			continue
		}
		haveRecords = true
		if r.TTL > maxTTL {
			maxTTL = r.TTL
		}
		if r.TTL > ttlWarnAboveSeconds {
			offending = append(offending, fmt.Sprintf("%s %s (TTL %ds)", r.Type, r.Value, r.TTL))
		}
	}
	if !haveRecords {
		return
	}

	switch {
	case len(offending) > 0:
		// A high TTL is trustworthy from any resolver: a cached value only
		// ever under-reports the authoritative one.
		add("dns.ttl", title, Warning,
			fmt.Sprintf("%s above %ds — lower the TTL to ~%ds now, well before migration day, so the eventual DNS cutover propagates quickly",
				strings.Join(offending, ", "), ttlWarnAboveSeconds, ttlLowEnoughSeconds))
	case maxTTL <= ttlLowEnoughSeconds && in.DNS.AuthoritativeTTLs:
		add("dns.ttl", title, Ok,
			fmt.Sprintf("TTLs are already %ds or lower (confirmed at the domain's nameserver) — ready for a fast cutover", ttlLowEnoughSeconds))
	case maxTTL <= ttlLowEnoughSeconds:
		// A LOW cached TTL proves nothing: it may be the decaying remainder
		// of an authoritative 86400. Never call that cutover-ready.
		add("dns.ttl", title, Info,
			fmt.Sprintf("TTLs read %ds or lower, but only from a resolver cache (the decaying remainder, not the configured value) — confirm the real TTL at the DNS provider before planning a fast cutover", maxTTL))
	}
}

// checkMultisite refuses multisite installs outright: rehost's rewrite and
// exclude machinery is single-site, and a half-migrated multisite (only the
// default site's database and config rewritten) is worse than an honest no.
func checkMultisite(in Input, add addFunc) {
	const title = "Multisite"
	for _, inst := range in.Installs {
		switch {
		case inst.Extra["multisite"] == "true":
			add("site.multisite", title, Blocker,
				fmt.Sprintf("%s is a WordPress multisite (MULTISITE is true) — rehost migrates single-site installs and a partial rewrite would break the network; migrate it with a multisite-aware tool for now", inst.Root))
		case inst.Framework == "drupal" && len(inst.Sites) > 1:
			add("site.multisite", title, Blocker,
				fmt.Sprintf("%s is a Drupal multisite (%s) — rehost migrates single-site installs and would only rewrite sites/default; migrate it by hand for now", inst.Root, strings.Join(inst.Sites, ", ")))
		}
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
