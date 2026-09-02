package recipe

import (
	"context"
	"path"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// Generic is the PHP+database fallback: a site that matches no framework but
// keeps its database credentials in a config file rehost can read. It is what
// hand-rolled PHP — the long tail of shared hosting — looks like.
//
// It declares no fingerprint markers on purpose. The names a generic config
// goes by (config.php above all) also occur inside framework plugin and
// vendor trees, so fingerprinting on them would turn every such file into a
// candidate root and cost a burst of SSH round trips on sites that are not
// generic at all. Discover scans the conventional docroots regardless, which
// is where a hand-rolled site actually lives; anything nested elsewhere is
// reached with --docroot.
//
// Detection is deliberately narrow: an entry document (index.php) plus a
// config file that yields a usable database name. Without credentials there
// is nothing a migration could do beyond copying files, which is what the
// static recipe already covers.
type Generic struct{}

func (Generic) Name() string { return "generic-php" }

// genericConfigNames are the file names hand-rolled PHP keeps its database
// settings in, most conventional first. Only files present in the root
// listing are read, so the length of this list costs nothing per site.
var genericConfigNames = []string{
	"config.php",
	"config.inc.php",
	"configuration.php",
	"db.php",
	"database.php",
	"dbconfig.php",
	"db_config.php",
	"connect.php",
	"dbconnect.php",
	"db_connect.php",
	"connection.php",
	"settings.php",
	"init.php",
	"common.php",
	"config.local.php",
}

// genericConfigSubdirs are the well-known places a config sits one level
// down, keyed by the directory that must exist for the path to be worth a
// probe. Framework-shaped layouts (application/config) are included because
// a framework rehost has no recipe for still migrates as generic PHP.
var genericConfigSubdirs = map[string][]string{
	"includes":    {"includes/config.php", "includes/db.php", "includes/connect.php", "includes/config.inc.php"},
	"inc":         {"inc/config.php", "inc/db.php", "inc/connect.php"},
	"config":      {"config/config.php", "config/database.php", "config/db.php"},
	"conf":        {"conf/config.php", "conf/database.php"},
	"application": {"application/config/database.php"},
	"admin":       {"admin/config.php"},
}

// genericMaxReads bounds how many candidate files one root may cost. A
// generic config is found in the first one or two in practice; the cap keeps
// a directory full of config-shaped files from turning detection into a
// round-trip storm.
const genericMaxReads = 5

func (g Generic) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	names, err := fs.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}
	if !present["index.php"] {
		return nil, nil // not an application root — a library or asset directory
	}

	var candidates []string
	for _, name := range genericConfigNames {
		if present[name] {
			candidates = append(candidates, name)
		}
	}
	for _, subdir := range genericSubdirOrder {
		if present[subdir] {
			candidates = append(candidates, genericConfigSubdirs[subdir]...)
		}
	}

	reads := 0
	for _, rel := range candidates {
		if reads >= genericMaxReads {
			break
		}
		full := path.Join(dir, rel)
		if strings.Contains(rel, "/") {
			// Subdirectory candidates were never listed, only implied by the
			// directory's presence; confirm before spending a read.
			ok, err := fs.Exists(ctx, full)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		reads++
		content, err := fs.ReadFile(ctx, full)
		if err != nil {
			continue // unreadable is not a transport failure worth aborting on
		}
		parsed := parseGenericConfig(content)
		if parsed == nil {
			continue
		}
		install := &detect.Install{Framework: g.Name(), Root: dir, ConfigFile: full}
		extra := map[string]string{}
		if parsed.api != "" {
			extra["db_api"] = parsed.api
		}
		if parsed.creds.Driver != "" {
			extra["db_driver"] = parsed.creds.Driver
		}
		if parsed.creds.TablePrefix != "" {
			extra["table_prefix"] = parsed.creds.TablePrefix
		}
		if len(extra) > 0 {
			install.Extra = extra
		}
		return install, nil
	}
	return nil, nil
}

// genericSubdirOrder fixes the order genericConfigSubdirs is walked in; a map
// alone would make detection depend on Go's randomized range order, so two
// runs against the same site could pick different config files.
var genericSubdirOrder = []string{"includes", "inc", "config", "conf", "application", "admin"}
