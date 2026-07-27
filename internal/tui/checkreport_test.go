package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/placeholder/rehost/internal/check"
)

var checkResults = []check.Result{
	{ID: "sites", Title: "Websites on the source", Severity: check.Ok, Detail: "1 wordpress"},
	{ID: "php.extensions", Title: "PHP extensions on the destination", Severity: check.Warning, Detail: "recommended extension(s) missing: mbstring"},
	{ID: "db.import", Title: "Database import (destination)", Severity: check.Blocker, Detail: "the mysql client is missing on the destination — the database cannot be imported"},
}

func TestPlainCheckReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModePlain, &buf).CheckReport(checkResults); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"[ok]", "[warning]", "[BLOCKER]", "1 blocker(s), 1 warning(s)", "rerun 'rehost check'"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain check report missing %q:\n%s", want, out)
		}
	}
}

func TestPlainCheckReportGreen(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModePlain, &buf).CheckReport(checkResults[:1]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "All green") {
		t.Errorf("green report should say so:\n%s", buf.String())
	}
}

func TestJSONCheckReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeJSON, &buf).CheckReport(checkResults); err != nil {
		t.Fatal(err)
	}
	var env CheckEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("check report is not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.check-report.v1" || len(env.Results) != 3 || env.Blockers != 1 || env.Warnings != 1 || env.Green {
		t.Errorf("unexpected envelope: %+v", env)
	}
}

func TestStyledCheckReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeStyled, &buf).CheckReport(checkResults); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Compatibility check", "✓", "!", "✗"} {
		if !strings.Contains(out, want) {
			t.Errorf("styled check report missing %q:\n%s", want, out)
		}
	}
}
