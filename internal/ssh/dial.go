package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/skeema/knownhosts"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Prompter supplies the interactive pieces of dialing. A non-TTY run passes
// an implementation that fails with remediation guidance instead of hanging.
type Prompter interface {
	// Password asks for a password or key passphrase.
	Password(prompt string) (string, error)
	// ConfirmHostKey asks whether to trust an unknown host key (TOFU).
	ConfirmHostKey(host, keyType, fingerprint string) (bool, error)
}

// Client is an established SSH connection.
type Client struct {
	conn *cryptossh.Client
	// Config is the resolved configuration the connection was made with.
	Config Config
}

// Dial resolves cfg against ~/.ssh/config and connects. Host keys are
// verified against ~/.ssh/known_hosts: unknown hosts go through a TOFU
// confirmation, changed keys always fail hard.
func Dial(ctx context.Context, cfg Config, p Prompter) (*Client, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	auths, err := authMethods(cfg, p)
	if err != nil {
		return nil, err
	}
	hostKeyCB, err := hostKeyCallback(p)
	if err != nil {
		return nil, err
	}

	clientCfg := &cryptossh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCB,
		Timeout:         cfg.Timeout,
	}

	dialer := net.Dialer{Timeout: cfg.Timeout}
	netConn, err := dialer.DialContext(ctx, "tcp", cfg.Addr())
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", cfg.Addr(), err)
	}
	conn, chans, reqs, err := cryptossh.NewClientConn(netConn, cfg.Addr(), clientCfg)
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", cfg.Addr(), err)
	}
	return &Client{conn: cryptossh.NewClient(conn, chans, reqs), Config: cfg}, nil
}

// Close terminates the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// RemoteIP is the IP address the connection actually reached — the host's
// real identity even when Config.Host is an alias or CNAME.
func (c *Client) RemoteIP() string {
	addr := c.conn.RemoteAddr()
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}

// authMethods builds the auth chain. For AuthAuto the order is agent,
// identity files, then password + keyboard-interactive prompts; an explicit
// Auth narrows the chain to that method.
func authMethods(cfg Config, p Prompter) ([]cryptossh.AuthMethod, error) {
	var methods []cryptossh.AuthMethod
	useAgent := cfg.Auth == AuthAuto || cfg.Auth == AuthAgent
	useKey := cfg.Auth == AuthAuto || cfg.Auth == AuthKey
	usePassword := cfg.Auth == AuthAuto || cfg.Auth == AuthPassword

	if useAgent {
		sock := os.Getenv("SSH_AUTH_SOCK")
		switch {
		case sock != "":
			conn, err := net.Dial("unix", sock)
			if err != nil {
				if cfg.Auth == AuthAgent {
					return nil, fmt.Errorf("connecting to ssh-agent: %w", err)
				}
			} else {
				methods = append(methods, cryptossh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		case cfg.Auth == AuthAgent:
			return nil, errors.New(`auth is "agent" but SSH_AUTH_SOCK is not set — is an ssh-agent running?`)
		}
	}

	if useKey {
		paths := keyCandidates(cfg)
		if len(paths) > 0 {
			// Lazy so passphrase prompts only fire if earlier methods failed.
			methods = append(methods, cryptossh.PublicKeysCallback(func() ([]cryptossh.Signer, error) {
				return loadSigners(paths, p)
			}))
		} else if cfg.Auth == AuthKey {
			return nil, fmt.Errorf(`auth is "key" but no key file was found (looked for %s and default ~/.ssh keys)`, cfg.KeyPath)
		}
	}

	if usePassword {
		prompt := fmt.Sprintf("Password for %s@%s", cfg.User, cfg.Host)
		password := cryptossh.PasswordCallback(func() (string, error) {
			return p.Password(prompt)
		})
		methods = append(methods, cryptossh.RetryableAuthMethod(password, 3))
		// Many shared hosts only offer keyboard-interactive for passwords.
		keyboard := cryptossh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i, q := range questions {
				a, err := p.Password(fmt.Sprintf("%s@%s: %s", cfg.User, cfg.Host, q))
				if err != nil {
					return nil, err
				}
				answers[i] = a
			}
			return answers, nil
		})
		methods = append(methods, cryptossh.RetryableAuthMethod(keyboard, 3))
	}

	if len(methods) == 0 {
		return nil, errors.New("no usable auth method: no ssh-agent and no key files found")
	}
	return methods, nil
}

