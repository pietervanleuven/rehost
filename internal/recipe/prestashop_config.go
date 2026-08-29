package recipe

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"github.com/pietervanleuven/rehost/internal/db"
)

// RewriteConfig points the synced PrestaShop config at the destination
// database, in whichever shape the shop uses: the parameters array
// (app/config/parameters.php, 1.7/8) or the legacy _DB_*_ defines
// (config/settings.inc.php, 1.6). Cookie keys, salts and everything else
// stay byte-exact.
func (p PrestaShop) RewriteConfig(ctx context.Context, h db.Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the docroot and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewritePrestaShopConfig(content, rw.DB)
	if err != nil {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s: %v — edit it by hand", confPath, err)}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, confPath, rewritten); err != nil {
		return ConfigRewriteResult{}, err
	}
	res := ConfigRewriteResult{Path: confPath, Supported: true}
	if len(missing) > 0 {
		res.PostSteps = append(res.PostSteps, fmt.Sprintf(
			"set the destination database %s in %s by hand — it is not a literal the rewrite could replace, so the shop still uses the source's",
			joinAnd(missing), confPath))
	}
	res.PostSteps = append(res.PostSteps,
		"clear the PrestaShop cache after go-live: empty var/cache/ (1.7/8) or delete cache/class_index.php (1.6)")
	return res, nil
}

// rewritePrestaShopConfig is the pure transform for both config shapes.
func rewritePrestaShopConfig(content []byte, creds db.Credentials) ([]byte, []string, error) {
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	if bytes.Contains(content, []byte("database_name")) {
		// parameters.php is nothing but parameters, so the whole file is the
		// splice region for the generic 'key' => <literal> replacer.
		var ok bool
		if content, ok = replaceDrupalValue(content, "database_name", creds.Name); !ok {
			return nil, nil, fmt.Errorf("no 'database_name' parameter found")
		}
		var missing []string
		if content, ok = replaceDrupalValue(content, "database_user", creds.User); !ok && creds.User != "" {
			missing = append(missing, "database_user")
		}
		if content, ok = replaceDrupalValue(content, "database_password", creds.Password); !ok && creds.Password != "" {
			missing = append(missing, "database_password")
		}
		content, _ = replaceDrupalValue(content, "database_host", host)
		if creds.Port != 0 {
			content, _ = replaceDrupalValue(content, "database_port", strconv.Itoa(creds.Port))
		}
		return content, missing, nil
	}

	// Legacy defines: PrestaShop 1.6 carries a port inside _DB_SERVER_.
	if creds.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, creds.Port)
	}
	var ok bool
	if content, ok = replacePHPDefine(content, "_DB_NAME_", creds.Name); !ok {
		return nil, nil, fmt.Errorf("no define('_DB_NAME_', …) found")
	}
	var missing []string
	if content, ok = replacePHPDefine(content, "_DB_USER_", creds.User); !ok && creds.User != "" {
		missing = append(missing, "_DB_USER_")
	}
	if content, ok = replacePHPDefine(content, "_DB_PASSWD_", creds.Password); !ok && creds.Password != "" {
		missing = append(missing, "_DB_PASSWD_")
	}
	content, _ = replacePHPDefine(content, "_DB_SERVER_", host)
	return content, missing, nil
}
