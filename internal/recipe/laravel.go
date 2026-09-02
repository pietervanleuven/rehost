package recipe

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// Laravel fingerprints a Laravel application by its `artisan` console script,
// confirmed against composer.json — a file merely named artisan must not
// match. The detected root is the PROJECT root (the docroot is public/ inside
// it): vendor/, bootstrap/, config/ and storage/ must all travel, so the
// whole project is the migration unit. Credentials live in .env; the
// configured connection (DB_CONNECTION, falling back to config/database.php's
// default — 'sqlite' on a stock Laravel 11+ skeleton) is recorded for the
// driver-aware rules downstream. A sqlite connection means the database is a
// file under the project root that travels with the file sync — no server
// database is involved at all.
type Laravel struct{}

func (Laravel) Name() string { return "laravel" }

func (Laravel) Markers() []string { return []string{"artisan"} }

// laravelLockVersion pulls laravel/framework's version out of composer.lock;
// the bounded gap keeps the match inside one package object.
var laravelLockVersion = regexp.MustCompile(`(?s)"name":\s*"laravel/framework",.{0,200}?"version":\s*"v?([^"]+)"`)

// laravelConfigDefault reads the default-connection fallback out of
// config/database.php: 'default' => env('DB_CONNECTION', 'mysql').
var laravelConfigDefault = regexp.MustCompile(`['"]default['"]\s*=>\s*env\(\s*['"]DB_CONNECTION['"]\s*,\s*['"]([^'"]+)['"]\s*\)`)

func (l Laravel) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	console := path.Join(dir, "artisan")
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
		return nil, nil // a directory named artisan is not the console script
	}
	composer, err := fs.ReadFile(ctx, path.Join(dir, "composer.json"))
	if err != nil || !strings.Contains(string(composer), `"laravel/framework"`) {
		return nil, nil // absence or unreadability means "not a Laravel app", never an error
	}

	install := &detect.Install{Framework: l.Name(), Root: dir}
	if lock, err := fs.ReadFile(ctx, path.Join(dir, "composer.lock")); err == nil {
		install.Version = firstSubmatch(laravelLockVersion, lock)
	}

	var env map[string]string
	envPath := path.Join(dir, ".env")
	if ok, err := fs.Exists(ctx, envPath); err != nil {
		return nil, err
	} else if ok {
		install.ConfigFile = envPath
		if content, err := fs.ReadFile(ctx, envPath); err == nil {
			env = parseEnvFile(content)
		}
	}

	driver := env["DB_CONNECTION"]
	if driver == "" {
		if config, err := fs.ReadFile(ctx, path.Join(dir, "config/database.php")); err == nil {
			driver = firstSubmatch(laravelConfigDefault, config)
		}
	}
	extra := map[string]string{}
	if driver != "" {
		extra["db_driver"] = driver
	}
	if prefix := env["DB_PREFIX"]; prefix != "" {
		extra["table_prefix"] = prefix
	}
	if len(extra) > 0 {
		install.Extra = extra
	}
	return install, nil
}

// laravelMinPHP maps a Laravel major version to its minimum PHP.
func laravelMinPHP(version string) string {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}
	switch n, _ := strconv.Atoi(major); {
	case n >= 11:
		return "8.2"
	case n == 10:
		return "8.1"
	case n == 9:
		return "8.0.2"
	case n == 8:
		return "7.3"
	case n == 7:
		return "7.2.5"
	case n == 6:
		return "7.2"
	default:
		return "7.3"
	}
}
