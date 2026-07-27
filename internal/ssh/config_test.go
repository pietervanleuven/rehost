package ssh

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
)

const configFixture = `
Host web
  HostName web.internal.example.com
  Port 2222
  User deploy
  IdentityFile ~/.ssh/web_ed25519

Host jumpy
  HostName inner.example.com
  ProxyJump bastion.example.com
`

func decodeFixture(t *testing.T) *sshconfig.Config {
	t.Helper()
	cfg, err := sshconfig.Decode(strings.NewReader(configFixture))
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return cfg
}

func TestResolveAppliesSSHConfig(t *testing.T) {
	got, err := Config{Host: "web"}.resolveWith(decodeFixture(t))
	if err != nil {
		t.Fatalf("resolveWith: %v", err)
	}
	if got.hostname != "web.internal.example.com" {
		t.Errorf("hostname = %q, want web.internal.example.com", got.hostname)
	}
	if got.Port != 2222 {
		t.Errorf("Port = %d, want 2222", got.Port)
	}
	if got.User != "deploy" {
		t.Errorf("User = %q, want deploy", got.User)
	}
	if want := filepath.Join(homeDir(), ".ssh", "web_ed25519"); got.KeyPath != want {
		t.Errorf("KeyPath = %q, want %q (tilde expanded)", got.KeyPath, want)
	}
	if got.Addr() != "web.internal.example.com:2222" {
		t.Errorf("Addr() = %q", got.Addr())
	}
	if got.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want default %v", got.Timeout, DefaultTimeout)
	}
}

func TestResolveExplicitValuesWin(t *testing.T) {
	in := Config{Host: "web", Port: 26, User: "root", KeyPath: "~/.ssh/other", Timeout: time.Second}
	got, err := in.resolveWith(decodeFixture(t))
	if err != nil {
		t.Fatalf("resolveWith: %v", err)
	}
	if got.Port != 26 || got.User != "root" || got.Timeout != time.Second {
		t.Errorf("explicit values overridden: %+v", got)
	}
	if want := filepath.Join(homeDir(), ".ssh", "other"); got.KeyPath != want {
		t.Errorf("KeyPath = %q, want %q", got.KeyPath, want)
	}
	// HostName still applies: alias resolution is not overridable.
	if got.Addr() != "web.internal.example.com:26" {
		t.Errorf("Addr() = %q", got.Addr())
	}
}

func TestResolveDefaultsForUnknownHost(t *testing.T) {
	got, err := Config{Host: "plain.example.com", User: "u"}.resolveWith(decodeFixture(t))
	if err != nil {
		t.Fatalf("resolveWith: %v", err)
	}
	if got.Addr() != "plain.example.com:22" {
		t.Errorf("Addr() = %q, want plain.example.com:22", got.Addr())
	}
}

func TestResolveNoSSHConfigFile(t *testing.T) {
	got, err := Config{Host: "plain.example.com", User: "u"}.resolveWith(nil)
	if err != nil {
		t.Fatalf("resolveWith(nil): %v", err)
	}
	if got.Addr() != "plain.example.com:22" {
		t.Errorf("Addr() = %q", got.Addr())
	}
}

func TestResolveRejectsProxyJump(t *testing.T) {
	_, err := Config{Host: "jumpy"}.resolveWith(decodeFixture(t))
	if err == nil || !strings.Contains(err.Error(), "ProxyJump") {
		t.Errorf("want ProxyJump rejection, got: %v", err)
	}
}
