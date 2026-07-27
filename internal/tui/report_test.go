package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/ssh"
)

func sampleReports() []HostReport {
	return []HostReport{
		{
			Role: "source",
			Caps: &ssh.Capabilities{
				Host: "source.example.com", Shell: "bash", Uname: "Linux x86_64",
				Home: "/home/u1", PHPVersion: "8.3.11",
				Tools: map[string]ssh.Tool{
					"rsync": {Name: "rsync", Found: true, Path: "/usr/bin/rsync", Version: "rsync 3.2.7"},
					"php":   {Name: "php", Found: true, Path: "/usr/bin/php", Version: "PHP 8.3.11 (cli)"},
					"drush": {Name: "drush", Found: false},
				},
			},
			Installs: []detect.Install{
				{Framework: "wordpress", Root: "public_html", Version: "6.5.2", ConfigFile: "public_html/wp-config.php"},
				{Framework: "drupal", Root: "sub", Version: "10.3.1", Sites: []string{"default", "example.com"}},
			},
		},
	}
}

func TestPlainReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModePlain, &buf).CapabilityReport(sampleReports()); err != nil {
		t.Fatalf("CapabilityReport: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"source: source.example.com",
		"shell bash",
		"PHP 8.3.11",
		"[ok]",
		"rsync",
		"[missing]",
		"drush",
		"wordpress 6.5.2",
		"drupal 10.3.1",
		"multisite: default, example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain output must not contain ANSI escapes")
	}
}

func TestJSONReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeJSON, &buf).CapabilityReport(sampleReports()); err != nil {
		t.Fatalf("CapabilityReport: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.capability-report.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if len(env.Hosts) != 1 || env.Hosts[0].Role != "source" || env.Hosts[0].Host != "source.example.com" {
		t.Errorf("hosts = %+v", env.Hosts)
	}
	if !env.Hosts[0].Has("rsync") {
		t.Error("rsync should survive the JSON round trip as found")
	}
	if len(env.Hosts[0].Installs) != 2 {
		t.Fatalf("want 2 installs in JSON, got %+v", env.Hosts[0].Installs)
	}
	if env.Hosts[0].Installs[0].Framework != "wordpress" || env.Hosts[0].Installs[0].Version != "6.5.2" {
		t.Errorf("install[0] = %+v", env.Hosts[0].Installs[0])
	}
}

func TestJSONReportEmptyInstalls(t *testing.T) {
	reports := sampleReports()
	reports[0].Installs = nil
	var buf bytes.Buffer
	if err := New(ModeJSON, &buf).CapabilityReport(reports); err != nil {
		t.Fatalf("CapabilityReport: %v", err)
	}
	// nil installs must serialize as [] so parsers can index without a null check.
	if !strings.Contains(buf.String(), `"installs": []`) {
		t.Errorf("empty installs should render as [], got:\n%s", buf.String())
	}
}

func TestStyledReportContainsMarks(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeStyled, &buf).CapabilityReport(sampleReports()); err != nil {
		t.Fatalf("CapabilityReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Errorf("styled output should contain ✓ and ✗ marks:\n%s", out)
	}
}

func TestNonInteractivePrompterFails(t *testing.T) {
	p := NonInteractivePrompter{}
	if _, err := p.Password("x"); err == nil {
		t.Error("Password must fail non-interactively")
	}
	ok, err := p.ConfirmHostKey("h", "ssh-ed25519", "SHA256:abc")
	if ok || err == nil {
		t.Error("ConfirmHostKey must refuse non-interactively")
	}
	if !strings.Contains(err.Error(), "SHA256:abc") {
		t.Errorf("error should include the fingerprint for manual verification: %v", err)
	}
}
