package remote

import "testing"

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"mysqldump: error 1044\nmore context\n": "mysqldump: error 1044",
		"  spaced  \n":                          "spaced",
		"":                                      "no error output",
		"\n\n":                                  "no error output",
	}
	for in, want := range cases {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
