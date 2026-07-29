package recipe

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// RewriteConfig points the synced settings.php at the destination database:
// the $databases entry's database/username/password/host (and port when
// configured) are replaced in place. hash_salt, trusted_host_patterns and
// everything else stay byte-exact — the salt must survive the migration or
// every session and one-time login link breaks. When drush is available on
// the destination a cache rebuild runs afterwards, as Drupal requires.
func (d Drupal) RewriteConfig(ctx context.Context, h db.Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the docroot and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, err := rewriteDrupalSettings(content, rw.DB)
	if err != nil {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s: %v — edit it by hand", confPath, err)}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, confPath, rewritten); err != nil {
		return ConfigRewriteResult{}, err
	}

	res := ConfigRewriteResult{Path: confPath, Supported: true}
	if h.HasTool("drush") {
		cr, err := h.Run.Run(ctx, "cd "+ssh.ShellQuote(rw.DestRoot)+" && drush cr 2>&1")
		switch {
		case err != nil:
			return res, err
		case cr.ExitCode == 0:
			res.PostSteps = append(res.PostSteps, "drush cr")
		default:
			res.PostSteps = append(res.PostSteps, "drush cr FAILED — run it by hand: "+ssh.FirstLine(cr.Stdout))
		}
	} else {
		res.PostSteps = append(res.PostSteps, "run 'drush cr' (or clear caches via the UI) — no drush on the destination")
	}
	return res, nil
}

// rewriteDrupalSettings is the pure transform: the first
// database/username/password/host entry each gets the destination value.
// port is rewritten only when the config declares one; database is the only
// key whose absence is an error (no $databases entry at all).
func rewriteDrupalSettings(content []byte, creds db.Credentials) ([]byte, error) {
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	var ok bool
	if content, ok = replaceDrupalValue(content, "database", creds.Name); !ok {
		return nil, fmt.Errorf("no $databases 'database' entry found")
	}
	content, _ = replaceDrupalValue(content, "username", creds.User)
	content, _ = replaceDrupalValue(content, "password", creds.Password)
	content, _ = replaceDrupalValue(content, "host", host)
	if creds.Port != 0 {
		content, _ = replaceDrupalValue(content, "port", strconv.Itoa(creds.Port))
	}
	return content, nil
}

// replaceDrupalValue splices a new single-quoted value into the first
// `'key' => <literal>` occurrence (the shape of a $databases entry).
func replaceDrupalValue(content []byte, key, value string) ([]byte, bool) {
	re := regexp.MustCompile(`['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|\d+)`)
	loc := re.FindSubmatchIndex(content)
	if loc == nil {
		return content, false
	}
	var b bytes.Buffer
	b.Write(content[:loc[2]])
	b.WriteString(phpSingleQuote(value))
	b.Write(content[loc[3]:])
	return b.Bytes(), true
}
