package recipe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// ConfigRewrite describes one site's config move: where the config file sat
// on the source, the docroot move, and the destination database the rewritten
// config must point at (Password included — the site's own config is where
// that secret legitimately lives).
type ConfigRewrite struct {
	SourceConfig string // config path on the source, from detection
	SourceRoot   string // source docroot
	DestRoot     string // destination docroot (the config was synced under it)
	DB           db.Credentials
}

// ConfigRewriteResult reports what a rewrite did. Supported false means the
// recipe had no strategy for this config (Note says why) — the caller
// surfaces it as a warning, the migration itself is not failed.
type ConfigRewriteResult struct {
	Path      string   `json:"path,omitempty"` // the rewritten file on the destination
	Supported bool     `json:"supported"`
	Note      string   `json:"note,omitempty"`
	PostSteps []string `json:"post_steps,omitempty"` // e.g. "drush cr" — what ran (or should run) after
}

// ConfigRewriter is the config-rewrite capability a recipe may implement,
// operating on the DESTINATION host after files and database landed there.
// Implementations must preserve everything but the database settings —
// salts, keys and custom code stay byte-exact.
type ConfigRewriter interface {
	RewriteConfig(ctx context.Context, h db.Host, rw ConfigRewrite) (ConfigRewriteResult, error)
}

// RewriterFor returns the config-rewrite strategy of a framework's recipe,
// or nil when the framework has none (static) or is unknown.
func RewriterFor(framework string) ConfigRewriter {
	for _, r := range All() {
		if r.Name() != framework {
			continue
		}
		if w, ok := r.(ConfigRewriter); ok {
			return w
		}
	}
	return nil
}

// destConfigPath maps the source config file to its synced location under the
// destination root. A config outside the source docroot (WordPress allows
// wp-config.php one level up) was never synced, so there is nothing to
// rewrite — the caller reports it as unsupported with guidance.
func destConfigPath(rw ConfigRewrite) (string, bool) {
	srcRoot := strings.TrimSuffix(rw.SourceRoot, "/")
	if rel, ok := strings.CutPrefix(rw.SourceConfig, srcRoot+"/"); ok {
		return path.Join(rw.DestRoot, rel), true
	}
	return "", false
}

// phpSingleQuote renders s as a PHP single-quoted string literal.
func phpSingleQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(s) + "'"
}

// readRemoteFile fetches a remote file's content over Run. A transport error
// propagates; a non-zero exit (missing/unreadable file) is an
// ErrMaintenanceTool-style local failure the caller can degrade on.
func readRemoteFile(ctx context.Context, r db.Runner, p string) ([]byte, error) {
	res, err := r.Run(ctx, "cat -- "+ssh.ShellQuote(p))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("reading %s: %s", p, ssh.FirstLine(res.Stderr))
	}
	return []byte(res.Stdout), nil
}

// writeRemoteFileRandom writes content to path like writeRemoteFile, but with
// a random heredoc marker so arbitrary config content can never collide with
// the delimiter.
func writeRemoteFileRandom(ctx context.Context, r db.Runner, p string, content []byte) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	marker := "REHOST_CONF_" + hex.EncodeToString(b[:])
	// The heredoc terminates the last body line itself, so a trailing
	// newline in content would come back doubled.
	body := strings.TrimSuffix(string(content), "\n")
	cmd := "cat > " + ssh.ShellQuote(p) + " <<'" + marker + "'\n" + body + "\n" + marker
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("writing %s: %s", p, ssh.FirstLine(res.Stderr))
	}
	return nil
}
