package searchreplace

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

func parsesFully(b []byte) bool {
	n, end, err := parse(b, 0, 0)
	return err == nil && end == len(b) && n != nil
}

// collectValueStrings gathers the byte content of every string in a *value*
// position (keys and class/property names excluded), used to prove replacements
// reached every value leaf and skipped every key.
func collectValueStrings(n node, out *[][]byte) {
	switch v := n.(type) {
	case *strVal:
		*out = append(*out, v.content)
	case *arrVal:
		for _, p := range v.pairs {
			collectValueStrings(p.val, out)
		}
	case *objVal:
		for _, p := range v.props {
			collectValueStrings(p.val, out)
		}
	}
}

func mustParse(t *testing.T, b []byte) node {
	t.Helper()
	n, end, err := parse(b, 0, 0)
	if err != nil || end != len(b) {
		t.Fatalf("parse failed: err=%v end=%d len=%d value=%q", err, end, len(b), b)
	}
	return n
}

// --- Round-trip identity -----------------------------------------------------

func TestReplace_RoundTripIdentity(t *testing.T) {
	// A payload that contains neither `from` nor `to` must come back byte-for-byte.
	cases := map[string]any{
		"null":      phpNull{},
		"bool":      true,
		"int":       -42,
		"double":    3.14159,
		"double_e":  1.0e20,
		"string":    "just a plain value",
		"empty_str": "",
		"utf8":      "café 日本語 🎉",
		"list":      phpArr{{0, "aa"}, {1, "bb"}, {2, 99}},
		"map":       phpArr{{"one", 1}, {"two", "zwei"}, {"nested", phpArr{{"x", true}}}},
		"object":    phpObj{Class: "stdClass", Props: []phpKV{{"a", 1}, {"b", "text"}}},
		"deep":      phpArr{{"o", phpObj{Class: "Widget", Props: []phpKV{{"items", phpArr{{0, "one"}, {1, "two"}}}}}}},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			in := phpSerialize(v)
			// Sanity: the production parser accepts the oracle's output.
			if !parsesFully(in) {
				t.Fatalf("production parser rejected valid input %q", in)
			}
			out, changed := Replace(in, "ABSENT-NEEDLE", "REPLACEMENT")
			if changed {
				t.Errorf("changed=true for absent needle")
			}
			if !bytes.Equal(out, in) {
				t.Errorf("not byte-identical:\n in=%q\nout=%q", in, out)
			}
		})
	}
}

// --- Byte-length correctness, including multibyte -----------------------------

func TestReplace_LengthRecomputed(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		from, to    string
		wantContent string
	}{
		{"grow", "https://old.example.com/", "old.example.com", "new.example.org", "https://new.example.org/"},
		{"shrink", "https://longhost.example.com/", "longhost.example.com", "x.io", "https://x.io/"},
		{"utf8_content", "café🎉/old-path", "old-path", "新しい", "café🎉/新しい"},
		{"utf8_needle", "prefix-café.example-suffix", "café.example", "日本.example", "prefix-日本.example-suffix"},
		{"multi_occurrence", "old old old", "old", "brand-new", "brand-new brand-new brand-new"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(phpStr(tc.content))
			out, changed := Replace(in, tc.from, tc.to)
			if !changed {
				t.Fatalf("expected change")
			}
			want := []byte(phpStr(tc.wantContent))
			if !bytes.Equal(out, want) {
				t.Fatalf("got  %q\nwant %q", out, want)
			}
			// The header must be a byte length, and the value must re-parse.
			if !parsesFully(out) {
				t.Fatalf("result does not re-parse: %q", out)
			}
		})
	}
}

// --- Nested structures --------------------------------------------------------

