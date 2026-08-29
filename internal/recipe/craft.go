package recipe

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// Craft fingerprints a Craft CMS project by its `craft` console script,
// confirmed against composer.json — a file merely named craft must not
// match. The detected root is the PROJECT root (the docroot is web/ inside
// it): vendor/, config/ and storage/ must all travel, so the whole project
// is the migration unit. Credentials live in .env (either the CRAFT_DB_ or
// the plain DB_ prefix, per Craft generation); the site runs MySQL/MariaDB
// or PostgreSQL, recorded for the driver-aware rules downstream.
type Craft struct{}

func (Craft) Name() string { return "craft" }

func (Craft) Markers() []string { return []string{"craft"} }

// craftLockVersion pulls craftcms/cms's version out of composer.lock; the
// bounded gap keeps the match inside one package object.
var craftLockVersion = regexp.MustCompile(`(?s)"name":\s*"craftcms/cms",.{0,200}?"version":\s*"v?([^"]+)"`)

func (c Craft) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	console := path.Join(dir, "craft")
	ok, err := fs.Exists(ctx, console)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if isDir, err := fs.IsDir(ctx, console); err != nil {
		return nil, err
	} else if isDir {
		return nil, nil // a directory named craft is not the console script
	}
	composer, err := fs.ReadFile(ctx, path.Join(dir, "composer.json"))
	if err != nil || !strings.Contains(string(composer), `"craftcms/cms"`) {
		return nil, nil // absence or unreadability means "not a Craft project", never an error
	}

	install := &detect.Install{Framework: c.Name(), Root: dir}
	if lock, err := fs.ReadFile(ctx, path.Join(dir, "composer.lock")); err == nil {
		install.Version = firstSubmatch(craftLockVersion, lock)
	}

	envPath := path.Join(dir, ".env")
	if ok, err := fs.Exists(ctx, envPath); err != nil {
		return nil, err
	} else if ok {
		install.ConfigFile = envPath
		if content, err := fs.ReadFile(ctx, envPath); err == nil {
			env := parseEnvFile(content)
			extra := map[string]string{}
			if driver := craftEnv(env, "DRIVER"); driver != "" {
				extra["db_driver"] = driver
			}
			if prefix := craftEnv(env, "TABLE_PREFIX"); prefix != "" {
				extra["table_prefix"] = prefix
			}
			if len(extra) > 0 {
				install.Extra = extra
			}
		}
	}
	return install, nil
}

// craftMinPHP maps a Craft major version to its minimum PHP.
func craftMinPHP(version string) string {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}
	switch n, _ := strconv.Atoi(major); {
	case n >= 5:
		return "8.2"
	case n == 4:
		return "8.0.2"
	case n == 3:
		return "7.2"
	default:
		return "8.0.2"
	}
}
