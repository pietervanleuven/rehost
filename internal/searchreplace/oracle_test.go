package searchreplace

import (
	"fmt"
	"strconv"
)

// This file is the test oracle: an independent PHP serializer that mirrors PHP's
// serialize() byte-for-byte, used to build inputs the production parser must
// accept and to assert round-trip identity against a source of truth that does
// not share code with serial.go.

type phpNull struct{}

type phpKV struct {
	Key any // int or string
	Val any
}

type phpArr []phpKV

type phpObj struct {
	Class string
	Props []phpKV
}

func phpSerialize(v any) []byte {
	switch t := v.(type) {
	case phpNull:
		return []byte("N;")
	case nil:
		return []byte("N;")
	case bool:
		if t {
			return []byte("b:1;")
		}
		return []byte("b:0;")
	case int:
		return []byte("i:" + strconv.Itoa(t) + ";")
	case float64:
		return []byte("d:" + strconv.FormatFloat(t, 'g', -1, 64) + ";")
	case string:
		return []byte(phpStr(t))
	case phpArr:
		out := "a:" + strconv.Itoa(len(t)) + ":{"
		for _, kv := range t {
			out += string(phpSerialize(kv.Key)) + string(phpSerialize(kv.Val))
		}
		return []byte(out + "}")
	case phpObj:
		out := "O:" + strconv.Itoa(len(t.Class)) + ":\"" + t.Class + "\":" + strconv.Itoa(len(t.Props)) + ":{"
		for _, kv := range t.Props {
			out += string(phpSerialize(kv.Key)) + string(phpSerialize(kv.Val))
		}
		return []byte(out + "}")
	default:
		panic(fmt.Sprintf("phpSerialize: unsupported %T", v))
	}
}

// phpStr serializes a string with a byte-length header, matching PHP (which does
// not escape any inner byte — the content is length-delimited).
func phpStr(s string) string {
	return "s:" + strconv.Itoa(len(s)) + ":\"" + s + "\";"
}