func TestReplace_NestedArrayObject(t *testing.T) {
	in := phpSerialize(phpArr{
		{"site", "https://old.example.com"},
		{"obj", phpObj{Class: "Config", Props: []phpKV{
			{"home", "https://old.example.com/home"},
			{"list", phpArr{{0, "https://old.example.com/a"}, {1, "no-url-here"}}},
		}}},
	})
	out, changed := Replace(in, "old.example.com", "new.example.org")
	if !changed {
		t.Fatal("expected change")
	}
	if !parsesFully(out) {
		t.Fatalf("result does not re-parse: %q", out)
	}
	var strs [][]byte
	collectValueStrings(mustParse(t, out), &strs)
	for _, s := range strs {
		if bytes.Contains(s, []byte("old.example.com")) {
			t.Errorf("value still contains old host: %q", s)
		}
	}
}

// --- Doubly-serialized (WordPress widgets) ------------------------------------

func TestReplace_DoublySerialized(t *testing.T) {
	inner := phpSerialize(phpArr{{"url", "https://old.example.com/x"}, {"n", 3}})
	// A string whose content is itself a serialized array.
	in := phpSerialize(string(inner))

	r := &Replacer{}
	out, changed := r.Replace(in, "old.example.com", "new.example.org")
	if !changed {
		t.Fatal("expected change")
	}
	wantInner := phpSerialize(phpArr{{"url", "https://new.example.org/x"}, {"n", 3}})
	want := phpSerialize(string(wantInner))
	if !bytes.Equal(out, want) {
		t.Fatalf("doubly-serialized mismatch:\n got %q\nwant %q", out, want)
	}
	if r.Stats.SerializedFixups != 1 {
		t.Errorf("SerializedFixups=%d want 1", r.Stats.SerializedFixups)
	}
	if r.Stats.ValuesChanged != 1 {
		t.Errorf("ValuesChanged=%d want 1", r.Stats.ValuesChanged)
	}
}

func TestReplace_DepthCapLeavesNestedSerializedUntouched(t *testing.T) {
	// With MaxDepth=1 the outer string is unwrapped once; a further nested
	// serialized payload sits at the cap. A plain replacement there would
	// desync its inner length headers (the one corruption this package
	// promises never to produce), so it stays untouched and is counted.
	deepInner := phpSerialize("https://old.example.com") // depth 2 payload
	midInner := phpSerialize(string(deepInner))          // depth 1 payload
	in := phpSerialize(string(midInner))                 // depth 0 payload
	r := &Replacer{MaxDepth: 1}
	out, changed := r.Replace(in, "old.example.com", "much-longer-new.example.org")
	if changed {
		t.Fatal("content past the depth cap must not be touched")
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("input must survive byte-exact:\n got %q\nwant %q", out, in)
	}
	if r.Stats.Unparseable != 1 {
		t.Errorf("Unparseable=%d want 1 (the skipped value must be reported)", r.Stats.Unparseable)
	}
}

// --- Objects with private/protected mangled property names --------------------

func TestReplace_MangledPropertyNames(t *testing.T) {
	// Private:  \0Class\0prop ; Protected: \0*\0prop — NUL bytes in the key are
	// a classic naive-replace corruption source. Keys must be preserved verbatim.
	privKey := "\x00Foo\x00secret" // 11 bytes
	protKey := "\x00*\x00prot"     // 8 bytes
	in := phpSerialize(phpObj{Class: "Foo", Props: []phpKV{
		{privKey, "https://old.example.com/s"},
		{protKey, "see https://old.example.com"},
		{"pub", "https://old.example.com"},
	}})
	out, changed := Replace(in, "old.example.com", "new.example.org")
	if !changed {
		t.Fatal("expected change")
	}
	obj, ok := mustParse(t, out).(*objVal)
	if !ok {
		t.Fatalf("expected object, got %T", mustParse(t, out))
	}
	if string(obj.class) != "Foo" {
		t.Errorf("class mangled: %q", obj.class)
	}
	wantKeys := []string{phpStr(privKey), phpStr(protKey), phpStr("pub")}
	for i, p := range obj.props {
		if string(p.keyRaw) != wantKeys[i] {
			t.Errorf("prop %d key changed:\n got %q\nwant %q", i, p.keyRaw, wantKeys[i])
		}
	}
	var strs [][]byte
	collectValueStrings(obj, &strs)
	for _, s := range strs {
		if bytes.Contains(s, []byte("old.example.com")) {
			t.Errorf("value not replaced: %q", s)
		}
	}
}

