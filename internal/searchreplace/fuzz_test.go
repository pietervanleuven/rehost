package searchreplace

import (
	"bytes"
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
