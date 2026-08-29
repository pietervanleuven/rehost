package recipe

import (
	"bytes"
	"context"
	"fmt"
	"regexp"

	"github.com/pietervanleuven/rehost/internal/db"
)

// RewriteConfig points the synced wp-config.php at the destination database:
// DB_NAME, DB_USER, DB_PASSWORD and DB_HOST are replaced in place, everything
// else — salts, keys, table prefix, custom code — stays byte-exact.
func (w WordPress) RewriteConfig(ctx context.Context, h Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the docroot and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, err := rewriteWPConfig(content, rw.DB)
	if err != nil {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s: %v — edit it by hand", confPath, err)}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, confPath, rewritten); err != nil {
		return ConfigRewriteResult{}, err
	}
	return ConfigRewriteResult{Path: confPath, Supported: true}, nil
}

// rewriteWPConfig is the pure transform: each DB define gets the destination
// value as a single-quoted literal, preserving the file byte-exact otherwise.
func rewriteWPConfig(content []byte, creds db.Credentials) ([]byte, error) {
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	if creds.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, creds.Port)
	}
	for _, kv := range []struct{ key, value string }{
		{"DB_NAME", creds.Name},
		{"DB_USER", creds.User},
		{"DB_PASSWORD", creds.Password},
		{"DB_HOST", host},
	} {
		var ok bool
		content, ok = replacePHPDefine(content, kv.key, kv.value)
		if !ok {
			return nil, fmt.Errorf("no define('%s', ...) found", kv.key)
		}
	}
	return content, nil
}

// replacePHPDefine splices a new single-quoted value into the first
// non-commented define('KEY', <literal>) occurrence, touching nothing else.
// Matching runs against a comment-masked copy — a commented-out define above
// the live one (common after hand edits) must not win — and offsets in the
// mask line up with the original byte-for-byte.
func replacePHPDefine(content []byte, key, value string) ([]byte, bool) {
	re := regexp.MustCompile(`define\(\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*,\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")`)
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
