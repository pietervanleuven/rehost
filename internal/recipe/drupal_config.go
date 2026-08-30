package recipe

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/go-ssh/remote"
)

// RewriteConfig points the synced settings.php at the destination database:
// the $databases entry's database/username/password/host (and port when
// configured) are replaced in place. hash_salt, trusted_host_patterns and
// everything else stay byte-exact — the salt must survive the migration or
// every session and one-time login link breaks. When drush is available on
// the destination a cache rebuild runs afterwards, as Drupal requires.
func (d Drupal) RewriteConfig(ctx context.Context, h Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the docroot and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewriteDrupalSettings(content, rw.DB)
	if err != nil {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s: %v — edit it by hand", confPath, err)}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, confPath, rewritten); err != nil {
		return ConfigRewriteResult{}, err
	}

	res := ConfigRewriteResult{Path: confPath, Supported: true}
	if len(missing) > 0 {
		// The $databases entry does not expose these as literals (e.g.
		// 'username' => getenv(...), or values pulled from an included file),
		// so the rewrite could not point them at the destination — the synced
		// settings.php still resolves them the source's way. Flag it as a
		// manual step rather than reporting a clean success.
		res.PostSteps = append(res.PostSteps, fmt.Sprintf(
			"set the destination database %s in %s by hand — it is not a literal the rewrite could replace, so the site still uses the source's",
			strings.Join(missing, " and "), confPath))
	}
	if h.HasTool("drush") {
		cr, err := h.Run.Run(ctx, "cd "+remote.ShellQuote(rw.DestRoot)+" && drush cr 2>&1")
		switch {
		case err != nil:
			return res, err
		case cr.ExitCode == 0:
			res.PostSteps = append(res.PostSteps, "drush cr")
		default:
			res.PostSteps = append(res.PostSteps, "drush cr FAILED — run it by hand: "+remote.FirstLine(cr.Stdout))
		}
	} else {
		res.PostSteps = append(res.PostSteps, "run 'drush cr' (or clear caches via the UI) — no drush on the destination")
	}
	return res, nil
}

// rewriteDrupalSettings is the pure transform: the first non-commented
// database/username/password/host entry each gets the destination value. It
// returns the auth-critical keys (username, password) it had a value for but
// could not replace — the config exposes them some way the regex cannot match,
// so the destination would still authenticate the source's way. port is
// rewritten only when the config declares one; database is the only key whose
// absence is an error (no $databases entry at all).
func rewriteDrupalSettings(content []byte, creds hostdb.Credentials) ([]byte, []string, error) {
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	// All splicing is confined to the default connection's own array literal:
	// a settings.php that configures memcache/redis servers or a 'migrate'
	// connection before $databases must never have their 'host'/'password'
	// entries rewritten in its place.
	start, end, found := drupalDefaultConnRange(maskPHPComments(content))
	if !found {
		return nil, nil, fmt.Errorf("no $databases assignment found")
	}
	region := append([]byte(nil), content[start:end]...)
	var ok bool
	if region, ok = replaceDrupalValue(region, "database", creds.Name); !ok {
		return nil, nil, fmt.Errorf("no $databases 'database' entry found")
	}
	// Only a value we actually have but could not place is worth flagging: an
	// empty source username/password has nothing to write, and the config may
	// legitimately omit the key.
	var missing []string
	if region, ok = replaceDrupalValue(region, "username", creds.User); !ok && creds.User != "" {
		missing = append(missing, "username")
	}
	if region, ok = replaceDrupalValue(region, "password", creds.Password); !ok && creds.Password != "" {
		missing = append(missing, "password")
	}
	// host defaults to localhost when the entry omits it, which is the common
	// correct case on shared hosts, so a missing host literal is not flagged.
	region, _ = replaceDrupalValue(region, "host", host)
	if creds.Port != 0 {
		region, _ = replaceDrupalValue(region, "port", strconv.Itoa(creds.Port))
	}
	out := make([]byte, 0, len(content)-(end-start)+len(region))
	out = append(out, content[:start]...)
	out = append(out, region...)
	out = append(out, content[end:]...)
	return out, missing, nil
}

// replaceDrupalValue splices a new single-quoted value into the first
// non-commented `'key' => <literal>` occurrence. The caller passes the
// default connection's array literal (drupalDefaultConnRange), never the
// whole file. Matching runs against a comment-masked copy so a commented-out
// entry is never edited in place of the real one; offsets in the mask line
// up with the original byte-for-byte.
func replaceDrupalValue(content []byte, key, value string) ([]byte, bool) {
	re := regexp.MustCompile(`['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|\d+)`)
	loc := re.FindSubmatchIndex(maskPHPComments(content))
	if loc == nil {
		return content, false
	}
	var b bytes.Buffer
	b.Write(content[:loc[2]])
	b.WriteString(phpSingleQuote(value))
	b.Write(content[loc[3]:])
	return b.Bytes(), true
}
