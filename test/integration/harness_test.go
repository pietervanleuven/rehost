//go:build integration

// Package integration runs rehost's host-facing plumbing against a real
// sshd and a real database server in a container.
//
// It exists because the unit tests mock the transport: every shell pipeline,
// credential-staging trick and stream verifier in go-hostdb is exercised here
// against software that can actually reject it. It is deliberately not a CMS
// harness — see README.md for why a default WordPress install would prove
// less than the fixtures in this directory do.
//
// Run with: make integration
package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"

	hostdb "github.com/pietervanleuven/go-hostdb"
	sshpkg "github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/go-ssh/remote"
)

const (
	imageName = "rehost-integration-rig"
	// dbName and the credentials mirror entrypoint.sh. The awkward password
	// is the point: it exercises my.cnf quoting rather than the happy path.
	dbName    = "sitedb"
	dbUser    = "siteuser"
	dbPass    = `p@ss w'rd#1`
	plainUser = "plainuser"
	plainPass = "plainpass"
	sshUser   = "site"

	// oldURL and newURL differ in length on purpose: a serialized string's
	// byte-length prefix has to be recomputed, and equal-length hosts would
	// hide a failure to do so.
	oldURL = "https://old.example.com"
	newURL = "https://new.hosting.example"
)

// buildOnce builds the image at most once per `go test` invocation.
var buildOnce sync.Once

func buildImage(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "build", "-t", imageName, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker build failed: %v\n%s", err, out)
		}
	})
}

// rig is one running environment plus an SSH connection to it.
type rig struct {
	t         *testing.T
	container string
	client    *sshpkg.Client
	env       string
}

// keypair writes a throwaway ed25519 key to dir and returns its path and the
// authorized_keys line for it.
func keypair(t *testing.T, dir string) (keyPath, pubLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	block, err := cryptossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	keyPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	sshPub, err := cryptossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return keyPath, strings.TrimSpace(string(cryptossh.MarshalAuthorizedKey(sshPub)))
}

// autoPrompter accepts the TOFU host-key confirmation. Each rig runs against
// a fresh HOME, so known_hosts starts empty and nothing is ever trusted
// across runs.
type autoPrompter struct{}

func (autoPrompter) Password(string) (string, error) { return "", fmt.Errorf("no password auth in the rig") }
func (autoPrompter) ConfirmHostKey(string, string, string) (bool, error) {
	return true, nil
}

