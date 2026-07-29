package searchreplace

import (
	"bytes"
	"strconv"
)

// node is one element of a parsed PHP-serialized value. The tree is walked to
// replace inside strings, then reserialized. rawScalar carries verbatim bytes so
// integers and doubles round-trip regardless of PHP's number formatting; strVal
// carries decoded content so its byte length can be recomputed after a change.
type node interface{ isNode() }

// rawScalar is any value we never look inside: null, bool, int, double,
// reference (R:/r:), and custom-serialized (C:) blocks. Emitted verbatim.
type rawScalar struct{ raw []byte }

// strVal is a PHP string. content is the decoded bytes (the declared length of
// them); serialize recomputes the length header from len(content).
type strVal struct{ content []byte }

// arrVal is a PHP array: an ordered list of key/value pairs. keyRaw is the exact
// serialized key, emitted verbatim (keys are never rewritten).
type arrVal struct{ pairs []kv }

// objVal is a PHP object. class is the decoded class name (verbatim on output);
// props are its properties, keyed like an array.
type objVal struct {
	class []byte
	props []kv
}

type kv struct {
	keyRaw []byte
	val    node
}

func (rawScalar) isNode() {}
func (*strVal) isNode()   {}
func (*arrVal) isNode()   {}
func (*objVal) isNode()   {}

// parse reads exactly one serialized value starting at pos and returns it, the
// index just past it, and an error. A value is valid only when its declared
// string/class lengths match the bytes present and every terminator is in place;
// any deviation yields errParse so the caller can refuse to touch it.
func parse(b []byte, pos, depth int) (node, int, error) {
	if depth > maxParseDepth {
		return nil, pos, errParse
	}
	if pos >= len(b) {
		return nil, pos, errParse
	}
	switch b[pos] {
	case 'N': // N;
		if pos+1 < len(b) && b[pos+1] == ';' {
			return rawScalar{b[pos : pos+2]}, pos + 2, nil
		}
		return nil, pos, errParse
	case 'b': // b:0; or b:1;
		if pos+3 < len(b) && b[pos+1] == ':' && (b[pos+2] == '0' || b[pos+2] == '1') && b[pos+3] == ';' {
			return rawScalar{b[pos : pos+4]}, pos + 4, nil
		}
		return nil, pos, errParse
	case 'i': // i:<int>;
		return parseIntLike(b, pos)
	case 'R', 'r': // R:<n>;  r:<n>;  (references)
		return parseIntLike(b, pos)
	case 'd': // d:<float>;
		return parseDouble(b, pos)
	case 's': // s:<len>:"<bytes>";
		return parseString(b, pos)
	case 'a': // a:<count>:{ <key><val>... }
		return parseArray(b, pos, depth)
	case 'O': // O:<len>:"<class>":<count>:{ <key><val>... }
		return parseObject(b, pos, depth)
	case 'C': // C:<len>:"<class>":<datalen>:{<data>}  (Serializable)
		return parseCustom(b, pos)
	}
	return nil, pos, errParse
}

// parseIntLike consumes i:/R:/r: tokens: a colon, an optional sign, one or more
// digits, and a terminating semicolon, captured verbatim.
func parseIntLike(b []byte, pos int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	j := pos + 2
	if j < len(b) && (b[j] == '-' || b[j] == '+') {
		j++
	}
	start := j
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		j++
	}
	if j == start || j >= len(b) || b[j] != ';' {
		return nil, pos, errParse
	}
	return rawScalar{b[pos : j+1]}, j + 1, nil
}

// parseDouble consumes d:<token>; and validates the token is a real float
// (including INF/-INF/NAN, which strconv.ParseFloat accepts case-insensitively).
func parseDouble(b []byte, pos int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	j := pos + 2
	for j < len(b) && b[j] != ';' {
		j++
	}
	if j >= len(b) {
		return nil, pos, errParse
	}
	if _, err := strconv.ParseFloat(string(b[pos+2:j]), 64); err != nil {
		return nil, pos, errParse
	}
	return rawScalar{b[pos : j+1]}, j + 1, nil
}

func parseString(b []byte, pos int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	n, j, err := readUint(b, pos+2)
	if err != nil {
		return nil, pos, errParse
	}
	// :"  <n bytes>  ";
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '"' {
		return nil, pos, errParse
	}
	j += 2
	if j+n+1 >= len(b) || b[j+n] != '"' || b[j+n+1] != ';' {
		return nil, pos, errParse
	}
	content := append([]byte(nil), b[j:j+n]...)
	return &strVal{content: content}, j + n + 2, nil
}

func parseArray(b []byte, pos, depth int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	count, j, err := readUint(b, pos+2)
	if err != nil {
		return nil, pos, errParse
	}
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '{' {
		return nil, pos, errParse
	}
	j += 2
	pairs, j, err := parsePairs(b, j, count, depth)
	if err != nil {
		return nil, pos, errParse
	}
	if j >= len(b) || b[j] != '}' {
		return nil, pos, errParse
	}
	return &arrVal{pairs: pairs}, j + 1, nil
}

