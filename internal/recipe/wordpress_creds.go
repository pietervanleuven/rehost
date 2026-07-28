package recipe

import (
	"context"
	"regexp"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// ExtractCredentials reads the WordPress database credentials in layers:
// wp-cli (authoritative — resolves PHP however the site does), then a PHP
// echo-helper that evaluates wp-config.php, then a plain regex over the file.
// Each layer hands over to the next on any failure; only transport errors
// abort.
func (w WordPress) ExtractCredentials(ctx context.Context, h db.Host, in detect.Install) (*db.Credentials, error) {
	if h.Run != nil && h.HasTool("wp") {
		creds, err := wpCLICredentials(ctx, h.Run, in.Root)
		if err != nil || creds != nil {
			return creds, err
		}
	}
	if h.Run != nil && h.HasTool("php") && in.ConfigFile != "" {
		creds, err := wpPHPCredentials(ctx, h.Run, in.ConfigFile)
		if err != nil || creds != nil {
			return creds, err
		}
	}
	if h.FS != nil && in.ConfigFile != "" {
		content, err := h.FS.ReadFile(ctx, in.ConfigFile)
		if err != nil {
			return nil, err
		}
		return parseWPConfig(content), nil
	}
	return nil, nil
}

// wpCLICredentials asks wp-cli for the config. --skip-plugins/--skip-themes
// keeps site code out of the way; stderr is dropped (PHP notices).
func wpCLICredentials(ctx context.Context, r db.Runner, root string) (*db.Credentials, error) {
	cmd := "cd " + ssh.ShellQuote(root) + " && wp config list --format=json --skip-plugins --skip-themes 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, nil
	}
	return parseWPConfigList(res.Stdout), nil
}

// parseWPConfigList reads `wp config list --format=json` output.
func parseWPConfigList(stdout string) *db.Credentials {
	var entries []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := decodeFirstJSON(stdout, &entries); err != nil {
		return nil
	}
	m := map[string]string{}
	for _, e := range entries {
		m[e.Name] = e.Value
	}
	if m["DB_NAME"] == "" {
		return nil
	}
	creds := &db.Credentials{
		Name:        m["DB_NAME"],
		User:        m["DB_USER"],
		Password:    m["DB_PASSWORD"],
		TablePrefix: m["table_prefix"],
		Method:      "wp-cli",
	}
	applyHost(creds, m["DB_HOST"])
	return creds
}

// wpPHPHelper evaluates wp-config.php without booting WordPress: the
// require of wp-settings.php is stripped, the rest runs as the site's own
// PHP would run it (dynamic configs work), and the DB constants are printed
// after a sentinel.
const wpPHPHelper = `
$f = $argv[1];
$c = file_get_contents($f);
$c = preg_replace('~^.*wp-settings\.php.*$~m', '', $c);
chdir(dirname($f));
error_reporting(0);
eval('?>' . $c);
echo '::REHOST-DB::' . json_encode(array(
  'name' => defined('DB_NAME') ? DB_NAME : '',
  'user' => defined('DB_USER') ? DB_USER : '',
  'password' => defined('DB_PASSWORD') ? DB_PASSWORD : '',
  'host' => defined('DB_HOST') ? DB_HOST : '',
  'prefix' => isset($table_prefix) ? $table_prefix : ''
));
`

func wpPHPCredentials(ctx context.Context, r db.Runner, configFile string) (*db.Credentials, error) {
	cmd := "php -d display_errors=0 -r " + ssh.ShellQuote(wpPHPHelper) + " " + ssh.ShellQuote(configFile) + " 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return parseSentinelCreds(res.Stdout, "php"), nil
}

// wpDefine matches define('KEY', 'value') with either quote style.
func wpDefine(content []byte, key string) string {
	re := regexp.MustCompile(`define\(\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*,\s*(?:'([^']*)'|"([^"]*)")\s*\)`)
	if m := re.FindSubmatch(content); m != nil {
		if m[1] != nil {
			return string(m[1])
		}
		return string(m[2])
	}
	return ""
}

// parseWPConfig is the last-resort regex layer over wp-config.php.
func parseWPConfig(content []byte) *db.Credentials {
	name := wpDefine(content, "DB_NAME")
	if name == "" {
		return nil
	}
	creds := &db.Credentials{
		Name:        name,
		User:        wpDefine(content, "DB_USER"),
		Password:    wpDefine(content, "DB_PASSWORD"),
		TablePrefix: firstSubmatch(wpTablePrefix, content),
		Method:      "config-parse",
	}
	applyHost(creds, wpDefine(content, "DB_HOST"))
	return creds
}
