package check

import (
	"strings"
	"testing"

	"github.com/pietervanleuven/go-dns"
	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// capsWith builds Capabilities with the named tools present.
func capsWith(phpVersion string, tools ...string) *remote.Capabilities {
	m := map[string]remote.Tool{}
	for _, t := range tools {
		m[t] = remote.Tool{Name: t, Found: true}
	}
	return &remote.Capabilities{Host: "h", PHPVersion: phpVersion, Tools: m}
}

func byID(t *testing.T, results []Result, id string) Result {
	t.Helper()
	for _, r := range results {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no result with id %q in %+v", id, results)
	return Result{}
}

func hasID(results []Result, id string) bool {
	for _, r := range results {
		if r.ID == id {
			return true
		}
	}
	return false
}

var wpInstall = detect.Install{Framework: "wordpress", Version: "6.5.2", Root: "/home/u/public_html"}

func TestGreenPath(t *testing.T) {
	in := Input{
		Source:            capsWith("8.2.1", "tar", "gzip", "mysqldump", "find"),
		Destination:       capsWith("8.3.11", "tar", "gzip", "mysql", "find"),
		Installs:          []detect.Install{wpInstall},
		DestPHPExtensions: []string{"mysqli", "curl", "gd", "mbstring", "openssl", "zip"},
		SourceSitesKB:     1024,
		DestFreeKB:        10 * 1024 * 1024,
	}
	results := Run(in)
	blockers, warnings := Summarize(results)
	if blockers != 0 || warnings != 0 {
		t.Fatalf("green input produced %d blockers, %d warnings: %+v", blockers, warnings, results)
	}
	if r := byID(t, results, "transfer.files"); r.Severity != Ok || !strings.Contains(r.Detail, "tar pipe") {
		t.Errorf("transfer should be ok via the tar pipe, got %+v", r)
	}
}

func TestNoSitesWarns(t *testing.T) {
	in := Input{Source: capsWith(""), Destination: capsWith("")}
	if r := byID(t, Run(in), "sites"); r.Severity != Warning {
		t.Errorf("no sites should warn, got %+v", r)
	}
}

func TestTransferMissingTarBlocks(t *testing.T) {
	in := Input{
		Source:      capsWith("", "tar", "gzip", "find"),
		Destination: capsWith("", "gzip", "find"),
	}
	r := byID(t, Run(in), "transfer.files")
	if r.Severity != Blocker || !strings.Contains(r.Detail, "destination") {
		t.Errorf("missing destination tar should block and name the host, got %+v", r)
	}

	in.Source = capsWith("", "find")
	in.Destination = capsWith("", "find")
	r = byID(t, Run(in), "transfer.files")
	if r.Severity != Blocker || !strings.Contains(r.Detail, "source and destination") {
		t.Errorf("tar missing everywhere should block and name both hosts, got %+v", r)
	}
}

func TestMissingFindBlocks(t *testing.T) {
	in := Input{Source: capsWith("", "tar"), Destination: capsWith("", "tar", "find")}
	if r := byID(t, Run(in), "transfer.find"); r.Severity != Blocker {
		t.Errorf("missing find should block (manifests drive the sync), got %+v", r)
	}
	in.Source = capsWith("", "tar", "find")
	if hasID(Run(in), "transfer.find") {
		t.Error("find on both hosts should produce no transfer.find row")
	}
}

func TestDestDBRule(t *testing.T) {
	in := Input{
		Source:      capsWith("", "tar", "find"),
		Destination: capsWith("", "tar", "find"),
		Installs:    []detect.Install{wpInstall},
	}
	if hasID(Run(in), "db.dest") {
		t.Error("nil DestDBs (not gathered) should stay silent")
	}

	in.DestDBs = map[string]bool{}
	r := byID(t, Run(in), "db.dest")
	if r.Severity != Warning || !strings.Contains(r.Detail, wpInstall.Root) {
		t.Errorf("missing dest_db should warn and name the site, got %+v", r)
	}

	in.DestDBs = map[string]bool{wpInstall.Root: true}
	if r := byID(t, Run(in), "db.dest"); r.Severity != Ok {
		t.Errorf("configured dest_db should be ok, got %+v", r)
	}

	in.Installs = []detect.Install{{Framework: "static", Root: "/home/u/www"}}
	in.DestDBs = map[string]bool{}
	if hasID(Run(in), "db.dest") {
		t.Error("static-only sites need no dest_db row")
	}
}

func TestDatabaseRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "php"),
		Destination: capsWith("8.2", "rsync"),
		Installs:    []detect.Install{wpInstall},
	}
	if r := byID(t, Run(in), "db.dump"); r.Severity != Warning {
		t.Errorf("missing mysqldump with php should warn (PHP fallback), got %+v", r)
	}
	if r := byID(t, Run(in), "db.import"); r.Severity != Blocker {
		t.Errorf("missing destination mysql must block, got %+v", r)
	}

	// Neither mysqldump nor a PHP CLI: nothing can dump, so the gate blocks
	// rather than passing migrate through to fail at the dump step.
	noDump := Input{
		Source:      capsWith("", "rsync", "find"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
	}
	if r := byID(t, Run(noDump), "db.dump"); r.Severity != Blocker {
		t.Errorf("no mysqldump and no php must block, got %+v", r)
	}

	// Static-only accounts have no DB rules at all.
	static := Input{
		Source:      capsWith("", "rsync", "find"),
		Destination: capsWith("", "rsync"),
		Installs:    []detect.Install{{Framework: "static", Root: "/home/u/www"}},
	}
	if results := Run(static); hasID(results, "db.dump") || hasID(results, "db.import") {
		t.Errorf("static-only input must not produce db results: %+v", results)
	}
}

func TestCredentialRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
	}

	if r := byID(t, Run(in), "db.credentials"); r.Severity != Info {
		t.Errorf("ungathered credentials should be info, got %+v", r)
	}

	in.SourceCreds = map[string]*db.Credentials{
		wpInstall.Root: {Name: "wpdb", Host: "localhost", Password: "topsecret", Method: "wp-cli"},
	}
	r := byID(t, Run(in), "db.credentials")
	if r.Severity != Ok || !strings.Contains(r.Detail, "wpdb@localhost") || !strings.Contains(r.Detail, "wp-cli") {
		t.Errorf("found credentials should be ok naming db and method, got %+v", r)
	}
	if strings.Contains(r.Detail, "topsecret") {
		t.Fatalf("detail must never contain the password: %q", r.Detail)
	}

	in.SourceCreds = map[string]*db.Credentials{wpInstall.Root: nil}
	if r := byID(t, Run(in), "db.credentials"); r.Severity != Warning || !strings.Contains(r.Detail, wpInstall.Root) {
		t.Errorf("missing credentials should warn naming the root, got %+v", r)
	}

	// Static-only: no credentials rule at all.
	static := Input{
		Source:      capsWith("", "rsync", "find"),
		Destination: capsWith("", "rsync"),
		Installs:    []detect.Install{{Framework: "static", Root: "/w"}},
	}
	if hasID(Run(static), "db.credentials") {
		t.Error("static-only input must not produce a credentials result")
	}
}

func TestDBConnectRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump", "mysql"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
		SourceCreds: map[string]*db.Credentials{wpInstall.Root: {Name: "wpdb", Method: "wp-cli"}},
	}

	if r := byID(t, Run(in), "db.connect"); r.Severity != Info {
		t.Errorf("uninspected databases should be info, got %+v", r)
	}

	in.SourceDBs = map[string]*db.Inspection{
		wpInstall.Root: {Connected: true, ServerVersion: "8.0.36", Tables: 12, SizeKB: 2048, Charset: "utf8mb4"},
	}
	r := byID(t, Run(in), "db.connect")
	if r.Severity != Ok || !strings.Contains(r.Detail, "8.0.36") || !strings.Contains(r.Detail, "12 tables") {
		t.Errorf("connected inspection should be ok with stats, got %+v", r)
	}

	in.SourceDBs = map[string]*db.Inspection{
		wpInstall.Root: {Connected: false, Reason: "Access denied for user"},
	}
	r = byID(t, Run(in), "db.connect")
	if r.Severity != Blocker || !strings.Contains(r.Detail, "Access denied") {
		t.Errorf("failed connection must block with the reason, got %+v", r)
	}

	// Without extracted credentials there is no connect rule (the
	// credentials rule already covers the failure).
	in.SourceCreds = map[string]*db.Credentials{wpInstall.Root: nil}
	if hasID(Run(in), "db.connect") {
		t.Error("no credentials → no connect result")
	}
}

func TestCharsetRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump", "mysql"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
		SourceCreds: map[string]*db.Credentials{wpInstall.Root: {Name: "wpdb"}},
		SourceDBs:   map[string]*db.Inspection{wpInstall.Root: {Connected: true, UTF8MB4Tables: 0}},
	}

	// No utf8mb4 in use: no charset result.
	if hasID(Run(in), "db.charset") {
		t.Error("no utf8mb4 usage → no charset result")
	}

	in.SourceDBs[wpInstall.Root].UTF8MB4Tables = 9

	// Modern MariaDB client on the destination: ok, using the Distrib number.
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true,
		Version: "mysql  Ver 15.1 Distrib 10.6.18-MariaDB, for Linux"}
	r := byID(t, Run(in), "db.charset")
	if r.Severity != Ok || !strings.Contains(r.Detail, "10.6.18") {
		t.Errorf("modern destination should be ok naming its version, got %+v", r)
	}

	// Ancient destination client: blocker.
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true, Version: "mysql Ver 14.14 Distrib 5.1.73"}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Blocker {
		t.Errorf("pre-utf8mb4 destination must block, got %+v", r)
	}

	// Debian/Ubuntu-packaged MySQL 8: the package revision after the dash
	// ("0ubuntu0.22.04.1") must not be read as the version.
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true,
		Version: "mysql  Ver 8.0.36-0ubuntu0.22.04.1 for Linux on x86_64 ((Ubuntu))"}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Ok || !strings.Contains(r.Detail, "8.0.36") {
		t.Errorf("distro-packaged MySQL 8 should be ok as 8.0.36, got %+v", r)
	}

	// Debian-packaged MariaDB: Distrib still wins over the client version.
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true,
		Version: "mysql  Ver 15.1 Distrib 10.11.6-MariaDB, for debian-linux-gnu (x86_64) using  EditLine wrapper"}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Ok || !strings.Contains(r.Detail, "10.11.6") {
		t.Errorf("debian MariaDB should be ok as 10.11.6, got %+v", r)
	}

	// Unparseable version: info, not a false pass.
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true, Version: ""}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Info {
		t.Errorf("unknown destination version should be info, got %+v", r)
	}
}

func TestDiskSplitsDatabaseFromHomeQuota(t *testing.T) {
	in := Input{
		Source:        capsWith("", "rsync", "find"),
		Destination:   capsWith("", "rsync"),
		SourceSitesKB: 1000,
		DestFreeKB:    1100,
		SourceDBs:     map[string]*db.Inspection{"/a": {Connected: true, SizeKB: 500}},
	}
	// The database lands on MySQL storage, not the home quota: 1000 KiB of
	// files against 1100 KiB free is tight, not blocked by the DB's 500.
	if r := byID(t, Run(in), "disk.space"); r.Severity != Warning || strings.Contains(r.Detail, "database") {
		t.Errorf("db size must not count against the home quota, got %+v", r)
	}
	// The database size gets its own row, naming the local staging need.
	if r := byID(t, Run(in), "disk.db"); r.Severity != Info || !strings.Contains(r.Detail, "THIS machine") {
		t.Errorf("db size should be reported separately, got %+v", r)
	}
}

func TestPHPRules(t *testing.T) {
	base := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump"),
		Destination: capsWith("", "rsync", "mysql"),
		Installs:    []detect.Install{{Framework: "drupal", Version: "10.2.6", Root: "/x"}},
	}
	if r := byID(t, Run(base), "php.version"); r.Severity != Blocker {
		t.Errorf("missing destination PHP must block, got %+v", r)
	}

	base.Destination = capsWith("7.4.33", "rsync", "mysql")
	r := byID(t, Run(base), "php.version")
	if r.Severity != Blocker || !strings.Contains(r.Detail, "8.1") || !strings.Contains(r.Detail, "drupal") {
		t.Errorf("PHP 7.4 vs Drupal 10 must block naming the requirement, got %+v", r)
	}

	base.Destination = capsWith("8.1.27", "rsync", "mysql")
	if r := byID(t, Run(base), "php.version"); r.Severity != Ok {
		t.Errorf("PHP 8.1.27 should satisfy Drupal 10, got %+v", r)
	}
}