func parseObject(b []byte, pos, depth int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	clen, j, err := readUint(b, pos+2)
	if err != nil {
		return nil, pos, errParse
	}
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '"' {
		return nil, pos, errParse
	}
	j += 2
	if j+clen+1 >= len(b) || b[j+clen] != '"' || b[j+clen+1] != ':' {
		return nil, pos, errParse
	}
	class := append([]byte(nil), b[j:j+clen]...)
	j += clen + 2
	count, j, err := readUint(b, j)
	if err != nil {
		return nil, pos, errParse
	}
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '{' {
		return nil, pos, errParse
	}
	j += 2
	props, j, err := parsePairs(b, j, count, depth)
	if err != nil {
		return nil, pos, errParse
	}
	if j >= len(b) || b[j] != '}' {
		return nil, pos, errParse
	}
	return &objVal{class: class, props: props}, j + 1, nil
}

// parsePairs reads count key/value pairs. Keys are captured verbatim (they are
// never rewritten); values are parsed into the tree.
func parsePairs(b []byte, j, count, depth int) ([]kv, int, error) {
	if count < 0 {
		return nil, j, errParse
	}
	// Each pair is at least 4 bytes, so a count beyond the remaining input is
	// impossible — reject early to avoid an absurd preallocation.
	if count > len(b)-j {
		return nil, j, errParse
	}
	pairs := make([]kv, 0, count)
	for k := 0; k < count; k++ {
		ks := j
		_, kend, err := parse(b, j, depth+1)
		if err != nil {
			return nil, j, errParse
		}
		val, vend, err := parse(b, kend, depth+1)
		if err != nil {
			return nil, j, errParse
		}
		pairs = append(pairs, kv{keyRaw: append([]byte(nil), b[ks:kend]...), val: val})
		j = vend
	}
	return pairs, j, nil
}

// parseCustom consumes a Serializable payload, C:<len>:"<class>":<datalen>:{...},
// verbatim. Its body is opaque, so we never look inside — any URL there is left
// as-is (a known, rare gap; wp-cli cannot reliably touch these either).
func parseCustom(b []byte, pos int) (node, int, error) {
	if pos+1 >= len(b) || b[pos+1] != ':' {
		return nil, pos, errParse
	}
	clen, j, err := readUint(b, pos+2)
	if err != nil {
		return nil, pos, errParse
	}
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '"' {
		return nil, pos, errParse
	}
	j += 2
	if j+clen+1 >= len(b) || b[j+clen] != '"' || b[j+clen+1] != ':' {
		return nil, pos, errParse
	}
	j += clen + 2
	dlen, j, err := readUint(b, j)
	if err != nil {
		return nil, pos, errParse
	}
	if j+1 >= len(b) || b[j] != ':' || b[j+1] != '{' {
		return nil, pos, errParse
	}
	j += 2
	if j+dlen >= len(b) || b[j+dlen] != '}' {
		return nil, pos, errParse
	}
	j += dlen + 1
	return rawScalar{b[pos:j]}, j, nil
}

// readUint reads a canonical non-negative decimal length/count and returns its
// value and the index of the first non-digit. It rejects a leading zero on a
// multi-digit number: PHP's serialize() never emits one, so "01" is corrupt or
// hostile input, and accepting it would break the reserialize-is-identical
// guarantee (we would canonicalize it to "1"). It also guards against overflow.
func readUint(b []byte, pos int) (int, int, error) {
	start := pos
	n := 0
	for pos < len(b) && b[pos] >= '0' && b[pos] <= '9' {
		d := int(b[pos] - '0')
		if n > (maxInt-d)/10 {
			return 0, pos, errParse
		}
		n = n*10 + d
		pos++
	}
	if pos == start {
		return 0, pos, errParse
	}
	if pos-start > 1 && b[start] == '0' {
		return 0, pos, errParse
	}
	return n, pos, nil
}

// serialize renders a (possibly modified) tree back to PHP-serialized bytes.
// String and container length headers are recomputed from the current contents,
// which is what makes an in-place edit length-correct — and, for an untouched
// tree, byte-identical to the input.
func serialize(n node) []byte {
	var b bytes.Buffer
	writeNode(&b, n)
	return b.Bytes()
}

func writeNode(b *bytes.Buffer, n node) {
	switch v := n.(type) {
	case rawScalar:
		b.Write(v.raw)
	case *strVal:
		writeString(b, v.content)
	case *arrVal:
		b.WriteString("a:")
		b.WriteString(strconv.Itoa(len(v.pairs)))
		b.WriteString(":{")
		for _, p := range v.pairs {
			b.Write(p.keyRaw)
			writeNode(b, p.val)
		}
		b.WriteByte('}')
	case *objVal:
		b.WriteString("O:")
		b.WriteString(strconv.Itoa(len(v.class)))
		b.WriteString(":\"")
		b.Write(v.class)
		b.WriteString("\":")
		b.WriteString(strconv.Itoa(len(v.props)))
		b.WriteString(":{")
		for _, p := range v.props {
			b.Write(p.keyRaw)
			writeNode(b, p.val)
		}
		b.WriteByte('}')
	}
}

func writeString(b *bytes.Buffer, content []byte) {
	b.WriteString("s:")
	b.WriteString(strconv.Itoa(len(content)))
	b.WriteString(":\"")
	b.Write(content)
	b.WriteString("\";")
}

// isSerializedLike reports whether b has the shape of a serialized value, used
// only to decide whether an unparseable value is likely damaged serialized data
// (skip it) or ordinary text (plain-replace it). Deliberately permissive on the
// skip side — see the corrupt-input policy on Replacer.Replace.
func isSerializedLike(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	switch b[0] {
	case 'N':
		return b[1] == ';'
	case 'b', 'i', 'd', 's', 'a', 'O', 'C', 'R', 'r':
		return b[1] == ':'
	}
	return false
}
