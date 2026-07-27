package check

import (
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/ssh"
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
