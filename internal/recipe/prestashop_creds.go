package recipe

import (
	"bytes"
	"context"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials reads the PrestaShop database credentials in layers: a
// PHP echo-helper that evaluates the config the way the shop does, then a
// regex over the file. Both generations are covered; there is no ubiquitous
// shop CLI on shared hosts.
func (p PrestaShop) ExtractCredentials(ctx context.Context, h db.Host, in detect.Install) (*db.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.Run != nil && h.HasTool("php") && in.ConfigFile != "", func(ctx context.Context) (*db.Credentials, error) {
			return prestashopPHPCredentials(ctx, h.Run, in.ConfigFile)
		}},
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*db.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			return parsePrestaShopConfig(content), nil
		}},
	})
}

// prestashopPHPHelper handles both config generations: parameters.php
// returns an array; settings.inc.php defines _DB_*_ constants.
const prestashopPHPHelper = `
$f = $argv[1];
error_reporting(0);
if (substr($f, -14) === 'parameters.php') {
  $p = include $f;
  if (!is_array($p) || !isset($p['parameters'])) { exit(1); }
  $c = $p['parameters'];
  echo '::REHOST-DB::' . json_encode(array(
    'name' => isset($c['database_name']) ? $c['database_name'] : '',
    'user' => isset($c['database_user']) ? $c['database_user'] : '',
    'password' => isset($c['database_password']) ? $c['database_password'] : '',
    'host' => isset($c['database_host']) ? $c['database_host'] : '',
    'port' => isset($c['database_port']) ? $c['database_port'] : '',
    'prefix' => isset($c['database_prefix']) ? $c['database_prefix'] : ''
  ));
} else {
  include $f;
  echo '::REHOST-DB::' . json_encode(array(
    'name' => defined('_DB_NAME_') ? _DB_NAME_ : '',
    'user' => defined('_DB_USER_') ? _DB_USER_ : '',
    'password' => defined('_DB_PASSWD_') ? _DB_PASSWD_ : '',
    'host' => defined('_DB_SERVER_') ? _DB_SERVER_ : '',
    'prefix' => defined('_DB_PREFIX_') ? _DB_PREFIX_ : ''
  ));
}
`

func prestashopPHPCredentials(ctx context.Context, r db.Runner, configFile string) (*db.Credentials, error) {
	cmd := "php -d display_errors=0 -r " + ssh.ShellQuote(prestashopPHPHelper) + " " + ssh.ShellQuote(configFile) + " 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return parseSentinelCreds(res.Stdout, "php"), nil
}

// parsePrestaShopConfig is the last-resort regex layer, keyed by shape: the
// parameters array ('database_name' => …) or the legacy defines (_DB_NAME_).
func parsePrestaShopConfig(content []byte) *db.Credentials {
	if bytes.Contains(content, []byte("database_name")) {
		name := firstConfigValue(content, "database_name")
		if name == "" {
			return nil
		}
		creds := &db.Credentials{
			Name:        name,
			User:        firstConfigValue(content, "database_user"),
			Password:    firstConfigValue(content, "database_password"),
			TablePrefix: firstConfigValue(content, "database_prefix"),
			Port:        toPort(firstConfigValue(content, "database_port")),
			Method:      "config-parse",
		}
		applyHost(creds, firstConfigValue(content, "database_host"))
		return creds
	}
	masked := maskPHPComments(content)
	name := wpDefine(masked, "_DB_NAME_")
	if name == "" {
		return nil
	}
	creds := &db.Credentials{
		Name:        name,
		User:        wpDefine(masked, "_DB_USER_"),
		Password:    wpDefine(masked, "_DB_PASSWD_"),
		TablePrefix: wpDefine(masked, "_DB_PREFIX_"),
		Method:      "config-parse",
	}
	applyHost(creds, wpDefine(masked, "_DB_SERVER_"))
	return creds
}
