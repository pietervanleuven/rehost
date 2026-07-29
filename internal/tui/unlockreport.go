package tui

import (
	"fmt"
	"text/tabwriter"
)

const unlockSchema = "rehost.unlock.v1"

// Unlock site statuses, shared by the command that fills an UnlockSite and the
// renderers that display it.
const (
	UnlockNotLocked = "not-locked" // was already off — no action needed
	UnlockCleared   = "unlocked"   // was locked and is now clear
	UnlockFailed    = "failed"     // was locked but could not be cleared
)

// UnlockSite is one site's unlock outcome.
type UnlockSite struct {
	Site      string `json:"site"`
	Framework string `json:"framework,omitempty"`
	Status    string `json:"status"`
	Method    string `json:"method,omitempty"` // which layer cleared it: wp-cli/file/drush/noop
	Detail    string `json:"detail,omitempty"` // why it failed, when it did
}

// UnlockView is what the unlock command renders: the source it acted on and
// the per-site results.
type UnlockView struct {
	Source string
	Sites  []UnlockSite
}

// tally counts the sites that needed unlocking, were cleared, and failed.
func (v UnlockView) tally() (locked, cleared, failed int) {
	for _, s := range v.Sites {
		switch s.Status {
		case UnlockCleared:
			locked++
			cleared++
		case UnlockFailed:
			locked++
			failed++
		}
	}
	return locked, cleared, failed
}

func unlockLine(s UnlockSite) string {
	switch s.Status {
	case UnlockCleared:
		via := s.Method
		if via == "" {
			via = "unknown"
		}
		return "cleared (" + via + ")"
	case UnlockFailed:
		msg := "could not clear"
		if s.Detail != "" {
			msg += ": " + s.Detail
		}
		return msg
	default:
		return "not locked"
	}
}

func (r styledRenderer) UnlockReport(v UnlockView) error {
	fprintln(r.out, titleStyle.Render("rehost unlock"))
	fprintf(r.out, "%s %s\n\n", roleStyle.Render("source:"), v.Source)
	locked, cleared, failed := v.tally()
	if locked == 0 {
		fprintln(r.out, okStyle.Render("Nothing to unlock — no site is in maintenance mode."))
		return nil
	}
	for _, s := range v.Sites {
		mark := okStyle.Render("✓")
		switch s.Status {
		case UnlockFailed:
			mark = missingStyle.Render("✗")
		case UnlockNotLocked:
			mark = dimStyle.Render("·")
		}
		fprintf(r.out, "  %s %s  %s\n", mark, s.Site, dimStyle.Render(unlockLine(s)))
	}
	fprintf(r.out, "\n%s\n", dimStyle.Render(fmt.Sprintf("%d cleared, %d failed", cleared, failed)))
	return nil
}

func (r plainRenderer) UnlockReport(v UnlockView) error {
	locked, _, _ := v.tally()
	if locked == 0 {
		fprintln(r.out, "nothing to unlock — no site is in maintenance mode")
		return nil
	}
	w := tabwriter.NewWriter(r.out, 2, 4, 2, ' ', 0)
	for _, s := range v.Sites {
		fprintf(w, "%s\t%s\n", s.Site, unlockLine(s))
	}
	return w.Flush()
}

// UnlockEnvelope is the versioned JSON shape of the unlock report.
type UnlockEnvelope struct {
	Schema  string       `json:"schema"`
	Source  string       `json:"source"`
	Locked  int          `json:"locked"`
	Cleared int          `json:"cleared"`
	Failed  int          `json:"failed"`
	Sites   []UnlockSite `json:"sites"`
}

func (r jsonRenderer) UnlockReport(v UnlockView) error {
	sites := v.Sites
	if sites == nil {
		sites = []UnlockSite{}
	}
	locked, cleared, failed := v.tally()
	return encodeJSON(r.out, UnlockEnvelope{
		Schema:  unlockSchema,
		Source:  v.Source,
		Locked:  locked,
		Cleared: cleared,
		Failed:  failed,
		Sites:   sites,
	})
}
