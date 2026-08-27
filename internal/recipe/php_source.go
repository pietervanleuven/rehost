package recipe

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
