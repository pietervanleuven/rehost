// Package ssh provides the connection layer to remote hosts: dialing with
// agent/key/password auth, ~/.ssh/config respect, known_hosts verification,
// command execution, and the remote capability probe.
package ssh

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
)

// AuthMethod selects how to authenticate. Empty means auto: agent, then
// default keys, then password prompt.
type AuthMethod string

const (
	AuthAuto     AuthMethod = ""
	AuthAgent    AuthMethod = "agent"
	AuthKey      AuthMethod = "key"
	AuthPassword AuthMethod = "password"
)

// DefaultTimeout bounds the TCP dial + handshake.
const DefaultTimeout = 15 * time.Second

// Config describes how to reach one host. Host may be a hostname or a
// ~/.ssh/config alias; zero values are filled by Resolve.
type Config struct {
	Host    string
	Port    int
	User    string
	Auth    AuthMethod
	KeyPath string
	Timeout time.Duration

	// hostname is the connect address after ssh_config HostName resolution;
	// Host keeps the alias for known_hosts prompts and display.
	hostname string
}

// Resolve applies ~/.ssh/config (HostName, Port, User, IdentityFile) and
// defaults. Explicit Config values always win over ssh_config values.
func (c Config) Resolve() (Config, error) {
	path := filepath.Join(homeDir(), ".ssh", "config")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c.resolveWith(nil)
	}
	if err != nil {
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := sshconfig.Decode(f)
	if err != nil {
		return c, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c.resolveWith(cfg)
}

// resolveWith is the pure core of Resolve, testable with an in-memory config.
func (c Config) resolveWith(cfg *sshconfig.Config) (Config, error) {
	get := func(key string) string {
		if cfg == nil {
			return ""
		}
		v, err := cfg.Get(c.Host, key)
		if err != nil {
			return ""
		}
		return v
	}

	// Jump hosts are post-1.0 (PLAN.md §5.3); fail honestly rather than
	// silently connecting direct.
	if jump := get("ProxyJump"); jump != "" {
		return c, fmt.Errorf("host %q uses ProxyJump %q in ~/.ssh/config, which rehost does not support yet — connect without a jump host for now", c.Host, jump)
	}

	c.hostname = c.Host
	if hn := get("HostName"); hn != "" {
		c.hostname = hn
	}
	if c.Port == 0 {
		if p := get("Port"); p != "" {
			port, err := strconv.Atoi(p)
			if err != nil {
				return c, fmt.Errorf("invalid Port %q in ~/.ssh/config for host %q", p, c.Host)
			}
			c.Port = port
		} else {
			c.Port = 22
		}
	}
	if c.User == "" {
		c.User = get("User")
	}
	if c.User == "" {
		if u, err := user.Current(); err == nil {
			c.User = u.Username
		}
	}
	if c.KeyPath == "" {
		c.KeyPath = expandTilde(get("IdentityFile"))
	} else {
		c.KeyPath = expandTilde(c.KeyPath)
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c, nil
}

// Addr returns the resolved dial address.
func (c Config) Addr() string {
	host := c.hostname
	if host == "" {
		host = c.Host
	}
	return fmt.Sprintf("%s:%d", host, c.Port)
}

func expandTilde(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
