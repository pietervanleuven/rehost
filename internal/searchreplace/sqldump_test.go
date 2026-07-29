package searchreplace

import (
	"bytes"
	"strings"
	"testing"
)

func rewrite(t *testing.T, in string, pairs []Pair) (string, *Stats) {
	t.Helper()
	var out bytes.Buffer
	stats, err := RewriteDump(strings.NewReader(in), &out, pairs)
	if err != nil {
		t.Fatalf("RewriteDump: %v", err)
	}
	return out.String(), stats
}

func TestRewriteDumpPassthroughIsByteExact(t *testing.T) {
	in := "-- MySQL dump, don't panic\n" +
		"/*!40101 SET NAMES utf8mb4 */;\n" +
		"CREATE TABLE `t` (`v` text);\n" +
		"INSERT INTO `t` VALUES ('plain', 'it\\'s escaped', 'doubled '' quote', 0xDEADBEEF, NULL);\n" +
		"-- rehost: skipped view v: it's broken\n" +
		"-- Dump completed on 2026-07-29\n"
	out, _ := rewrite(t, in, []Pair{{From: "/home/old", To: "/home/new"}})
	if out != in {
		t.Errorf("non-matching rewrite must be byte-exact:\n got %q\nwant %q", out, in)
	}
}

func TestRewriteDumpPlainLiteral(t *testing.T) {
	in := "INSERT INTO `opts` VALUES ('/home/alice/public_html/uploads');\n"
	out, stats := rewrite(t, in, []Pair{{From: "/home/alice/public_html", To: "/home/bob/www"}})
	want := "INSERT INTO `opts` VALUES ('/home/bob/www/uploads');\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if stats.PlainReplacements != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestRewriteDumpSerializedLengthFixup(t *testing.T) {
	// s:23 counts the old path; the rewrite must recompute it (s:13).
	in := `INSERT INTO t VALUES ('a:1:{i:0;s:23:"/home/alice/public_html";}');` + "\n"
	out, stats := rewrite(t, in, []Pair{{From: "/home/alice/public_html", To: "/home/bob/www"}})
	want := `INSERT INTO t VALUES ('a:1:{i:0;s:13:"/home/bob/www";}');` + "\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if stats.SerializedFixups != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestRewriteDumpEscapedValueRoundTrips(t *testing.T) {
	// The value contains characters mysql escapes; after a replacement the
	// re-encoded literal must stay import-equivalent (\n stays \n etc.).
	in := `INSERT INTO t VALUES ('line1\nold-host it\'s');` + "\n"
	out, _ := rewrite(t, in, []Pair{{From: "old-host", To: "new-host"}})
	want := `INSERT INTO t VALUES ('line1\nnew-host it\'s');` + "\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestRewriteDumpCommentApostropheDoesNotOpenLiteral(t *testing.T) {
	in := "-- it's a comment with target /home/alice\n" +
		"INSERT INTO t VALUES ('/home/alice');\n"
	out, _ := rewrite(t, in, []Pair{{From: "/home/alice", To: "/home/bob"}})
	if !strings.Contains(out, "-- it's a comment with target /home/alice\n") {
		t.Errorf("comments must pass through untouched:\n%s", out)
	}
	if !strings.Contains(out, "VALUES ('/home/bob');") {
		t.Errorf("literal after comment must still be rewritten:\n%s", out)
	}
}

func TestRewriteDumpJSONEscapedVariant(t *testing.T) {
	// JSON in the DB stores https:\/\/host; in the dump each backslash is
	// itself escaped (\\). The pair from Pairs() carries the raw \/ form.
	in := `INSERT INTO t VALUES ('{"url":"https:\\/\\/old.example.com"}');` + "\n"
	out, _ := rewrite(t, in, []Pair{{From: `https:\/\/old.example.com`, To: `https:\/\/new.example.com`}})
	want := `INSERT INTO t VALUES ('{"url":"https:\\/\\/new.example.com"}');` + "\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestRewriteDumpUnterminatedLiteralFails(t *testing.T) {
	var out bytes.Buffer
	_, err := RewriteDump(strings.NewReader("INSERT INTO t VALUES ('oops"), &out, nil)
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("unterminated literal should fail, got %v", err)
	}
}
