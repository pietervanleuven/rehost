package recipe

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// RewriteConfig points the synced .env at the destination database. The
// variable prefix (CRAFT_DB_ or DB_) follows what the file already uses;
// everything else in .env — the security key above all — stays byte-exact.
// A project configured through a single DB URL/DSN degrades to guidance:
// splicing credentials into a URL is a hand edit worth reviewing anyway.
func (c Craft) RewriteConfig(ctx context.Context, h db.Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the project root and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewriteCraftEnv(content, rw.DB)
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
			"set %s in %s by hand — the variable is not present to rewrite, so the project still uses the source's value",
			joinAnd(missing), confPath))
	}
	res.PostSteps = append(res.PostSteps,
		"run 'php craft clear-caches/all' in the project root (or clear caches from the control panel)")
	return res, nil
}

// rewriteCraftEnv is the pure transform: each DB variable present in the
// file gets the destination value; absent auth-critical variables are
// reported rather than appended (an append could contradict config/db.php).
func rewriteCraftEnv(content []byte, creds db.Credentials) ([]byte, []string, error) {
	if envHasAny(content, "CRAFT_DB_URL", "DB_DSN", "DB_URL") {
		return nil, nil, fmt.Errorf("the database is configured as a single URL/DSN")
	}
	prefix := ""
	switch {
	case envHasAny(content, "CRAFT_DB_DATABASE"):
		prefix = "CRAFT_DB_"
	case envHasAny(content, "DB_DATABASE"):
		prefix = "DB_"
	default:
		return nil, nil, fmt.Errorf("no CRAFT_DB_DATABASE/DB_DATABASE variable found")
	}

	content, _ = replaceEnvValue(content, prefix+"DATABASE", creds.Name)
	var missing []string
	var ok bool
	if content, ok = replaceEnvValue(content, prefix+"USER", creds.User); !ok && creds.User != "" {
		missing = append(missing, prefix+"USER")
	}
	if content, ok = replaceEnvValue(content, prefix+"PASSWORD", creds.Password); !ok && creds.Password != "" {
		missing = append(missing, prefix+"PASSWORD")
	}
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	if content, ok = replaceEnvValue(content, prefix+"SERVER", host); !ok {
		content, _ = replaceEnvValue(content, prefix+"HOST", host)
	}
	if creds.Port != 0 {
		content, _ = replaceEnvValue(content, prefix+"PORT", strconv.Itoa(creds.Port))
	}
	return content, missing, nil
}

// envHasAny reports whether any of the variables is assigned in the file.
func envHasAny(content []byte, keys ...string) bool {
	env := parseEnvFile(content)
	for _, k := range keys {
		if _, ok := env[k]; ok {
			return true
		}
	}
	return false
}

// replaceEnvValue rewrites the value of one KEY= line in place, preserving
// every other byte of the file. The new value is double-quoted with
// backslash escaping — the dotenv-safe encoding for arbitrary passwords.
func replaceEnvValue(content []byte, key, value string) ([]byte, bool) {
	re := regexp.MustCompile(`(?m)^(\s*(?:export\s+)?` + regexp.QuoteMeta(key) + `\s*=).*$`)
	loc := re.FindSubmatchIndex(content)
	if loc == nil {
		return content, false
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	out := make([]byte, 0, len(content)+len(quoted))
	out = append(out, content[:loc[3]]...)
	out = append(out, quoted...)
	out = append(out, content[loc[1]:]...)
	return out, true
}

// Maintenance: `craft off` serves a 503 from Craft itself (3.5+); `craft on`
// lifts it. The console script needs the host's PHP CLI; there is no file
// fallback because Craft has none.

func (c Craft) EnableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	return c.craftToggle(ctx, h, in, true)
}

// DisableMaintenance is idempotent: `craft on` when already on converges.
func (c Craft) DisableMaintenance(ctx context.Context, h db.Host, in detect.Install) (MaintenanceResult, error) {
	return c.craftToggle(ctx, h, in, false)
}

func (c Craft) craftToggle(ctx context.Context, h db.Host, in detect.Install, on bool) (MaintenanceResult, error) {
	if h.Run == nil || !h.HasTool("php") {
		return MaintenanceResult{Supported: false,
			Note: "no PHP CLI to run the craft console — writes during the window may be lost"}, nil
	}
	sub := "on"
	if on {
		sub = "off"
	}
	cmd := "cd " + ssh.ShellQuote(in.Root) + " && php craft " + sub + " --interactive=0 2>&1"
	res, err := h.Run.Run(ctx, cmd)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if res.ExitCode != 0 {
		return MaintenanceResult{Supported: false,
			Note: "'craft " + sub + "' failed (" + ssh.FirstLine(res.Stdout) + ") — writes during the window may be lost"}, nil
	}
	return MaintenanceResult{State: maintenanceState(on), Method: "craft-cli", Supported: true}, nil
}

// MaintenanceStatus: the console has no status query, so only history can
// answer — Unknown lets the caller fall back to it.
func (c Craft) MaintenanceStatus(ctx context.Context, h db.Host, in detect.Install) (MaintenanceState, error) {
	return MaintenanceUnknown, nil
}