// startRig builds the image if needed, starts the requested environment,
// waits for it to finish seeding, and dials in. Everything is torn down when
// the test ends.
func startRig(t *testing.T, env string) *rig {
	t.Helper()
	if testing.Short() {
		t.Skip("integration rig skipped in -short mode")
	}
	buildImage(t)

	// A private HOME keeps the rig's host keys out of the developer's real
	// known_hosts and keeps ~/.ssh/config from redirecting the connection.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath, pubLine := keypair(t, home)

	name := fmt.Sprintf("rehost-rig-%s-%d", env, time.Now().UnixNano())
	// No --rm: a container that dies during startup must survive long enough
	// for its logs to be read. Cleanup removes it either way.
	out, err := exec.Command("docker", "run", "-d",
		"--name", name,
		"-e", "RIG_ENV="+env,
		"-e", "PUBKEY="+pubLine,
		"-p", "127.0.0.1::22",
		imageName).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	r := &rig{t: t, container: name, env: env}
	t.Cleanup(func() {
		if r.client != nil {
			_ = r.client.Close()
		}
		if t.Failed() {
			if logs, err := exec.Command("docker", "logs", name).CombinedOutput(); err == nil {
				t.Logf("container logs:\n%s", logs)
			}
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	r.waitReady()
	r.client = r.dial(keyPath, r.mappedPort())
	return r
}

// waitReady blocks until the entrypoint has finished seeding the database.
func (r *rig) waitReady() {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if err := exec.Command("docker", "exec", r.container, "test", "-f", "/tmp/rig-ready").Run(); err == nil {
			return
		}
		state, err := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", r.container).Output()
		if err != nil || strings.TrimSpace(string(state)) == "exited" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := exec.Command("docker", "logs", r.container).CombinedOutput()
	r.t.Fatalf("rig %s never became ready:\n%s", r.env, logs)
}

// mappedPort reports the host port docker bound the container's sshd to.
func (r *rig) mappedPort() int {
	r.t.Helper()
	out, err := exec.Command("docker", "port", r.container, "22/tcp").Output()
	if err != nil {
		r.t.Fatalf("docker port: %v", err)
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	i := strings.LastIndex(line, ":")
	if i < 0 {
		r.t.Fatalf("unexpected docker port output %q", line)
	}
	var port int
	if _, err := fmt.Sscanf(line[i+1:], "%d", &port); err != nil {
		r.t.Fatalf("parsing port from %q: %v", line, err)
	}
	return port
}

// dial connects with retries: sshd accepts connections a moment after the
// readiness marker appears.
func (r *rig) dial(keyPath string, port int) *sshpkg.Client {
	r.t.Helper()
	cfg := sshpkg.Config{
		Host:    "127.0.0.1",
		Port:    port,
		User:    sshUser,
		Auth:    sshpkg.AuthKey,
		KeyPath: keyPath,
		Timeout: 10 * time.Second,
	}
	var lastErr error
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		client, err := sshpkg.Dial(ctx, cfg, autoPrompter{})
		cancel()
		if err == nil {
			return client
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	r.t.Fatalf("dialing the rig: %v", lastErr)
	return nil
}

// probe runs rehost's real capability probe against the rig.
func (r *rig) probe() *remote.Capabilities {
	r.t.Helper()
	caps, err := remote.Probe(context.Background(), r.client, "rig-"+r.env)
	if err != nil {
		r.t.Fatalf("probe: %v", err)
	}
	return caps
}

// creds builds credentials for the seeded database. The awkward password is
// the default; pass plain=true for the baseline account.
func (r *rig) creds(name string, plain bool) *hostdb.Credentials {
	c := &hostdb.Credentials{
		Name: name,
		User: dbUser,
		// localhost makes the client use the unix socket, which is what a
		// shared host actually does.
		Host:   "localhost",
		Method: "config-parse",
	}
	if plain {
		c.User, c.Password = plainUser, plainPass
	} else {
		c.Password = dbPass
	}
	if r.env == "pgsql" {
		c.Driver = hostdb.DriverPostgres
		c.Host = "127.0.0.1"
		c.Port = 5432
		c.User, c.Password = dbUser, dbPass
	}
	return c
}

// exec runs a command inside the container as root, for assertions that need
// to look at the database out of band.
func (r *rig) exec(args ...string) string {
	r.t.Helper()
	full := append([]string{"exec", r.container}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("docker exec %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// query runs one SQL statement against a database in the rig and returns the
// raw client output.
func (r *rig) query(database, sql string) string {
	r.t.Helper()
	if r.env == "pgsql" {
		// Over the unix socket, which the cluster trusts. TCP deliberately
		// demands a password so that rehost's pgpass staging is on trial;
		// this out-of-band measurement must not depend on it.
		return r.exec("su", "postgres", "-c",
			fmt.Sprintf("psql -d %s -tAc %s", database, shellSingleQuote(sql)))
	}
	client := "mariadb"
	if r.env == "mysql" || r.env == "nodump" {
		client = "mysql"
	}
	return r.exec("sh", "-c",
		fmt.Sprintf("%s -u root -N -B %s -e %s", client, database, shellSingleQuote(sql)))
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gunzipAll decompresses a gzipped dump held in memory.
func gunzipAll(t *testing.T, gz []byte) string {
	t.Helper()
	cmd := exec.Command("gzip", "-dc")
	cmd.Stdin = bytes.NewReader(gz)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}