// keyCandidates returns existing identity files to try: the configured path
// first, else the standard defaults.
func keyCandidates(cfg Config) []string {
	candidates := []string{}
	if cfg.KeyPath != "" {
		candidates = append(candidates, cfg.KeyPath)
	} else {
		sshDir := filepath.Join(homeDir(), ".ssh")
		candidates = append(candidates, filepath.Join(sshDir, "id_ed25519"), filepath.Join(sshDir, "id_rsa"))
	}
	var found []string
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}

// loadSigners parses identity files, prompting for passphrases when needed.
func loadSigners(paths []string, p Prompter) ([]cryptossh.Signer, error) {
	var signers []cryptossh.Signer
	var errs []error
	for _, path := range paths {
		pem, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		signer, err := cryptossh.ParsePrivateKey(pem)
		var missing *cryptossh.PassphraseMissingError
		if errors.As(err, &missing) {
			phrase, perr := p.Password(fmt.Sprintf("Passphrase for %s", path))
			if perr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, perr))
				continue
			}
			signer, err = cryptossh.ParsePrivateKeyWithPassphrase(pem, []byte(phrase))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		return nil, fmt.Errorf("no usable identity files: %w", errors.Join(errs...))
	}
	return signers, nil
}

// hostKeyCallback verifies against ~/.ssh/known_hosts (and known_hosts2 when
// present). Unknown host: TOFU confirmation, appended on accept. Changed
// key: hard failure, deliberately not overridable.
func hostKeyCallback(p Prompter) (cryptossh.HostKeyCallback, error) {
	sshDir := filepath.Join(homeDir(), ".ssh")
	khPath := filepath.Join(sshDir, "known_hosts")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(khPath); os.IsNotExist(err) {
		if err := os.WriteFile(khPath, nil, 0o600); err != nil {
			return nil, err
		}
	}
	files := []string{khPath}
	if _, err := os.Stat(khPath + "2"); err == nil {
		files = append(files, khPath+"2")
	}
	// skeema/knownhosts rejects the whole file on the first malformed line;
	// OpenSSH just skips bad lines. Stage a sanitized copy so accumulated
	// cruft in known_hosts can't block an otherwise valid connection.
	staged, cleanup, err := stageKnownHosts(files)
	if err != nil {
		return nil, fmt.Errorf("reading known_hosts: %w", err)
	}
	defer cleanup()
	db, err := knownhosts.NewDB(staged)
	if err != nil {
		return nil, fmt.Errorf("parsing known_hosts: %w", err)
	}
	verify := db.HostKeyCallback()

	return func(hostname string, remote net.Addr, key cryptossh.PublicKey) error {
		err := verify(hostname, remote, key)
		switch {
		case err == nil:
			return nil
		case knownhosts.IsHostKeyChanged(err):
			return fmt.Errorf("host key for %s has CHANGED (now %s %s) — possible man-in-the-middle attack; if the host was legitimately reinstalled, remove its line from ~/.ssh/known_hosts and retry", hostname, key.Type(), cryptossh.FingerprintSHA256(key))
		case knownhosts.IsHostUnknown(err):
			ok, perr := p.ConfirmHostKey(hostname, key.Type(), cryptossh.FingerprintSHA256(key))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("host key for %s not accepted", hostname)
			}
			return appendKnownHost(khPath, hostname, remote, key)
		default:
			return err
		}
	}, nil
}

func appendKnownHost(path, hostname string, remote net.Addr, key cryptossh.PublicKey) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("recording host key: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("recording host key: %w", cerr)
		}
	}()
	return knownhosts.WriteKnownHost(f, hostname, remote, key)
}

// stageKnownHosts writes the parseable entries of the given known_hosts files
// to a temporary file and returns its path, dropping blank lines, comments,
// and any line the parser rejects. This mirrors OpenSSH's tolerance of
// malformed lines; the temp file is removed once the DB has read it. New keys
// accepted via trust-on-first-use are still appended to the real known_hosts.
func stageKnownHosts(files []string) (path string, cleanup func(), err error) {
	var clean bytes.Buffer
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			// ParseKnownHosts validates one entry; a comment or malformed
			// line returns an error and is skipped.
			if _, _, _, _, _, perr := cryptossh.ParseKnownHosts(line); perr != nil {
				continue
			}
			clean.Write(line)
			clean.WriteByte('\n')
		}
	}

	tmp, err := os.CreateTemp("", "rehost-known-hosts-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.Write(clean.Bytes()); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}
