package searchreplace

import (
	"sort"
	"strings"
)

// Pair is one ordered from→to substitution.
type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PlanInput describes the URL (and optional docroot) change a migration makes.
type PlanInput struct {
	SourceURL string // e.g. https://old.example.com
	DestURL   string // e.g. https://new.example.com

	// Optional filesystem docroot move, e.g. /home/old/public_html →
	// /home/new/public_html. Both must be set to emit path pairs.
	SourceDocroot string
	DestDocroot   string
}

// Pairs returns the ordered replacement pairs a migration needs, longest From
// first. It emits, for the URL change, the canonical form plus the two variants
// WordPress content actually stores — protocol-relative (//host) and JSON
// escaped-slash (https:\/\/host) — and their escaped combination, then the
// docroot move (raw and escaped) if given. Longest-first ordering guarantees the
// full URL is rewritten before its protocol-relative substring, so schemes stay
// consistent. It is a pure data function: no I/O, deterministic output.
func Pairs(in PlanInput) []Pair {
	var pairs []Pair
	add := func(from, to string) {
		if from == "" || from == to {
			return
		}
		pairs = append(pairs, Pair{From: from, To: to})
	}

	src := strings.TrimRight(in.SourceURL, "/")
	dst := strings.TrimRight(in.DestURL, "/")
	if src != "" && dst != "" {
		srcRel := schemeRelative(src) // //old.example.com
		dstRel := schemeRelative(dst)

		add(src, dst)                                     // https://old.example.com
		add(escapeSlashes(src), escapeSlashes(dst))       // https:\/\/old.example.com
		add(doubleEscape(src), doubleEscape(dst))         // https:\\\/\\\/… (Elementor's JSON-in-serialized)
		add(urlEncode(src), urlEncode(dst))               // https%3A%2F%2F… (urlencoded params)
		add(srcRel, dstRel)                               // //old.example.com
		add(escapeSlashes(srcRel), escapeSlashes(dstRel)) // \/\/old.example.com
		add(doubleEscape(srcRel), doubleEscape(dstRel))   // \\\/\\\/old.example.com
	}

	srcRoot := strings.TrimRight(in.SourceDocroot, "/")
	dstRoot := strings.TrimRight(in.DestDocroot, "/")
	if srcRoot != "" && dstRoot != "" {
		add(srcRoot, dstRoot)                               // /home/old/public_html
		add(escapeSlashes(srcRoot), escapeSlashes(dstRoot)) // \/home\/old\/public_html
		add(doubleEscape(srcRoot), doubleEscape(dstRoot))   // \\\/home\\\/old\\\/public_html
	}

	pairs = dedupePairs(pairs)
	// Longest From first; stable so equal-length pairs keep insertion order.
	sort.SliceStable(pairs, func(i, j int) bool {
		return len(pairs[i].From) > len(pairs[j].From)
	})
	return pairs
}

// schemeRelative drops the scheme, leaving the protocol-relative //host/path
// form that WordPress stores to let a page inherit http vs https.
func schemeRelative(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		return "//" + u[i+3:]
	}
	if strings.HasPrefix(u, "//") {
		return u
	}
	return "//" + u
}

// escapeSlashes produces the JSON-escaped form (/ → \/) WordPress writes when a
// URL is embedded in a JSON string inside serialized or plain content.
func escapeSlashes(s string) string {
	return strings.ReplaceAll(s, "/", `\/`)
}

// doubleEscape is escapeSlashes applied twice — the form JSON-inside-JSON
// content (Elementor page data is the notorious case) actually stores.
func doubleEscape(s string) string {
	return escapeSlashes(escapeSlashes(s))
}

// urlEncode percent-encodes the URL's scheme separator and slashes, the form
// a URL takes inside a query-string parameter (share links, redirect params).
var urlEncoder = strings.NewReplacer(":", "%3A", "/", "%2F")

func urlEncode(s string) string {
	return urlEncoder.Replace(s)
}

func dedupePairs(in []Pair) []Pair {
	seen := make(map[Pair]bool, len(in))
	out := in[:0]
	for _, p := range in {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
