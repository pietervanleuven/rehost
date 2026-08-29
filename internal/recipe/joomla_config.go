package recipe

import (
	"bytes"
	"context"
	"fmt"
	"regexp"

	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// RewriteConfig points the synced configuration.php at the destination
// database: the JConfig $db/$user/$password/$host properties are replaced in
// place; $secret, paths and everything else stay byte-exact. Joomla keeps a
// port inside $host ("localhost:3306"), so a configured port folds back in.
func (j Joomla) RewriteConfig(ctx context.Context, h Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the docroot and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewriteJoomlaConfig(content, rw.DB)
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
			"set the destination database %s in %s by hand — it is not a literal the rewrite could replace, so the site still uses the source's",
			joinAnd(missing), confPath))
	}
	res.PostSteps = append(res.PostSteps,
		"clear the Joomla cache (Administrator → System → Clear Cache, or empty cache/ and administrator/cache/)")
	return res, nil
}

// joinAnd joins names with " and " for prose.
func joinAnd(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		out := names[0]
		for _, n := range names[1:] {
			out += " and " + n
		}
		return out
	}
}

// rewriteJoomlaConfig is the pure transform: each JConfig DB property gets
// the destination value as a single-quoted literal. It returns the
// auth-critical properties (user, password) it could not replace.
func rewriteJoomlaConfig(content []byte, creds db.Credentials) ([]byte, []string, error) {
	host := creds.Host
	if host == "" {
		host = "localhost"
	}
	if creds.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, creds.Port)
	}
	var ok bool
	if content, ok = replaceJoomlaProperty(content, "db", creds.Name); !ok {
		return nil, nil, fmt.Errorf("no JConfig $db property found")
	}
	var missing []string
	if content, ok = replaceJoomlaProperty(content, "user", creds.User); !ok && creds.User != "" {
		missing = append(missing, "user")
	}
	if content, ok = replaceJoomlaProperty(content, "password", creds.Password); !ok && creds.Password != "" {
		missing = append(missing, "password")
	}
	content, _ = replaceJoomlaProperty(content, "host", host)
	return content, missing, nil
}

// joomlaPropertyLiteral matches a JConfig property with a quoted, boolean or
// numeric literal value — $offline is routinely a bare false/'0'.
func joomlaPropertyLiteral(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?:public|var|protected)\s+\$` + regexp.QuoteMeta(key) +
		`\s*=\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|true|false|\d+)`)
}

// replaceJoomlaProperty splices a new single-quoted value into the first
// non-commented `public $key = <literal>;` occurrence. Matching runs against
// a comment-masked copy; offsets line up with the original byte-for-byte.
func replaceJoomlaProperty(content []byte, key, value string) ([]byte, bool) {
	loc := joomlaPropertyLiteral(key).FindSubmatchIndex(maskPHPComments(content))
	if loc == nil {
		return content, false
	}
	var b bytes.Buffer
	b.Write(content[:loc[2]])
	b.WriteString(phpSingleQuote(value))
	b.Write(content[loc[3]:])
	return b.Bytes(), true
}

// Maintenance: Joomla's offline mode is the $offline property in
// configuration.php — there is no maintenance file and no ubiquitous CLI on
// shared hosts, so the recipe splices '1'/'0' into the config through the
// same atomic temp+rename write the config rewrite uses.

func (j Joomla) EnableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return j.setOffline(ctx, h, in, true)
}

// DisableMaintenance is idempotent: writing '0' over '0' converges.
func (j Joomla) DisableMaintenance(ctx context.Context, h Host, in detect.Install) (MaintenanceResult, error) {
	return j.setOffline(ctx, h, in, false)
}

func (j Joomla) setOffline(ctx context.Context, h Host, in detect.Install, on bool) (MaintenanceResult, error) {
	if h.Run == nil || in.ConfigFile == "" {
		return MaintenanceResult{Supported: false,
			Note: "no configuration.php to toggle $offline in — put the site offline in the administrator backend"}, nil
	}
	res, err := h.Run.Run(ctx, "cat -- "+remote.ShellQuote(in.ConfigFile))
	if err != nil {
		return MaintenanceResult{}, err
	}
	if res.ExitCode != 0 {
		return MaintenanceResult{}, fmt.Errorf("%w: reading %s: %s", ErrMaintenanceTool, in.ConfigFile, remote.FirstLine(res.Stderr))
	}
	value := "0"
	if on {
		value = "1"
	}
	rewritten, ok := replaceJoomlaProperty([]byte(res.Stdout), "offline", value)
	if !ok {
		return MaintenanceResult{Supported: false,
			Note: "configuration.php has no recognizable $offline property — put the site offline in the administrator backend"}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, in.ConfigFile, rewritten); err != nil {
		// The write helper conflates transport and exit failures; treating
		// both as a tool failure lets the caller degrade to dumping the live
		// site, and a real transport loss fails the very next step loudly.
		return MaintenanceResult{}, fmt.Errorf("%w: %v", ErrMaintenanceTool, err)
	}
	return MaintenanceResult{State: maintenanceState(on), Method: "config", Supported: true}, nil
}

// MaintenanceStatus reads $offline back out of configuration.php.
func (j Joomla) MaintenanceStatus(ctx context.Context, h Host, in detect.Install) (MaintenanceState, error) {
	if h.Run == nil || in.ConfigFile == "" {
		return MaintenanceUnknown, nil
	}
	res, err := h.Run.Run(ctx, "cat -- "+remote.ShellQuote(in.ConfigFile))
	if err != nil {
		return MaintenanceUnknown, err
	}
	if res.ExitCode != 0 {
		return MaintenanceUnknown, nil
	}
	m := joomlaPropertyLiteral("offline").FindSubmatch(maskPHPComments([]byte(res.Stdout)))
	if m == nil {
		return MaintenanceUnknown, nil
	}
	switch string(bytes.Trim(m[1], `'"`)) {
	case "1", "true":
		return MaintenanceOn, nil
	case "0", "false", "":
		return MaintenanceOff, nil
	}
	return MaintenanceUnknown, nil
}