func TestPHPMajorDriftInfo(t *testing.T) {
	in := Input{
		Source:      capsWith("7.4.33", "rsync", "find", "mysqldump"),
		Destination: capsWith("8.3.1", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
	}
	if r := byID(t, Run(in), "php.drift"); r.Severity != Info {
		t.Errorf("major drift should be info, got %+v", r)
	}
}

func TestExtensionRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
	}

	in.DestPHPExtensions = nil
	if r := byID(t, Run(in), "php.extensions"); r.Severity != Info {
		t.Errorf("unknown extensions should be info, got %+v", r)
	}

	in.DestPHPExtensions = []string{"curl", "gd", "mbstring", "openssl", "zip"}
	r := byID(t, Run(in), "php.extensions")
	if r.Severity != Blocker || !strings.Contains(r.Detail, "mysqli") {
		t.Errorf("missing mysqli must block, got %+v", r)
	}

	in.DestPHPExtensions = []string{"mysqli", "curl", "gd", "openssl", "zip"}
	r = byID(t, Run(in), "php.extensions")
	if r.Severity != Warning || !strings.Contains(r.Detail, "mbstring") {
		t.Errorf("missing mbstring should warn, got %+v", r)
	}

	in.DestPHPExtensions = []string{"MySQLi", "Curl", "GD", "mbstring", "OpenSSL", "zip"}
	if r := byID(t, Run(in), "php.extensions"); r.Severity != Ok {
		t.Errorf("extension match must be case-insensitive, got %+v", r)
	}
}

func TestDiskRules(t *testing.T) {
	base := Input{
		Source:      capsWith("", "rsync", "find"),
		Destination: capsWith("", "rsync"),
	}

	if r := byID(t, Run(base), "disk.space"); r.Severity != Info {
		t.Errorf("unknown sizes should be info, got %+v", r)
	}

	base.SourceSitesKB, base.DestFreeKB = 2048, 1024
	if r := byID(t, Run(base), "disk.space"); r.Severity != Blocker {
		t.Errorf("free < needed must block, got %+v", r)
	}

	base.SourceSitesKB, base.DestFreeKB = 1000, 1200
	if r := byID(t, Run(base), "disk.space"); r.Severity != Warning {
		t.Errorf("free < 1.5x needed should warn, got %+v", r)
	}

	base.SourceSitesKB, base.DestFreeKB = 1000, 10000
	if r := byID(t, Run(base), "disk.space"); r.Severity != Ok {
		t.Errorf("plenty of space should be ok, got %+v", r)
	}
}

func TestDNSRules(t *testing.T) {
	base := Input{Source: capsWith("", "rsync", "find"), Destination: capsWith("", "rsync")}

	// No domain configured: one hint, nothing else.
	r := byID(t, Run(base), "dns.domain")
	if r.Severity != Info || !strings.Contains(r.Detail, "domain:") {
		t.Errorf("no domain should hint at the field, got %+v", r)
	}
	if hasID(Run(base), "dns.mail") {
		t.Error("no domain → no mail result")
	}

	// Domain set but lookup failed.
	base.Domain = "example.com"
	if r := byID(t, Run(base), "dns.domain"); r.Severity != Info || !strings.Contains(r.Detail, "could not look up") {
		t.Errorf("failed lookup should be info, got %+v", r)
	}

	base.SourceIPs = []string{"192.0.2.10"}
	base.DNS = &dns.Snapshot{
		Domain: "example.com",
		Records: []dns.Record{
			{Type: "A", Value: "192.0.2.10", TTL: 3600},
			{Type: "MX", Value: "mail.example.com", TTL: 3600, Priority: 10},
		},
		MailHosts: map[string][]string{"mail.example.com": {"192.0.2.10"}},
	}
	if r := byID(t, Run(base), "dns.domain"); r.Severity != Ok || !strings.Contains(r.Detail, "points at the source") {
		t.Errorf("domain on the source should be ok, got %+v", r)
	}
	r = byID(t, Run(base), "dns.mail")
	if r.Severity != Warning || !strings.Contains(r.Detail, "mail is hosted there") {
		t.Errorf("MX on the source must warn, got %+v", r)
	}

	// Mail elsewhere: fine.
	base.DNS.MailHosts = map[string][]string{"mail.example.com": {"198.51.100.9"}}
	if r := byID(t, Run(base), "dns.mail"); r.Severity != Ok {
		t.Errorf("mail elsewhere should be ok, got %+v", r)
	}

	// Domain pointing somewhere else entirely: warn.
	base.DNS.Records[0].Value = "203.0.113.5"
	if r := byID(t, Run(base), "dns.domain"); r.Severity != Warning {
		t.Errorf("domain not on the source should warn, got %+v", r)
	}
}

