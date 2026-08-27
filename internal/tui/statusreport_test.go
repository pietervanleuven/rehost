package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pietervanleuven/rehost/internal/state"
)

func sampleEntries() []state.Entry {
	return []state.Entry{
		{Time: time.Now().Add(-2 * time.Hour), Event: "dry-run", Details: map[string]string{"sites": "2", "warnings": "1"}},
		{Time: time.Now().Add(-3 * 24 * time.Hour), Event: "dry-run", Details: map[string]string{"sites": "2", "warnings": "0"}},
	}
}

func TestHistoryReportRendersInAllModes(t *testing.T) {
	for _, mode := range []Mode{ModeStyled, ModePlain, ModeJSON} {
		var buf bytes.Buffer
		if err := New(mode, &buf).HistoryReport(sampleEntries()); err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if !strings.Contains(buf.String(), "dry-run") {
			t.Errorf("mode %d: history output missing event:\n%s", mode, buf.String())
		}
	}
}

func TestHistoryReportEmptyText(t *testing.T) {
	var styled, plain bytes.Buffer
	if err := New(ModeStyled, &styled).HistoryReport(nil); err != nil {
		t.Fatal(err)
	}
	if err := New(ModePlain, &plain).HistoryReport(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(styled.String()), "no runs recorded") {
		t.Errorf("styled empty history should say so:\n%s", styled.String())
	}
	if !strings.Contains(plain.String(), "no runs recorded yet") {
		t.Errorf("plain empty history should say so:\n%s", plain.String())
	}
}

func TestStatusReportTextMentionsFlowSteps(t *testing.T) {
	v := StatusView{
		ProjectFile: "migrate.yaml",
		Source:      "deploy@src.example.com",
		Destination: "", // not configured
		Sites:       nil,
		Recent:      nil,
	}
	for _, mode := range []Mode{ModeStyled, ModePlain} {
		var buf bytes.Buffer
		if err := New(mode, &buf).StatusReport(v); err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		out := buf.String()
		for _, want := range []string{
			"migrate.yaml",
			"deploy@src.example.com",
			"not configured",    // no destination
			"none detected yet", // no sites
			"not run yet",       // neither dry-run nor migrate has run
		} {
			if !strings.Contains(out, want) {
				t.Errorf("mode %d: status output missing %q:\n%s", mode, want, out)
			}
		}
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "unknown"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
		{now.Add(1 * time.Hour), "just now"}, // future / clock skew never goes negative
	}
	for _, c := range cases {
		if got := humanAge(c.t); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestFormatDetailsIsSorted(t *testing.T) {
	got := formatDetails(map[string]string{"warnings": "1", "sites": "2"})
	if got != "sites=2, warnings=1" {
		t.Errorf("formatDetails = %q, want deterministic sorted order", got)
	}
	if formatDetails(nil) != "" {
		t.Error("empty details should render as empty string")
	}
}
