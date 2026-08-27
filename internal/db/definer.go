package db

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
)

// definerClause matches the DEFINER=<user>@<host> clause (identifiers
// backtick-quoted) a MySQL dump emits before a routine/trigger/view definition;
// the leading space is consumed so removal leaves no double space. Removing it
// makes the object default its definer to the importing account — the fix for
// shared hosts where the source's panel user does not exist on the destination
// and lacks SUPER to set a foreign one.
var definerClause = regexp.MustCompile("[ ]DEFINER=`(?:[^`]|``)*`@`(?:[^`]|``)*`")

// StripDefiners copies an uncompressed SQL dump from r to w, removing DEFINER
// clauses from DDL lines. It is applied to every dump before import so a
// cross-account import does not abort on ERROR 1227 (foreign definer without
// SUPER).
//
// Stripping is restricted to lines whose first non-blank content is `CREATE` or
// a `/*!…*/` version comment — the only lines mysqldump and the PHP helper put
// DEFINER on. Row data lives on `INSERT` lines, and both producers escape
// newlines inside string values (\n), so a physical line boundary is always a
// statement boundary: a DEFINER-shaped substring inside a table row can never
// be on a stripped line, and its data is left byte-exact.
func StripDefiners(r io.Reader, w io.Writer) error {
	// ReadBytes has no line-length cap, so an extended-INSERT megabyte line is
	// read whole rather than truncated the way bufio.Scanner would.
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(stripDefinerLine(line)); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func stripDefinerLine(line []byte) []byte {
	head := bytes.TrimLeft(line, " \t")
	if bytes.HasPrefix(head, []byte("CREATE ")) || bytes.HasPrefix(head, []byte("/*!")) {
		return definerClause.ReplaceAll(line, nil)
	}
	return line
}
