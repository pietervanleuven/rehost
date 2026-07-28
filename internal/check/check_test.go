package check

import (
	"strings"
	"testing"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/dns"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// capsWith builds Capabilities with the named tools present.
func capsWith(phpVersion string, tools ...string) *ssh.Capabilities {
	m := map[string]ssh.Tool{}
	for _, t := range tools {
		m[t] = ssh.Tool{Name: t, Found: true}
	}
	return &ssh.Capabilities{Host: "h", PHPVersion: phpVersion, Tools: m}
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
		Source:            capsWith("8.2.1", "rsync", "tar", "gzip", "mysqldump", "find"),
		Destination:       capsWith("8.3.11", "rsync", "tar", "gzip", "mysql"),
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
	if r := byID(t, results, "transfer.files"); r.Severity != Ok || !strings.Contains(r.Detail, "rsync") {
		t.Errorf("transfer should be ok via rsync, got %+v", r)
	}
}

func TestNoSitesWarns(t *testing.T) {
	in := Input{Source: capsWith(""), Destination: capsWith("")}
	if r := byID(t, Run(in), "sites"); r.Severity != Warning {
		t.Errorf("no sites should warn, got %+v", r)
	}
}

func TestTransferFallbacks(t *testing.T) {
	in := Input{
		Source:      capsWith("", "tar", "gzip", "find"),
		Destination: capsWith("", "tar", "gzip"),
	}
	if r := byID(t, Run(in), "transfer.files"); r.Severity != Info || !strings.Contains(r.Detail, "tar") {
		t.Errorf("tar-only hosts should be info, got %+v", r)
	}

	in.Source = capsWith("", "find")
	in.Destination = capsWith("")
	if r := byID(t, Run(in), "transfer.files"); r.Severity != Warning {
		t.Errorf("no rsync/tar should warn, got %+v", r)
	}
}

func TestMissingFindWarns(t *testing.T) {
	in := Input{Source: capsWith("", "rsync"), Destination: capsWith("", "rsync")}
	if r := byID(t, Run(in), "transfer.find"); r.Severity != Warning {
		t.Errorf("missing find should warn, got %+v", r)
	}
}

func TestDatabaseRules(t *testing.T) {
	in := Input{
		Source:      capsWith("8.2", "rsync", "find"),
		Destination: capsWith("8.2", "rsync"),
		Installs:    []detect.Install{wpInstall},
	}
	if r := byID(t, Run(in), "db.dump"); r.Severity != Warning {
		t.Errorf("missing mysqldump should warn (PHP fallback), got %+v", r)
	}
	if r := byID(t, Run(in), "db.import"); r.Severity != Blocker {
		t.Errorf("missing destination mysql must block, got %+v", r)
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
	in.Destination.Tools["mysql"] = ssh.Tool{Name: "mysql", Found: true,
		Version: "mysql  Ver 15.1 Distrib 10.6.18-MariaDB, for Linux"}
	r := byID(t, Run(in), "db.charset")
	if r.Severity != Ok || !strings.Contains(r.Detail, "10.6.18") {
		t.Errorf("modern destination should be ok naming its version, got %+v", r)
	}

	// Ancient destination client: blocker.
	in.Destination.Tools["mysql"] = ssh.Tool{Name: "mysql", Found: true, Version: "mysql Ver 14.14 Distrib 5.1.73"}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Blocker {
		t.Errorf("pre-utf8mb4 destination must block, got %+v", r)
	}

	// Unparseable version: info, not a false pass.
	in.Destination.Tools["mysql"] = ssh.Tool{Name: "mysql", Found: true, Version: ""}
	if r := byID(t, Run(in), "db.charset"); r.Severity != Info {
		t.Errorf("unknown destination version should be info, got %+v", r)
	}
}

func TestDiskIncludesDatabaseSize(t *testing.T) {
	in := Input{
		Source:        capsWith("", "rsync", "find"),
		Destination:   capsWith("", "rsync"),
		SourceSitesKB: 1000,
		DestFreeKB:    1100,
		SourceDBs:     map[string]*db.Inspection{"/a": {Connected: true, SizeKB: 500}},
	}
	// 1000 sites + 500 db = 1500 needed > 1100 free → blocker.
	if r := byID(t, Run(in), "disk.space"); r.Severity != Blocker || !strings.Contains(r.Detail, "database") {
		t.Errorf("db size must count toward disk need, got %+v", r)
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
