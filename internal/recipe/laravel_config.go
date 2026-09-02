package recipe

import (
	"context"
	"fmt"
	"path"
	"strconv"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// RewriteConfig points the synced .env at the destination database.
// Everything else in .env — APP_KEY above all — stays byte-exact. An app
// configured through a single DB URL/DSN degrades to guidance: splicing
// credentials into a URL is a hand edit worth reviewing anyway.
func (l Laravel) RewriteConfig(ctx context.Context, h Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the project root and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewriteLaravelEnv(content, rw.DB)
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
			"set %s in %s by hand — the variable is not present to rewrite, so the app still uses the source's value",
			joinAnd(missing), confPath))
	}
	res.PostSteps = append(res.PostSteps,
		"run 'php artisan config:clear' in the project root — a cached bootstrap/cache/config.php would keep serving the source's settings")
	return res, nil
}

// rewriteLaravelEnv is the pure transform: each DB variable present in the
// file gets the destination value; absent auth-critical variables are
// reported rather than appended (an append could contradict a customized
// config/database.php).
func rewriteLaravelEnv(content []byte, creds hostdb.Credentials) ([]byte, []string, error) {
	if envHasAny(content, "DB_URL", "DATABASE_URL") {
		return nil, nil, fmt.Errorf("the database is configured as a single URL/DSN")
	}
	if parseEnvFile(content)["DB_CONNECTION"] == "sqlite" {
		return nil, nil, fmt.Errorf("the connection is sqlite — the database file travels with the file sync")
	}
	if !envHasAny(content, "DB_DATABASE") {
		return nil, nil, fmt.Errorf("no DB_DATABASE variable found")
	}

	content, _ = replaceEnvValue(content, "DB_DATABASE", creds.Name)
	var missing []string
	var ok bool
	if content, ok = replaceEnvValue(content, "DB_USERNAME", creds.User); !ok && creds.User != "" {
		missing = append(missing, "DB_USERNAME")
	}
	if content, ok = replaceEnvValue(content, "DB_PASSWORD", creds.Password); !ok && creds.Password != "" {
		missing = append(missing, "DB_PASSWORD")
	}
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	content, _ = replaceEnvValue(content, "DB_HOST", host)
	if creds.Port != 0 {
		content, _ = replaceEnvValue(content, "DB_PORT", strconv.Itoa(creds.Port))
	}
	return content, missing, nil
}

// Maintenance: `php artisan down` serves a 503 from Laravel itself; `artisan
// up` lifts it, and both converge when the app is already in the requested
// state. The console script needs the host's PHP CLI; there is no file
// fallback — Laravel 8+ expects a JSON payload in storage/framework/down
// that only the framework itself writes correctly.

func (l Laravel) EnableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return l.artisanToggle(ctx, h, in, true)
}

func (l Laravel) DisableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return l.artisanToggle(ctx, h, in, false)
}

func (l Laravel) artisanToggle(ctx context.Context, h Host, in detect.Install, on bool) (MaintenanceResult, error) {
	if h.Run == nil || !h.HasTool("php") {
		return MaintenanceResult{Supported: false,
			Note: "no PHP CLI to run the artisan console — writes during the window may be lost"}, nil
	}
	sub := "up"
	if on {
		sub = "down"
	}
	cmd := "cd " + remote.ShellQuote(in.Root) + " && php artisan " + sub + " --no-interaction 2>&1"
	res, err := h.Run.Run(ctx, cmd)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if res.ExitCode != 0 {
		return MaintenanceResult{Supported: false,
			Note: "'artisan " + sub + "' failed (" + remote.FirstLine(res.Stdout) + ") — writes during the window may be lost"}, nil
	}
	return MaintenanceResult{State: maintenanceState(on), Method: "artisan", Supported: true}, nil
}

// MaintenanceStatus reads the framework's own marker: storage/framework/down
// exists exactly while maintenance mode is active (stable across Laravel 5–12).
func (l Laravel) MaintenanceStatus(ctx context.Context, h Host, in detect.Install) (MaintenanceState, error) {
	if h.Run == nil {
		return MaintenanceUnknown, nil
	}
	ok, err := remoteExists(ctx, h.Run, path.Join(in.Root, "storage/framework/down"))
	if err != nil {
		return MaintenanceUnknown, err
	}
	if ok {
		return MaintenanceOn, nil
	}
	return MaintenanceOff, nil
}
