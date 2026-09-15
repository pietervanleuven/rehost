// Package searchreplace is the pure transformation core for the post-migration
// URL/path rewrite (PLAN.md §6, Phase 3). After a database is copied to the new
// host, absolute URLs and filesystem paths embedded in the data must change
// (https://old.example.com → https://new.example.com, /home/old/public_html →
// /home/new/public_html). WordPress and Drupal store many values as PHP-
// *serialized* structures where every string carries an explicit byte length
// (s:24:"https://old.example.com/";), so a naive SQL REPLACE desynchronizes the
// length from the content and corrupts the value on read. This package walks the
// serialized structure, replaces inside string scalars, and recomputes the byte
// lengths — the `wp search-replace --precise` contract.
//
// It is deliberately transport-free: how a replacement reaches the database
// (wp-cli remotely, or generated UPDATE statements) is the caller's concern.
package searchreplace

import (
	"bytes"
	"errors"
)

const maxInt = int(^uint(0) >> 1)

// defaultMaxDepth caps how deep doubly-serialized unwrapping recurses. Real
// WordPress data nests one, occasionally two levels (a serialized array holding
// a serialized widget); the cap only guards against pathological input.
const defaultMaxDepth = 20

// maxParseDepth bounds structural recursion in the parser so hostile input
// cannot exhaust the goroutine stack.
const maxParseDepth = 10000

// errParse marks any input that is not a complete, well-formed PHP-serialized
// value. Callers never see it — it only steers Replace between the structured,
// skip, and plain-text branches.
var errParse = errors.New("searchreplace: not valid php-serialized data")

// Stats accumulates across every value a Replacer touches so the migrate report
// can distinguish safe structural fixups from plain substitutions and, above
// all, surface the values it refused to touch.
type Stats struct {
	// ValuesExamined counts Replace calls (one per DB cell the caller feeds in).
	ValuesExamined int `json:"values_examined"`
	// ValuesChanged counts cells whose bytes changed.
	ValuesChanged int `json:"values_changed"`
	// SerializedFixups counts string scalars rewritten *inside* a serialized
	// structure — each one had its byte length recomputed.
	SerializedFixups int `json:"serialized_fixups"`
	// PlainReplacements counts non-serialized cells rewritten with a straight
	// substring replacement.
	PlainReplacements int `json:"plain_replacements"`
	// Unparseable counts cells that looked serialized but failed to parse and
	// were left untouched (see the corrupt-input policy on Replacer.Replace).
	Unparseable int `json:"unparseable"`
}

// Replacer performs serialization-aware search-and-replace and accumulates
// Stats. Create one per migration pass, reuse it across every cell, then read
// Stats. The zero value is ready to use.
type Replacer struct {
	Stats Stats
	// MaxDepth overrides the doubly-serialized recursion cap when > 0.
	MaxDepth int
}

func (r *Replacer) maxDepth() int {
	if r.MaxDepth > 0 {
		return r.MaxDepth
	}
	return defaultMaxDepth
}

// Replace rewrites every occurrence of from with to inside value and reports
// whether anything changed.
//
// It routes value one of three ways:
//
//   - Valid PHP-serialized data: the structure is walked, string scalars get a
//     plain substring replacement, byte lengths are recomputed, and the result
//     is reserialized. Reserializing an untouched payload is byte-identical.
//   - Not serialized-looking (an ordinary text column): a straight
//     bytes.ReplaceAll.
//   - Serialized-looking but not parseable (truncated, wrong length, missing
//     terminator): left completely untouched and counted in Stats.Unparseable.
//
// Corrupt-input policy — untouched, not plain-replaced. A plain byte
// replacement on damaged serialized data would produce exactly the length/
// content desync this package exists to prevent, and lengths we cannot parse
// cannot be fixed. Refusing is the only non-destructive option, so we return the
// original and flag it (Stats.Unparseable) for a human to inspect. This mirrors
// wp-cli, which leaves unparseable values alone. The serialized-looking test is
// intentionally conservative: it may flag an odd plain-text value (e.g. CSS
// beginning "a:") rather than risk corrupting real serialized data — a false
// skip is recoverable, a false rewrite is not.
func (r *Replacer) Replace(value []byte, from, to string) ([]byte, bool) {
	r.Stats.ValuesExamined++
	if from == "" || from == to {
		return value, false
	}

	if n, end, err := parse(value, 0, 0); err == nil && end == len(value) {
		if r.walk(n, from, to, 0) {
			out := serialize(n)
			r.Stats.ValuesChanged++
			return out, true
		}
		return value, false
	}

	if isSerializedLike(value) {
		r.Stats.Unparseable++
		return value, false
	}

	out := bytes.ReplaceAll(value, []byte(from), []byte(to))
	if !bytes.Equal(out, value) {
		r.Stats.ValuesChanged++
		r.Stats.PlainReplacements++
		return out, true
	}
	return value, false
}

// Replace is the stateless convenience wrapper around Replacer.Replace for
// callers that do not need aggregate statistics.
func Replace(value []byte, from, to string) ([]byte, bool) {
	return (&Replacer{}).Replace(value, from, to)
}

// walk mutates the parsed tree in place, replacing inside string scalars, and
// reports whether anything changed. Only strings in *value* positions are
// touched: array/object keys, class names, and mangled property names are
// carried through verbatim, matching wp-cli and preserving NUL-mangled
// visibility markers byte-for-byte.
func (r *Replacer) walk(n node, from, to string, depth int) bool {
	switch v := n.(type) {
	case *strVal:
		return r.walkStr(v, from, to, depth)
	case *arrVal:
		changed := false
		for i := range v.pairs {
			if r.walk(v.pairs[i].val, from, to, depth) {
				changed = true
			}
		}
		return changed
	case *objVal:
		changed := false
		for i := range v.props {
			if r.walk(v.props[i].val, from, to, depth) {
				changed = true
			}
		}
		return changed
	default: // rawScalar: integers, doubles, bools, null, references, custom.
		return false
	}
}

// walkStr handles one string scalar. WordPress stores doubly-serialized values
// (a serialized string whose content is itself serialized — widgets do this), so
// before treating the content as opaque text we test whether it is a complete
// serialized payload and, if so, recurse into it and reserialize. Otherwise the
// content gets a plain substring replacement and its byte length is recomputed
// by serialize.
func (r *Replacer) walkStr(s *strVal, from, to string, depth int) bool {
	if inner, end, err := parse(s.content, 0, 0); err == nil && end == len(s.content) {
		if depth >= r.maxDepth() {
			// Serialized content nested past the recursion cap: a plain
			// substring replacement would desync its inner length headers —
			// the one corruption this package promises never to produce. Per
			// the corrupt-input contract it stays untouched, counted only
			// when a replacement was actually due.
			if bytes.Contains(s.content, []byte(from)) {
				r.Stats.Unparseable++
			}
			return false
		}
		if r.walk(inner, from, to, depth+1) {
			s.content = serialize(inner)
			return true
		}
		return false
	}
	nc := bytes.ReplaceAll(s.content, []byte(from), []byte(to))
	if !bytes.Equal(nc, s.content) {
		s.content = nc
		r.Stats.SerializedFixups++
		return true
	}
	return false
}
