package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/rehost/internal/check"
)

func sampleMigrateView() MigrateReportView {
	return MigrateReportView{
		Preflight: MigratePreflightView{
			Results: []check.Result{{ID: "php.version", Title: "PHP", Severity: check.Ok, Detail: "8.3"}},
			Passed:  true,
		},
		Sites: []SiteSyncResult{
			{Site: "/home/u/public_html", DestRoot: "/home/d/www", Compressed: true,
				FilesSent: 42, BytesSent: 5 << 20, WireBytes: 2 << 20, FilesDeleted: 3, Duration: 1500 * time.Millisecond},
			{Site: "/home/u/drupal", DestRoot: "/home/d/drupal", Err: "destination extract failed"},
		},
		Notice:   "File sync converged, but the migration is NOT complete.",
		Warnings: []string{"could not record on the destination — next run needs --onto-existing"},
	}
}

func TestPlainMigrateReport(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModePlain, &buf).MigrateReport(sampleMigrateView()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"file sync:", "/home/u/public_html -> /home/d/www", "42 files", "gzip",
		"[FAILED]", "destination extract failed", "[warning]", "--onto-existing", "NOT complete",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain migrate report missing %q:\n%s", want, out)
		}
	}
}

func TestStyledMigrateReportRenders(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeStyled, &buf).MigrateReport(sampleMigrateView()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"File sync", "/home/d/www", "42 files", "destination extract failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("styled migrate report missing %q:\n%s", want, out)
		}
	}
}

func TestJSONMigrateReportEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := New(ModeJSON, &buf).MigrateReport(sampleMigrateView()); err != nil {
		t.Fatal(err)
	}
	var env MigrateEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if env.Schema != "rehost.migrate.v1" {
		t.Errorf("schema = %q", env.Schema)
	}
	if env.Complete {
		t.Error("Phase 3 migrate is never complete")
	}
	if env.Preflight.Schema != "rehost.migrate-preflight.v1" || !env.Preflight.Passed {
		t.Errorf("preflight section wrong: %+v", env.Preflight)
	}
	if len(env.Sites) != 2 || env.Sites[0].FilesSent != 42 || env.Sites[1].Err == "" {
		t.Errorf("sites not carried: %+v", env.Sites)
	}
	if len(env.Warnings) != 1 {
		t.Errorf("warnings not carried: %+v", env.Warnings)
	}
}
