package recipe

import (
	"regexp"
	"strings"
)

// maskPHPComments returns a copy of src with every comment byte replaced by a
// space, preserving length and all non-comment bytes (including string-literal
// contents) exactly. Callers match `$databases` entries against the mask and
// then splice into the original at the same offsets, so a match inside a
// comment — the `@code … @endcode` example Drupal ships in default.settings.php
// — is never mistaken for the real connection block.
//
// It tracks single- and double-quoted strings so a `//`, `#` or `/*` inside a
// string literal is not read as a comment. Heredocs/nowdocs are not modeled;
// settings.php connection blocks do not use them.
func maskPHPComments(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	const (
		code = iota
		single
		double
		line
		block
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '\'':
				state = single
			case c == '"':
				state = double
			case c == '#':
				out[i] = ' '
				state = line
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				out[i], out[i+1] = ' ', ' '
				i++
				state = line
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				state = block
			}
		case single:
			if c == '\\' && i+1 < len(src) {
				i++ // skip the escaped byte
			} else if c == '\'' {
				state = code
			}
		case double:
			if c == '\\' && i+1 < len(src) {
				i++
			} else if c == '"' {
				state = code
			}
		case line:
			if c == '\n' {
				state = code // keep the newline
			} else {
				out[i] = ' '
			}
		case block:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' { // keep newlines so line/regex anchoring survives
				out[i] = ' '
			}
		}
	}
	return out
}

// phpUnescape decodes the escapes a PHP quoted literal supports for its own
// quote character: \<quote> and \\ become the plain byte, any other backslash
// stays literal (the single-quote rule; for double quotes this is the safe
// subset — \n-style escapes in DB credentials do not occur in practice).
func phpUnescape(s string, quote byte) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == quote) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// quotedValue extracts a regex match's quoted-literal capture: m[first] holds
// a single-quoted body, m[first+1] a double-quoted one. The decoded value is
// returned with the surrounding quotes gone and escapes resolved.
func quotedValue(m [][]byte, first int) string {
	if m[first] != nil {
		return phpUnescape(string(m[first]), '\'')
	}
	return phpUnescape(string(m[first+1]), '"')
}

// phpStatementEnd scans comment-masked PHP for the `;` that terminates the
// statement starting at from, skipping string literals and bracketed nesting.
// Returns -1 when the statement never terminates.
func phpStatementEnd(masked []byte, from int) int {
	depth := 0
	const (
		code = iota
		single
		double
	)
	state := code
	for i := from; i < len(masked); i++ {
		c := masked[i]
		switch state {
		case code:
			switch c {
			case '\'':
				state = single
			case '"':
				state = double
			case '(', '[':
				depth++
			case ')', ']':
				depth--
			case ';':
				if depth == 0 {
					return i
				}
			}
		case single:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '\'' {
				state = code
			}
		case double:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '"' {
				state = code
			}
		}
	}
	return -1
}

// drupalDefaultKeyAt anchors a 'default' => entry with an array value.
var drupalDefaultKeyAt = regexp.MustCompile(`^['"]default['"]\s*=>\s*(?:array\s*\(|\[)`)

// phpMatchingClose scans comment-masked PHP from just past an opening
// bracket (depth 1) to the index of its matching close, skipping strings.
func phpMatchingClose(masked []byte, from int) (int, bool) {
	depth := 1
	const (
		code = iota
		single
		double
	)
	state := code
	for i := from; i < len(masked); i++ {
		c := masked[i]
		switch state {
		case code:
			switch c {
			case '\'':
				state = single
			case '"':
				state = double
			case '(', '[':
				depth++
			case ')', ']':
				depth--
				if depth == 0 {
					return i, true
				}
			}
		case single:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '\'' {
				state = code
			}
		case double:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '"' {
				state = code
			}
		}
	}
	return 0, false
}

// phpDefaultValueRange returns the range (brackets included) of the first
// top-level 'default' => array(...) / [...] value in a comment-masked array
// literal region. Top-level means the key sits directly inside the region's
// outermost bracket — D7's $databases carries a 'default' target key inside
// every connection, so one nested in an earlier connection ('migrate') must
// not match.
func phpDefaultValueRange(masked []byte) (int, int, bool) {
	depth := 0
	const (
		code = iota
		single
		double
	)
	state := code
	for i := 0; i < len(masked); i++ {
		c := masked[i]
		switch state {
		case code:
			switch c {
			case '\'', '"':
				if depth == 1 {
					if loc := drupalDefaultKeyAt.FindIndex(masked[i:]); loc != nil {
						open := i + loc[1] - 1
						end, ok := phpMatchingClose(masked, open+1)
						if !ok {
							return 0, 0, false
						}
						return open, end + 1, true
					}
				}
				if c == '\'' {
					state = single
				} else {
					state = double
				}
			case '(', '[':
				depth++
			case ')', ']':
				depth--
			}
		case single:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '\'' {
				state = code
			}
		case double:
			if c == '\\' && i+1 < len(masked) {
				i++
			} else if c == '"' {
				state = code
			}
		}
	}
	return 0, 0, false
}

var (
	// $databases['default']['default'] = … — the explicit default connection.
	drupalDefaultAssign = regexp.MustCompile(`\$databases\s*\[\s*['"]default['"]\s*\]\s*\[\s*['"]default['"]\s*\]\s*=`)
	// Any $databases assignment ($databases = …, or with other subscripts).
	drupalAnyAssign = regexp.MustCompile(`\$databases\s*(?:\[[^\]\n]*\]\s*)*=`)
)

// drupalDefaultConnRange locates the byte range of the default connection's
// array literal in comment-masked settings.php. Real files routinely carry
// memcache/redis servers, SMTP config, or a 'migrate' connection before
// $databases — matching keys anywhere in the file would read or rewrite the
// wrong block, so both extraction and splicing constrain themselves to this
// range. Mask offsets equal original offsets, so the range indexes both.
func drupalDefaultConnRange(masked []byte) (int, int, bool) {
	if loc := drupalDefaultAssign.FindIndex(masked); loc != nil {
		if end := phpStatementEnd(masked, loc[1]); end >= 0 {
			return loc[1], end, true
		}
	}
	loc := drupalAnyAssign.FindIndex(masked)
	if loc == nil {
		return 0, 0, false
	}
	start, end := loc[1], phpStatementEnd(masked, loc[1])
	if end < 0 {
		return 0, 0, false
	}
	// $databases = ['default' => ['default' => [...]]] (the D7 shape):
	// narrow through the nested 'default' keys as far as they go.
	for {
		s, e, ok := phpDefaultValueRange(masked[start:end])
		if !ok {
			break
		}
		start, end = start+s, start+e
	}
	return start, end, true
}