// --- Corrupt input is left alone and flagged ----------------------------------

func TestReplace_CorruptSkipped(t *testing.T) {
	corrupt := map[string]string{
		"count_too_high":   `a:2:{i:0;s:5:"hello";}`,
		"len_too_high":     `s:100:"short";`,
		"len_too_low":      `s:2:"short";`,
		"truncated_array":  `a:1:{i:0;s:24:"https://old.example`,
		"missing_brace":    `O:3:"Foo":1:{s:3:"url";s:5:"hello"`,
		"bad_bool":         `b:3;`,
		"looks_serialized": `a:hover { color: red }`, // CSS beginning "a:" — conservative skip
	}
	for name, s := range corrupt {
		t.Run(name, func(t *testing.T) {
			in := []byte(s)
			r := &Replacer{}
			out, changed := r.Replace(in, "old.example", "new.example")
			if changed {
				t.Errorf("changed=true for corrupt input")
			}
			if !bytes.Equal(out, in) {
				t.Errorf("corrupt input mutated:\n in=%q\nout=%q", in, out)
			}
			if r.Stats.Unparseable != 1 {
				t.Errorf("Unparseable=%d want 1", r.Stats.Unparseable)
			}
		})
	}
}

// --- Plain (non-serialized) text ---------------------------------------------

func TestReplace_PlainText(t *testing.T) {
	r := &Replacer{}
	in := []byte("Visit https://old.example.com now, https://old.example.com!")
	out, changed := r.Replace(in, "https://old.example.com", "https://new.example.org")
	if !changed {
		t.Fatal("expected change")
	}
	want := []byte("Visit https://new.example.org now, https://new.example.org!")
	if !bytes.Equal(out, want) {
		t.Fatalf("got %q want %q", out, want)
	}
	if r.Stats.PlainReplacements != 1 || r.Stats.SerializedFixups != 0 {
		t.Errorf("stats: plain=%d serialized=%d", r.Stats.PlainReplacements, r.Stats.SerializedFixups)
	}
}

func TestReplace_NoOpGuards(t *testing.T) {
	in := []byte("anything at all")
	if out, changed := Replace(in, "", "x"); changed || !bytes.Equal(out, in) {
		t.Errorf("empty from should be a no-op")
	}
	if out, changed := Replace(in, "same", "same"); changed || !bytes.Equal(out, in) {
		t.Errorf("from==to should be a no-op")
	}
}

// --- Realistic WordPress wp_options widget_text fixture -----------------------

func TestReplace_WordPressWidgetFixture(t *testing.T) {
	html := `<a href="https://old.example.com/page">Link</a>`
	in := phpSerialize(phpArr{
		{2, phpArr{
			{"title", "My Widget"},
			{"text", html},
		}},
		{"_multiwidget", 1},
	})
	// Confirm it looks like a real option value (nested serialized array).
	if !parsesFully(in) {
		t.Fatalf("fixture is not valid serialized data: %q", in)
	}
	r := &Replacer{}
	out, changed := r.Replace(in, "https://old.example.com", "https://new.example.org")
	if !changed {
		t.Fatal("expected change")
	}
	wantHTML := `<a href="https://new.example.org/page">Link</a>`
	want := phpSerialize(phpArr{
		{2, phpArr{
			{"title", "My Widget"},
			{"text", wantHTML},
		}},
		{"_multiwidget", 1},
	})
	if !bytes.Equal(out, want) {
		t.Fatalf("widget fixture mismatch:\n got %q\nwant %q", out, want)
	}
	if r.Stats.SerializedFixups != 1 {
		t.Errorf("SerializedFixups=%d want 1", r.Stats.SerializedFixups)
	}
}

