package check

import "testing"

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		want   bool
	}{
		{"8.3.11", "7.2", true},
		{"8.1", "8.1.0", true},
		{"8.1.0", "8.1", true},
		{"7.4.33", "8.1", false},
		{"8.3.11-1ubuntu2", "8.3", true},
		{"10.2", "9.9.9", true},
		{"5.6", "5.6", true},
		{"5.5.9", "5.6", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
}

func TestMajorOf(t *testing.T) {
	if got := majorOf("8.3.11"); got != "8" {
		t.Errorf("majorOf(8.3.11) = %q", got)
	}
	if got := majorOf(""); got != "" {
		t.Errorf("majorOf(\"\") = %q, want empty", got)
	}
}
