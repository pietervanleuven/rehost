package recipe

import (
	"context"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials reads the Laravel database credentials from .env — the
// application's own source of truth (config/database.php reads these very
// variables). The single-URL forms (DB_URL / DATABASE_URL) are handled. A
// sqlite connection yields no credentials on purpose: the database is a file
// under the project root and travels with the file sync.
func (l Laravel) ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*hostdb.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*hostdb.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			return parseLaravelEnv(content, in.Extra["db_driver"]), nil
		}},
	})
}

// parseLaravelEnv assembles credentials from a parsed .env; nil when the
// connection is sqlite or no database name (or URL) is configured. The
// fallback driver is what detection resolved from config/database.php for an
// .env that leaves DB_CONNECTION unset.
func parseLaravelEnv(content []byte, fallbackDriver string) *hostdb.Credentials {
	env := parseEnvFile(content)

	driver := env["DB_CONNECTION"]
	if driver == "" {
		driver = fallbackDriver
	}
	if driver == "sqlite" {
		return nil
	}

	if raw := firstNonEmpty(env["DB_URL"], env["DATABASE_URL"]); raw != "" {
		if creds := parseDBURL(raw); creds != nil {
			return creds
		}
	}

	name := env["DB_DATABASE"]
	if name == "" {
		return nil
	}
	creds := &hostdb.Credentials{
		Driver:      driver,
		Name:        name,
		User:        env["DB_USERNAME"],
		Password:    env["DB_PASSWORD"],
		TablePrefix: env["DB_PREFIX"],
		Method:      "config-parse",
	}
	if port := env["DB_PORT"]; port != "" {
		creds.Port = toPort(port)
	}
	applyHost(creds, env["DB_HOST"])
	return creds
}
