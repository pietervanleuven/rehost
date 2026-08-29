package recipe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
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

// writeRemoteFileRandom replaces a remote config file: a random heredoc
// marker so arbitrary config content can never collide with the delimiter,
// staged through a sibling temp file and renamed so a dropped connection can
// never leave a torn or empty config (cp -p seeds the temp file, so the
// config's mode and ownership survive the rename). Before the first rewrite
// the original is backed up once — best-effort — under ~/.rehost/ rather
// than next to the config: a wp-config.php.bak-style sibling in the docroot
// would be served as plain text, credentials included. A rerun keeps the
// existing backup, which is the pre-rehost original.
func writeRemoteFileRandom(ctx context.Context, r db.Runner, p string, content []byte) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	marker := "REHOST_CONF_" + hex.EncodeToString(b[:])
	// The heredoc terminates the last body line itself, so a trailing
	// newline in content would come back doubled.
	body := strings.TrimSuffix(string(content), "\n")
	q := ssh.ShellQuote(p)
	tmp := ssh.ShellQuote(p + ".rehost-tmp")
	bak := `"$HOME"/.rehost/config-backups/` + backupName(p)
	cmd := `{ mkdir -p "$HOME"/.rehost/config-backups && { test -f ` + bak + ` || cp -p ` + q + ` ` + bak + `; }; } 2>/dev/null; ` +
		"cp -p " + q + " " + tmp + " && cat > " + tmp + " <<'" + marker + "' && mv -f " + tmp + " " + q +
		"\n" + body + "\n" + marker
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("writing %s: %s", p, ssh.FirstLine(res.Stderr))
	}
	return nil
}

// backupName derives a stable, shell-safe backup file name for a config path:
// a short hash keeps distinct paths distinct, the base name keeps it
// recognizable. Stability across runs is what makes the backup one-time.
func backupName(p string) string {
	sum := sha256.Sum256([]byte(p))
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, path.Base(p))
	return hex.EncodeToString(sum[:4]) + "-" + base
}
