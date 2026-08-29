package recipe

import (
	"context"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials reads the Joomla database credentials in layers: a PHP
// echo-helper that instantiates JConfig (dynamic configs work), then a regex
// over configuration.php. Joomla has no ubiquitous site CLI on shared hosts,
// so there is no CLI layer.
func (j Joomla) ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*hostdb.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.Run != nil && h.HasTool("php") && in.ConfigFile != "", func(ctx context.Context) (*hostdb.Credentials, error) {
			return joomlaPHPCredentials(ctx, h.Run, in.ConfigFile)
		}},
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*hostdb.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			return parseJoomlaConfig(content), nil
		}},
	})
}

// joomlaPHPHelper evaluates configuration.php the way Joomla itself does:
// require the file, instantiate JConfig, print the DB properties after the
// sentinel.
const joomlaPHPHelper = `
$f = $argv[1];
error_reporting(0);
require $f;
if (!class_exists('JConfig')) { exit(1); }
$c = new JConfig();
echo '::REHOST-DB::' . json_encode(array(
  'driver' => isset($c->dbtype) ? $c->dbtype : '',
  'name' => isset($c->db) ? $c->db : '',
  'user' => isset($c->user) ? $c->user : '',
  'password' => isset($c->password) ? $c->password : '',
  'host' => isset($c->host) ? $c->host : '',
  'prefix' => isset($c->dbprefix) ? $c->dbprefix : ''
));
`

func joomlaPHPCredentials(ctx context.Context, r remote.Runner, configFile string) (*hostdb.Credentials, error) {
	cmd := "php -d display_errors=0 -r " + remote.ShellQuote(joomlaPHPHelper) + " " + remote.ShellQuote(configFile) + " 2>/dev/null"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return parseSentinelCreds(res.Stdout, "php"), nil
}

// parseJoomlaConfig is the last-resort regex layer over configuration.php.
func parseJoomlaConfig(content []byte) *hostdb.Credentials {
	masked := maskPHPComments(content)
	name := joomlaProperty(masked, "db")
	if name == "" {
		return nil
	}
	creds := &hostdb.Credentials{
		Driver:      joomlaProperty(masked, "dbtype"),
		Name:        name,
		User:        joomlaProperty(masked, "user"),
		Password:    joomlaProperty(masked, "password"),
		TablePrefix: joomlaProperty(masked, "dbprefix"),
		Method:      "config-parse",
	}
	applyHost(creds, joomlaProperty(masked, "host"))
	return creds
}
