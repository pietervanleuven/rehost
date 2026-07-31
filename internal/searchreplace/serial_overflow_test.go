package searchreplace

import (
	"strconv"
	"testing"
)

// A near-MaxInt length header must be rejected as unparseable, never indexed
// into (which overflows int to a negative index and panics). One case per
// length-carrying parser: string, object class, custom class, custom data.
func TestParse_HugeLengthHeaderDoesNotPanic(t *testing.T) {
	big := strconv.Itoa(maxInt)
	cases := map[string]string{
		"string":       `s:` + big + `:"x";`,
		"object-class": `O:` + big + `:"Cls":0:{}`,
		"custom-class": `C:` + big + `:"Cls":3:{xyz}`,
		"custom-data":  `C:3:"Cls":` + big + `:{xyz}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parse panicked on %q: %v", in, r)
				}
			}()
			if _, _, err := parse([]byte(in), 0, 0); err != errParse {
				t.Fatalf("parse(%q) err = %v, want errParse", in, err)
			}
		})
	}
}

// Replace treats the same input as damaged serialized data and leaves it
// untouched (Unparseable), rather than crashing the migration mid-run.
func TestReplace_HugeLengthHeaderSkipped(t *testing.T) {
	in := []byte(`s:` + strconv.Itoa(maxInt) + `:"x";`)
	rep := &Replacer{}
	out, did := rep.Replace(in, "x", "y")
	if did {
		t.Fatalf("Replace reported a change on unparseable input")
	}
	if string(out) != string(in) {
		t.Fatalf("Replace mutated unparseable input: %q", out)
	}
	if rep.Stats.Unparseable != 1 {
		t.Fatalf("Unparseable = %d, want 1", rep.Stats.Unparseable)
	}
}