// --- Stats aggregation across a mixed batch -----------------------------------

func TestReplace_StatsAggregate(t *testing.T) {
	r := &Replacer{}
	r.Replace([]byte(phpStr("https://old.example.com")), "old.example.com", "new.example.org") // serialized fixup
	r.Replace([]byte("plain https://old.example.com"), "old.example.com", "new.example.org")   // plain
	r.Replace([]byte(`s:100:"broken";`), "old.example.com", "new.example.org")                 // unparseable
	r.Replace([]byte(phpStr("no needle here")), "old.example.com", "new.example.org")          // examined, unchanged

	got := r.Stats
	want := Stats{ValuesExamined: 4, ValuesChanged: 2, SerializedFixups: 1, PlainReplacements: 1, Unparseable: 1}
	if got != want {
		t.Errorf("stats mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// --- Property test: random nested trees ---------------------------------------

// genTree builds a random serializable tree. Value strings always start with a
// non-serializable prefix so they are treated as leaf text (never accidental
// doubly-serialized), and may embed the marker to exercise replacement.
func genTree(rng *rand.Rand, depth int, marker string) any {
	if depth <= 0 || rng.Intn(3) == 0 {
		switch rng.Intn(6) {
		case 0:
			return phpNull{}
		case 1:
			return rng.Intn(2) == 0
		case 2:
			return rng.Intn(1000) - 500
		case 3:
			return float64(rng.Intn(1000)) / 7.0
		default:
			return genString(rng, marker)
		}
	}
	n := rng.Intn(4)
	if rng.Intn(2) == 0 {
		arr := make(phpArr, n)
		for i := range arr {
			var key any = i
			if rng.Intn(2) == 0 {
				key = "k" + genKeyToken(rng)
			}
			arr[i] = phpKV{Key: key, Val: genTree(rng, depth-1, marker)}
		}
		return arr
	}
	props := make([]phpKV, n)
	for i := range props {
		props[i] = phpKV{Key: "p" + genKeyToken(rng), Val: genTree(rng, depth-1, marker)}
	}
	return phpObj{Class: "C" + genKeyToken(rng), Props: props}
}

func genString(rng *rand.Rand, marker string) string {
	// "val-" guarantees it never parses as serialized data.
	parts := []string{"val-café", "🎉x", "日本", marker, "tail"}
	rng.Shuffle(len(parts), func(i, j int) { parts[i], parts[j] = parts[j], parts[i] })
	keep := rng.Intn(len(parts)) + 1
	return "val-" + strings.Join(parts[:keep], "-")
}

func genKeyToken(rng *rand.Rand) string {
	// Keys use a disjoint alphabet so the marker never appears in a key.
	return string(rune('A' + rng.Intn(26)))
}

func TestReplace_PropertyRoundTripAndReplace(t *testing.T) {
	const marker = "old.example.com"
	const repl = "much-longer-new.example.org-日本" // multibyte, different byte length
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		tree := genTree(rng, 4, marker)
		in := phpSerialize(tree)
		if !parsesFully(in) {
			t.Fatalf("oracle produced input the parser rejects: %q", in)
		}

		// Invariant 1: absent needle → byte-identical, unchanged.
		if out, changed := Replace(in, "ZZ-absent-ZZ", "x"); changed || !bytes.Equal(out, in) {
			t.Fatalf("identity broken (i=%d): changed=%v\n in=%q\nout=%q", i, changed, in, out)
		}

		// Invariant 2: replacing the marker leaves no marker in any value, and
		// the result is still valid serialized data (lengths correct).
		out, _ := Replace(in, marker, repl)
		if !parsesFully(out) {
			t.Fatalf("replaced output not re-parseable (i=%d): %q", i, out)
		}
		var strs [][]byte
		collectValueStrings(mustParse(t, out), &strs)
		for _, s := range strs {
			if bytes.Contains(s, []byte(marker)) {
				t.Fatalf("marker survived in value (i=%d): %q", i, s)
			}
		}
	}
}
