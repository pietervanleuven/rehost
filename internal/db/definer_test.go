package db

import (
	"bytes"
	"strings"
	"testing"
)

func stripDefinersString(t *testing.T, in string) string {
	t.Helper()
	var out bytes.Buffer
	if err := StripDefiners(strings.NewReader(in), &out); err != nil {
		t.Fatalf("StripDefiners: %v", err)
	}
	return out.String()
}

func TestStripDefiners(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"trigger version comment",
			"/*!50003 CREATE*/ /*!50017 DEFINER=`u12345_src`@`localhost`*/ /*!50003 TRIGGER trg BEFORE INSERT*/;\n",
			"/*!50003 CREATE*/ /*!50017*/ /*!50003 TRIGGER trg BEFORE INSERT*/;\n",
		},
		{
			"view create line",
			"CREATE ALGORITHM=UNDEFINED DEFINER=`u`@`localhost` SQL SECURITY DEFINER VIEW `v` AS SELECT 1;\n",
			"CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS SELECT 1;\n",
		},
		{
			"routine",
			"CREATE DEFINER=`root`@`%` PROCEDURE `p`() BEGIN END;\n",
			"CREATE PROCEDURE `p`() BEGIN END;\n",
		},
		{
			"no definer passes through",
			"CREATE TABLE `t` (`id` int);\n",
			"CREATE TABLE `t` (`id` int);\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripDefinersString(t, c.in); got != c.want {
				t.Errorf("StripDefiners:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// A row value that merely contains DEFINER-shaped text must survive byte-exact:
// it is on an INSERT line, never a CREATE / version-comment line, so it is not
// touched. Corrupting row data would be worse than the bug being fixed.
func TestStripDefinersLeavesRowDataIntact(t *testing.T) {
	in := "INSERT INTO `posts` VALUES ('CREATE DEFINER=`a`@`b` PROCEDURE x','ok');\n"
	if got := stripDefinersString(t, in); got != in {
		t.Errorf("row data was mutated:\n got %q\nwant %q", got, in)
	}
}

// A final line without a trailing newline is still emitted whole.
func TestStripDefinersNoTrailingNewline(t *testing.T) {
	in := "CREATE DEFINER=`u`@`h` FUNCTION f() RETURNS int RETURN 1"
	want := "CREATE FUNCTION f() RETURNS int RETURN 1"
	if got := stripDefinersString(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
