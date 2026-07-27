package tui

import "testing"

func TestValidateHost(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{in: "web.example.com"},
		{in: "myalias"}, // ~/.ssh/config alias
		{in: "192.0.2.10"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "user@host", wantErr: true},
		{in: "two words", wantErr: true},
	}
	for _, c := range cases {
		err := validateHost(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateHost(%q) = %v, wantErr %v", c.in, err, c.wantErr)
		}
	}
}

func TestValidatePort(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{in: ""},
		{in: "22"},
		{in: "65535"},
		{in: "0", wantErr: true},
		{in: "65536", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "-1", wantErr: true},
	}
	for _, c := range cases {
		err := validatePort(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validatePort(%q) = %v, wantErr %v", c.in, err, c.wantErr)
		}
	}
}
