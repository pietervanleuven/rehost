package searchreplace

import (
	"sort"
	"testing"
)

func TestPairs_URLVariantsAndOrder(t *testing.T) {
	got := Pairs(PlanInput{
		SourceURL: "https://old.example.com",
		DestURL:   "https://new.example.org",
	})
	want := []Pair{
		{`https:\/\/old.example.com`, `https:\/\/new.example.org`}, // escaped full (longest)
		{`https://old.example.com`, `https://new.example.org`},     // full
		{`\/\/old.example.com`, `\/\/new.example.org`},             // escaped protocol-relative
		{`//old.example.com`, `//new.example.org`},                 // protocol-relative
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// Longest-first is the load-bearing property: the full URL must precede its
	// protocol-relative substring.
	if !sort.SliceIsSorted(got, func(i, j int) bool { return len(got[i].From) > len(got[j].From) }) {
		t.Errorf("pairs not longest-first: %+v", got)
	}
	fullIdx, relIdx := -1, -1
	for i, p := range got {
		if p.From == "https://old.example.com" {
			fullIdx = i
		}
		if p.From == "//old.example.com" {
			relIdx = i
		}
	}
	if fullIdx < 0 || relIdx < 0 || fullIdx >= relIdx {
		t.Errorf("full URL must sort before protocol-relative: full=%d rel=%d", fullIdx, relIdx)
	}
}

func TestPairs_WithDocroot(t *testing.T) {
	got := Pairs(PlanInput{
		SourceURL:     "https://old.example.com/",
		DestURL:       "https://new.example.org",
		SourceDocroot: "/home/olduser/public_html/",
		DestDocroot:   "/home/newuser/public_html",
	})
	// Must include the raw and escaped docroot move.
	wantContains := []Pair{
		{"/home/olduser/public_html", "/home/newuser/public_html"},
		{`\/home\/olduser\/public_html`, `\/home\/newuser\/public_html`},
	}
	for _, w := range wantContains {
		if !containsPair(got, w) {
			t.Errorf("missing docroot pair %+v in %+v", w, got)
		}
	}
	// Trailing slash on the URL must not leak into pairs.
	for _, p := range got {
		if p.From == "https://old.example.com/" {
			t.Errorf("trailing slash not trimmed: %+v", p)
		}
	}
}

func TestPairs_SchemeUpgradeSkipsNoOpRelative(t *testing.T) {
	// http → https on the same host: the protocol-relative form is identical on
	// both sides, so it must be dropped (no-op) while the full URLs stay.
	got := Pairs(PlanInput{
		SourceURL: "http://example.com",
		DestURL:   "https://example.com",
	})
	for _, p := range got {
		if p.From == p.To {
			t.Errorf("no-op pair emitted: %+v", p)
		}
		if p.From == "//example.com" {
			t.Errorf("host-unchanged protocol-relative pair should be dropped: %+v", p)
		}
	}
	if !containsPair(got, Pair{"http://example.com", "https://example.com"}) {
		t.Errorf("full URL pair missing: %+v", got)
	}
}

func TestPairs_EmptyInput(t *testing.T) {
	if got := Pairs(PlanInput{}); len(got) != 0 {
		t.Errorf("empty input should yield no pairs, got %+v", got)
	}
}

func containsPair(pairs []Pair, want Pair) bool {
	for _, p := range pairs {
		if p == want {
			return true
		}
	}
	return false
}