func TestDNSTTLRule(t *testing.T) {
	base := Input{Source: capsWith("", "rsync", "find"), Destination: capsWith("", "rsync")}

	// No domain configured: not applicable, silent.
	if hasID(Run(base), "dns.ttl") {
		t.Error("no domain → dns.ttl should not appear")
	}

	// Domain set but snapshot missing (lookup failed): silent.
	base.Domain = "example.com"
	if hasID(Run(base), "dns.ttl") {
		t.Error("missing snapshot → dns.ttl should not appear")
	}

	// High TTL on an A record: warn, naming the record and its TTL.
	base.DNS = &dns.Snapshot{
		Domain: "example.com",
		Records: []dns.Record{
			{Type: "A", Value: "192.0.2.10", TTL: 86400},
			{Type: "MX", Value: "mail.example.com", TTL: 300, Priority: 10},
		},
	}
	r := byID(t, Run(base), "dns.ttl")
	if r.Severity != Warning {
		t.Fatalf("TTL above 3600s should warn, got %+v", r)
	}
	if !strings.Contains(r.Detail, "192.0.2.10") || !strings.Contains(r.Detail, "86400") {
		t.Errorf("warning should name the offending record and its TTL, got %q", r.Detail)
	}
	if !strings.Contains(r.Detail, "300") {
		t.Errorf("warning should suggest the ~300s target TTL, got %q", r.Detail)
	}

	// Low TTL from a resolver cache: never "cutover-ready" — a cached value
	// is the decaying remainder of a possibly-huge authoritative TTL.
	base.DNS.Records = []dns.Record{
		{Type: "A", Value: "192.0.2.10", TTL: 300},
		{Type: "CNAME", Value: "example.com", TTL: 120},
	}
	if r := byID(t, Run(base), "dns.ttl"); r.Severity != Info || !strings.Contains(r.Detail, "resolver cache") {
		t.Errorf("low cached TTLs should hedge as info, got %+v", r)
	}

	// The same low TTLs confirmed at the domain's nameserver: Ok.
	base.DNS.AuthoritativeTTLs = true
	if r := byID(t, Run(base), "dns.ttl"); r.Severity != Ok || !strings.Contains(r.Detail, "ready for a fast cutover") {
		t.Errorf("authoritative low TTLs should confirm readiness, got %+v", r)
	}
	base.DNS.AuthoritativeTTLs = false

	// Snapshot present but no A/AAAA/CNAME records at all: nothing to advise.
	base.DNS.Records = []dns.Record{{Type: "MX", Value: "mail.example.com", TTL: 86400, Priority: 10}}
	if hasID(Run(base), "dns.ttl") {
		t.Error("no A/AAAA/CNAME records → dns.ttl should not appear")
	}
}

func TestStrictestMinPHPPicksHighest(t *testing.T) {
	minPHP, needer := strictestMinPHP([]detect.Install{
		{Framework: "wordpress", Version: "6.5"}, // 7.2
		{Framework: "drupal", Version: "11.0.1"}, // 8.3
		{Framework: "static"},                    // none
		{Framework: "drupal", Version: "7.98"},   // 5.6
	})
	if minPHP != "8.3" || !strings.Contains(needer, "drupal 11") {
		t.Errorf("strictestMinPHP = %q for %q, want 8.3 for drupal 11", minPHP, needer)
	}
}

func TestMultisiteBlocks(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find"),
		Destination: capsWith("8.2", "rsync"),
	}

	// Single-site installs: silent.
	in.Installs = []detect.Install{wpInstall}
	if hasID(Run(in), "site.multisite") {
		t.Error("single-site installs must not trigger the multisite rule")
	}

	// WordPress network install: blocker.
	wpMulti := wpInstall
	wpMulti.Extra = map[string]string{"multisite": "true"}
	in.Installs = []detect.Install{wpMulti}
	if r := byID(t, Run(in), "site.multisite"); r.Severity != Blocker || !strings.Contains(r.Detail, "multisite") {
		t.Errorf("WP multisite must block, got %+v", r)
	}

	// Drupal with more than one configured site: blocker.
	in.Installs = []detect.Install{{Framework: "drupal", Root: "/home/u/drupal", Sites: []string{"default", "shop.example.com"}}}
	if r := byID(t, Run(in), "site.multisite"); r.Severity != Blocker || !strings.Contains(r.Detail, "shop.example.com") {
		t.Errorf("Drupal multisite must block naming the sites, got %+v", r)
	}
}

