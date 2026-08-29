package recipe

import (
	"context"
	"encoding/json"

	"github.com/pietervanleuven/go-ssh"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials reads the Drupal database credentials in layers: drush
// (authoritative), then a PHP echo-helper that includes settings.php, then a
// regex over the file. Multisite: the default site's settings (in.ConfigFile)
// are used; per-subsite credentials are a later phase.
func (d Drupal) ExtractCredentials(ctx context.Context, h db.Host, in detect.Install) (*db.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.Run != nil && h.HasTool("drush"), func(ctx context.Context) (*db.Credentials, error) {
			return drushCredentials(ctx, h.Run, in.Root)
		}},
		{h.Run != nil && h.HasTool("php") && in.ConfigFile != "", func(ctx context.Context) (*db.Credentials, error) {
			return drupalPHPCredentials(ctx, h.Run, in.ConfigFile)
		}},
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*db.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			return parseDrupalSettings(content), nil
		}},
	})
}

// drushCredentials asks drush for the SQL config. `sql-conf` is the alias
// that works across drush 8 through 12+.
func drushCredentials(ctx context.Context, r db.Runner, root string) (*db.Credentials, error) {
	cmd := "cd " + ssh.ShellQuote(root) + " && drush sql-conf --format=json 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, nil
	}
	return parseDrushSQLConf(res.Stdout), nil
}

// parseDrushSQLConf reads `drush sql-conf --format=json` output.
func parseDrushSQLConf(stdout string) *db.Credentials {
	var conf struct {
		Driver   string          `json:"driver"`
		Database string          `json:"database"`
		Username string          `json:"username"`
		Password string          `json:"password"`
		Host     string          `json:"host"`
		Port     any             `json:"port"`
		Prefix   json.RawMessage `json:"prefix"` // string, or a per-table map
	}
	if err := decodeFirstJSON(stdout, &conf); err != nil || conf.Database == "" {
		return nil
	}
	creds := &db.Credentials{
		Driver:   conf.Driver,
		Name:     conf.Database,
		User:     conf.Username,
		Password: conf.Password,
		Port:     toPort(conf.Port),
		Method:   "drush",
	}
	var prefix string
	if json.Unmarshal(conf.Prefix, &prefix) == nil {
		creds.TablePrefix = prefix
	}
	applyHost(creds, conf.Host)
	return creds
}

// drupalPHPHelper includes settings.php the way Drupal core would — with
// $app_root and $site_path defined for D8+ configs that use them — and
// prints the default database target after a sentinel. Works for D7 too
// ($databases has the same shape).
const drupalPHPHelper = `
$f = $argv[1];
$app_root = dirname(dirname(dirname($f)));
$site_path = 'sites/' . basename(dirname($f));
$databases = array();
error_reporting(0);
@include $f;
$d = isset($databases['default']['default']) ? $databases['default']['default'] : null;
if (is_array($d)) {
  $p = isset($d['prefix']) ? $d['prefix'] : '';
  echo '::REHOST-DB::' . json_encode(array(
    'driver' => isset($d['driver']) ? $d['driver'] : '',
    'name' => isset($d['database']) ? $d['database'] : '',
    'user' => isset($d['username']) ? $d['username'] : '',
    'password' => isset($d['password']) ? $d['password'] : '',
    'host' => isset($d['host']) ? $d['host'] : '',
    'port' => isset($d['port']) ? $d['port'] : '',
    'prefix' => is_string($p) ? $p : ''
  ));
}
`

func drupalPHPCredentials(ctx context.Context, r db.Runner, configFile string) (*db.Credentials, error) {
	cmd := "php -d display_errors=0 -r " + ssh.ShellQuote(drupalPHPHelper) + " " + ssh.ShellQuote(configFile) + " 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return parseSentinelCreds(res.Stdout, "php"), nil
}

// parseDrupalSettings is the last-resort regex layer over settings.php,
// scoped to the default connection's array so a memcache/redis/'migrate'
// block earlier in the file can never supply the credentials. A file with no
// recognizable $databases assignment falls back to a whole-file match —
// best-effort beats returning nothing.
func parseDrupalSettings(content []byte) *db.Credentials {
	if s, e, ok := drupalDefaultConnRange(maskPHPComments(content)); ok {
		content = content[s:e]
	}
	name := firstConfigValue(content, "database")
	if name == "" {
		return nil
	}
	creds := &db.Credentials{
		Driver:      firstConfigValue(content, "driver"),
		Name:        name,
		User:        firstConfigValue(content, "username"),
		Password:    firstConfigValue(content, "password"),
		TablePrefix: firstConfigValue(content, "prefix"),
		Method:      "config-parse",
	}
	if port := firstConfigValue(content, "port"); port != "" {
		creds.Port = toPort(port)
	}
	applyHost(creds, firstConfigValue(content, "host"))
	return creds
}
