package recipe

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials reads the Craft database credentials from .env — the
// project's own source of truth (config/db.php conventionally reads these
// very variables). Both env layouts are handled: CRAFT_DB_* (Craft 4/5) and
// DB_* (Craft 3), plus the single-URL forms (CRAFT_DB_URL / DB_DSN).
func (c Craft) ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*hostdb.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*hostdb.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			return parseCraftEnv(content), nil
		}},
	})
}

// parseEnvFile reads KEY=VALUE lines the way dotenv loaders do: optional
// `export `, full-line # comments, values optionally single- or
// double-quoted (escapes decoded for double quotes), no interpolation.
func parseEnvFile(content []byte) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"':
			val = phpUnescape(val[1:len(val)-1], '"')
		case len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'':
			val = val[1 : len(val)-1]
		default:
			// An unquoted value ends at a trailing comment.
			if i := strings.Index(val, " #"); i >= 0 {
				val = strings.TrimSpace(val[:i])
			}
		}
		env[key] = val
	}
	return env
}

// craftEnv reads a DB variable under either prefix, newest first.
func craftEnv(env map[string]string, suffix string) string {
	if v := env["CRAFT_DB_"+suffix]; v != "" {
		return v
	}
	return env["DB_"+suffix]
}

// parseCraftEnv assembles credentials from a parsed .env; nil when no
// database name (or URL) is configured.
func parseCraftEnv(content []byte) *hostdb.Credentials {
	env := parseEnvFile(content)

	if raw := firstNonEmpty(env["CRAFT_DB_URL"], env["DB_DSN"], env["DB_URL"]); raw != "" {
		if creds := parseDBURL(raw); creds != nil {
			return creds
		}
	}

	name := craftEnv(env, "DATABASE")
	if name == "" {
		return nil
	}
	creds := &hostdb.Credentials{
		Driver:      craftEnv(env, "DRIVER"),
		Name:        name,
		User:        craftEnv(env, "USER"),
		Password:    craftEnv(env, "PASSWORD"),
		TablePrefix: craftEnv(env, "TABLE_PREFIX"),
		Method:      "config-parse",
	}
	if port := craftEnv(env, "PORT"); port != "" {
		creds.Port = toPort(port)
	}
	host := craftEnv(env, "SERVER")
	if host == "" {
		host = craftEnv(env, "HOST")
	}
	applyHost(creds, host)
	return creds
}

// parseDBURL decodes a single-URL database config (Craft's CRAFT_DB_URL,
// Laravel's DB_URL/DATABASE_URL): mysql://user:pass@host:port/db.
func parseDBURL(raw string) *hostdb.Credentials {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return nil
	}
	creds := &hostdb.Credentials{
		Driver: u.Scheme,
		Name:   name,
		Host:   u.Hostname(),
		Method: "config-parse",
	}
	if p := u.Port(); p != "" {
		creds.Port, _ = strconv.Atoi(p)
	}
	if u.User != nil {
		creds.User = u.User.Username()
		creds.Password, _ = u.User.Password()
	}
	return creds
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
