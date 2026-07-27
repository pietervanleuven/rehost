package check

import (
	"strconv"
	"strings"
)

// versionAtLeast reports whether dotted version v is >= min. Non-numeric
// suffixes within a segment ("8.3.11-1ubuntu2") are ignored; a missing
// segment counts as 0, so "8.1" >= "8.1.0".
func versionAtLeast(v, min string) bool {
	vs, ms := splitVersion(v), splitVersion(min)
	for i := 0; i < len(vs) || i < len(ms); i++ {
		a, b := segment(vs, i), segment(ms, i)
		if a != b {
			return a > b
		}
	}
	return true
}

// majorOf returns the leading numeric segment of a version ("8.3.11" → "8").
func majorOf(v string) string {
	segs := splitVersion(v)
	if len(segs) == 0 {
		return ""
	}
	return strconv.Itoa(segs[0])
}

func splitVersion(v string) []int {
	var segs []int
	for _, part := range strings.Split(v, ".") {
		digits := part
		for i, r := range part {
			if r < '0' || r > '9' {
				digits = part[:i]
				break
			}
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			break // stop at the first non-numeric segment
		}
		segs = append(segs, n)
	}
	return segs
}

func segment(segs []int, i int) int {
	if i < len(segs) {
		return segs[i]
	}
	return 0
}
