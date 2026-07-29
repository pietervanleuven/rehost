package searchreplace

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// RewriteDump streams an uncompressed SQL dump from r to w, applying pairs
// inside single-quoted SQL string literals via Replacer (serialized-safe).
// Everything outside string literals — DDL, identifiers, comments, hex
// literals, the completion footer — passes through byte-exact, and so does
// any literal no pair matches, so an empty or non-matching rewrite is a
// byte-identical copy. This is how search-replace reaches the destination
// database before it exists there: the dump is rewritten locally between
// dump and import, needing no framework CLI on the destination.
//
// The scanner understands the dumps rehost itself produces (mysqldump and
// the PHP fallback): backslash escapes and doubled quotes inside literals,
// `-- ` line comments passed through whole so an apostrophe in a comment
// cannot open a phantom literal. One literal is buffered at a time.
func RewriteDump(r io.Reader, w io.Writer, pairs []Pair) (*Stats, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	bw := bufio.NewWriterSize(w, 64*1024)
	rep := &Replacer{}

	atLineStart := true
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &rep.Stats, err
		}

		// A `-- ` comment runs to end of line and may contain apostrophes
		// (skip notes with error messages); pass the whole line through.
		if atLineStart && b == '-' {
			if next, _ := br.Peek(1); len(next) == 1 && next[0] == '-' {
				if err := bw.WriteByte(b); err != nil {
					return &rep.Stats, err
				}
				if err := copyLine(br, bw); err != nil {
					return &rep.Stats, err
				}
				continue
			}
		}
		atLineStart = b == '\n'

		if b != '\'' {
			if err := bw.WriteByte(b); err != nil {
				return &rep.Stats, err
			}
			continue
		}
		if err := rewriteLiteral(br, bw, rep, pairs); err != nil {
			return &rep.Stats, err
		}
	}
	return &rep.Stats, bw.Flush()
}

// copyLine copies up to and including the next newline (or EOF).
func copyLine(br *bufio.Reader, bw *bufio.Writer) error {
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := bw.WriteByte(b); err != nil {
			return err
		}
		if b == '\n' {
			return nil
		}
	}
}

// rewriteLiteral consumes one single-quoted literal (opening quote already
// read), applies the pairs to its decoded value, and writes it back — the
// original escaped bytes verbatim when nothing matched, a re-encoded value
// when something did.
func rewriteLiteral(br *bufio.Reader, bw *bufio.Writer, rep *Replacer, pairs []Pair) error {
	var escaped bytes.Buffer
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return fmt.Errorf("unterminated string literal at end of dump")
		}
		if err != nil {
			return err
		}
		switch b {
		case '\\':
			nxt, err := br.ReadByte()
			if err != nil {
				return fmt.Errorf("unterminated escape in string literal")
			}
			escaped.WriteByte(b)
			escaped.WriteByte(nxt)
			continue
		case '\'':
			// '' is an escaped quote, a lone ' ends the literal.
			if next, _ := br.Peek(1); len(next) == 1 && next[0] == '\'' {
				_, _ = br.ReadByte()
				escaped.WriteString("''")
				continue
			}
		default:
			escaped.WriteByte(b)
			continue
		}
		break
	}

	raw := unescapeSQL(escaped.Bytes())
	changed := false
	for _, p := range pairs {
		if !bytes.Contains(raw, []byte(p.From)) {
			continue
		}
		if out, did := rep.Replace(raw, p.From, p.To); did {
			raw = out
			changed = true
		}
	}

	if err := bw.WriteByte('\''); err != nil {
		return err
	}
	if !changed {
		if _, err := bw.Write(escaped.Bytes()); err != nil {
			return err
		}
	} else if _, err := bw.Write(escapeSQL(raw)); err != nil {
		return err
	}
	return bw.WriteByte('\'')
}

// unescapeSQL decodes a literal's body: backslash escapes as the MySQL
// client library emits them, plus the doubled-quote form. Unknown escapes
// decode to the escaped byte itself, matching server behavior.
func unescapeSQL(esc []byte) []byte {
	if !bytes.ContainsAny(esc, `\'`) {
		return esc
	}
	out := make([]byte, 0, len(esc))
	for i := 0; i < len(esc); i++ {
		b := esc[i]
		if b == '\'' { // only reachable as '' — rewriteLiteral kept the pair
			out = append(out, '\'')
			i++
			continue
		}
		if b != '\\' || i+1 == len(esc) {
			out = append(out, b)
			continue
		}
		i++
		switch esc[i] {
		case '0':
			out = append(out, 0)
		case 'b':
			out = append(out, '\b')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'Z':
			out = append(out, 0x1a)
		default: // \' \" \\ and anything else: the byte itself
			out = append(out, esc[i])
		}
	}
	return out
}

// escapeSQL encodes a raw value for a single-quoted literal: quote,
// backslash and the control bytes mysqldump escapes. A double quote needs no
// escape inside single quotes (mysqldump leaves it bare too), keeping
// rewritten literals close to the surrounding dump's own style.
func escapeSQL(raw []byte) []byte {
	out := make([]byte, 0, len(raw)+8)
	for _, b := range raw {
		switch b {
		case 0:
			out = append(out, '\\', '0')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case 0x1a:
			out = append(out, '\\', 'Z')
		case '\'', '\\':
			out = append(out, '\\', b)
		default:
			out = append(out, b)
		}
	}
	return out
}
