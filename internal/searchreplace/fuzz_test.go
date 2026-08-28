package searchreplace

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzReplace asserts the two invariants that matter for not destroying user
// sites: Replace never panics, and it never turns a value that parsed as valid
// serialized data into one that does not (structure and lengths stay consistent).
// It also checks the pure round-trip identity of parse+serialize.
func FuzzReplace(f *testing.F) {
	seeds := []string{
		`s:24:"https://old.example.com/";`,
		`a:1:{i:0;s:23:"https://old.example.com";}`,
		`O:3:"Foo":1:{s:3:"url";s:23:"https://old.example.com";}`,
		`a:2:{i:0;s:5:"hello";}`, // corrupt: count too high
		`s:100:"short";`,         // corrupt: length too high
		`N;`,
		`b:1;`,
		`i:-42;`,
		`d:3.14;`,
		`plain https://old.example.com text`,
		"\x00\x01\x02",
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s), "old.example.com", "new.example.org")
	}

	f.Fuzz(func(t *testing.T, value []byte, from, to string) {
		parsedBefore := parsesFully(value)

		// Round-trip identity: anything that parses fully must reserialize to
		// the exact input bytes.
		if parsedBefore {
			n, _, _ := parse(value, 0, 0)
			if got := serialize(n); !bytes.Equal(got, value) {
				t.Fatalf("round-trip not identical:\n in=%q\nout=%q", value, got)
			}
		}

		out, changed := Replace(value, from, to) // must not panic

		if !changed && !bytes.Equal(out, value) {
			t.Fatalf("changed=false but bytes differ:\n in=%q\nout=%q", value, out)
		}

		// If the input was valid serialized data, the output must remain valid
		// serialized data — the whole point of the package.
		if parsedBefore && !parsesFully(out) {
			t.Fatalf("valid input became invalid:\n in=%q\nout=%q", value, out)
		}
	})
}

// FuzzRewriteDump asserts the dump rewriter's contract over arbitrary dump
// bytes: it never panics, a rewrite with no pairs (or a pair the dump does
// not contain) is a byte-identical copy, and rewritten output still rewrites
// cleanly (no phantom literal desync carried forward).
func FuzzRewriteDump(f *testing.F) {
	seeds := []string{
		"INSERT INTO t VALUES ('https://old.example.com/x');\n",
		"-- comment with 'apostrophe\nINSERT INTO t VALUES ('a');\n",
		"/*!50003 CREATE TRIGGER x -- don't */;\nINSERT INTO t VALUES ('b');\n",
		"CREATE TABLE `it's odd` (id int);\n",
		"INSERT INTO t VALUES ('esc\\'aped', 'do''ubled');\n",
		"INSERT INTO t VALUES ('" + `a:1:{i:0;s:23:"https://old.example.com";}` + "');\n",
		"'unterminated",
		"/* unterminated comment",
		"`unterminated ident",
		"",
	}
	for _, s := range seeds {
		f.Add([]byte(s), "old.example.com", "new.example.org")
	}

	f.Fuzz(func(t *testing.T, dump []byte, from, to string) {
		var noPairs bytes.Buffer
		if _, err := RewriteDump(bytes.NewReader(dump), &noPairs, nil); err == nil {
			if !bytes.Equal(noPairs.Bytes(), dump) {
				t.Fatalf("no-pair rewrite must be byte-identical:\n in=%q\nout=%q", dump, noPairs.Bytes())
			}
		}

		if from == "" || from == to {
			return
		}
		var out bytes.Buffer
		if _, err := RewriteDump(bytes.NewReader(dump), &out, []Pair{{From: from, To: to}}); err != nil {
			return // unterminated literals are a legitimate error
		}
		// A literal's decoded value can contain from even when the escaped
		// dump bytes do not ('\0' decodes to a NUL byte), so the containment
		// check is only sound for froms free of quote/backslash/escape-target
		// bytes.
		plainFrom := !strings.ContainsAny(from, "\\'\x00\b\n\r\t\x1a")
		if plainFrom && !bytes.Contains(dump, []byte(from)) && !bytes.Equal(out.Bytes(), dump) {
			t.Fatalf("non-matching rewrite must be byte-identical:\n in=%q\nout=%q", dump, out.Bytes())
		}
		// The output must still scan: rewriting it again with no pairs is a
		// clean byte-identical pass, proving no desync was introduced.
		var again bytes.Buffer
		if _, err := RewriteDump(bytes.NewReader(out.Bytes()), &again, nil); err == nil {
			if !bytes.Equal(again.Bytes(), out.Bytes()) {
				t.Fatalf("rewritten dump does not re-scan cleanly:\n out=%q\nagain=%q", out.Bytes(), again.Bytes())
			}
		}
	})
}