// Hosts with MariaDB-named binaries only (no mysql symlinks) must pass the
// tooling rules.
func TestDatabaseRulesMariaDBNames(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mariadb-dump"),
		Destination: capsWith("8.2", "rsync", "mariadb"),
		Installs:    []detect.Install{wpInstall},
	}
	if r := byID(t, Run(in), "db.dump"); r.Severity != Ok || !strings.Contains(r.Detail, "mariadb-dump") {
		t.Errorf("mariadb-dump should satisfy the dump rule, got %+v", r)
	}
	if r := byID(t, Run(in), "db.import"); r.Severity != Ok {
		t.Errorf("mariadb client should satisfy the import rule, got %+v", r)
	}
}

// PostgreSQL-backed sites need pg tooling; there is no PHP fallback, and
// rehost never converts engines.
func TestDatabaseRulesPostgres(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "php"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{{Framework: "wordpress", Root: "/home/u/craft"}},
		SourceCreds: map[string]*db.Credentials{"/home/u/craft": {Name: "craftdb", Driver: "pgsql"}},
	}
	if r := byID(t, Run(in), "db.dump.pgsql"); r.Severity != Blocker || !strings.Contains(r.Detail, "no PHP fallback") {
		t.Errorf("missing pg_dump must block without a PHP-fallback promise, got %+v", r)
	}
	if r := byID(t, Run(in), "db.import.pgsql"); r.Severity != Blocker || !strings.Contains(r.Detail, "never converts") {
		t.Errorf("missing psql must block naming the no-conversion policy, got %+v", r)
	}
	// A pg-only site must not demand mysql tooling.
	if hasID(Run(in), "db.dump") {
		t.Error("a pgsql-only site must not produce mysql-family dump rows")
	}

	in.Source = capsWith("8.2", "rsync", "find", "pg_dump")
	in.Destination = capsWith("8.2", "rsync", "psql")
	if r := byID(t, Run(in), "db.dump.pgsql"); r.Severity != Ok {
		t.Errorf("pg_dump present should be ok, got %+v", r)
	}
	if r := byID(t, Run(in), "db.import.pgsql"); r.Severity != Ok {
		t.Errorf("psql present should be ok, got %+v", r)
	}
}

// The engine rule warns on MySQL↔MariaDB cross-migrations — as-is imports,
// never conversions — and confirms matching engines.
func TestEngineRule(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find", "mysqldump", "mysql"),
		Destination: capsWith("8.2", "rsync", "mysql"),
		Installs:    []detect.Install{wpInstall},
		SourceCreds: map[string]*db.Credentials{wpInstall.Root: {Name: "wpdb"}},
	}

	// No inspection: engines unknown, rule silent.
	if hasID(Run(in), "db.engine") {
		t.Error("without an inspection the engine rule must stay silent")
	}

	// MariaDB source → MySQL destination: warn, as-is import named.
	in.SourceDBs = map[string]*db.Inspection{
		wpInstall.Root: {Connected: true, ServerVersion: "10.11.6-MariaDB"},
	}
	in.Destination.Tools["mysql"] = remote.Tool{Name: "mysql", Found: true,
		Version: "mysql  Ver 8.0.36-0ubuntu0.22.04.1 for Linux on x86_64"}
	r := byID(t, Run(in), "db.engine")
	if r.Severity != Warning || !strings.Contains(r.Detail, "MariaDB") || !strings.Contains(r.Detail, "no conversion") {
		t.Errorf("MariaDB→MySQL should warn about the as-is import, got %+v", r)
	}

	// MariaDB → MariaDB (via the mariadb-named client): ok.
	in.Destination = capsWith("8.2", "rsync", "mariadb")
	in.Destination.Tools["mariadb"] = remote.Tool{Name: "mariadb", Found: true,
		Version: "mariadb  Ver 15.1 Distrib 10.11.6-MariaDB"}
	if r := byID(t, Run(in), "db.engine"); r.Severity != Ok || !strings.Contains(r.Detail, "MariaDB") {
		t.Errorf("matching engines should confirm, got %+v", r)
	}

	// MySQL → MariaDB: warn the other way.
	in.SourceDBs[wpInstall.Root].ServerVersion = "8.0.36"
	if r := byID(t, Run(in), "db.engine"); r.Severity != Warning || !strings.Contains(r.Detail, "MySQL 8.0.36") {
		t.Errorf("MySQL→MariaDB should warn, got %+v", r)
	}
}
