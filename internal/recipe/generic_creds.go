package recipe

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	hostdb "github.com/pietervanleuven/go-hostdb"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// ExtractCredentials re-reads the config file detection settled on. There is
// no CLI or PHP-helper layer: a hand-rolled config has no framework CLI, and
// including an unknown file to evaluate it would run whatever that file does
// — connect, redirect, print. Parsing is the only honest layer here.
func (g Generic) ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*hostdb.Credentials, error) {
	return extractLayered(ctx, []credLayer{
		{h.FS != nil && in.ConfigFile != "", func(ctx context.Context) (*hostdb.Credentials, error) {
			content, err := h.FS.ReadFile(ctx, in.ConfigFile)
			if err != nil {
				return nil, err
			}
			parsed := parseGenericConfig(content)
			if parsed == nil {
				return nil, nil
			}
			return parsed.creds, nil
		}},
	})
}

// genericSlot is the byte range of one quoted literal that supplied a
// credential, in the original file. maskPHPComments preserves length, so an
// offset found in the mask indexes the original unchanged — which is what
// lets the rewrite splice a new value into exactly the literal it read.
type genericSlot struct {
	field      string
	start, end int // the whole literal, quotes included
}

// genericParse is one config file's yield.
type genericParse struct {
	creds *hostdb.Credentials
	// slots is empty when the credentials came from a positional call
	// (mysqli_connect, new PDO) rather than named assignments — there is
	// nothing safe to splice, so the rewrite degrades to guidance.
	slots []genericSlot
	api   string // mysqli, pdo_mysql, mysql (the removed API), pgsql, or ""
}

// The key spellings generic PHP uses, most specific first. Bare names (USER,
// PASSWORD) come last: they also name FTP or SMTP settings, so a DB-prefixed
// spelling in the same file must win.
var (
	genericNameKeys   = []string{"DB_NAME", "DB_DATABASE", "DBNAME", "DB_DBNAME", "MYSQL_DATABASE", "MYSQL_DB", "PGSQL_DATABASE", "DATABASE_NAME", "DB_BASE", "DATABASE"}
	genericUserKeys   = []string{"DB_USER", "DB_USERNAME", "DBUSER", "DB_LOGIN", "MYSQL_USER", "PGSQL_USER", "DATABASE_USER", "USERNAME", "USER"}
	genericPassKeys   = []string{"DB_PASSWORD", "DB_PASS", "DBPASSWORD", "DBPASS", "MYSQL_PASSWORD", "PGSQL_PASSWORD", "DATABASE_PASSWORD", "PASSWORD", "PASS"}
	genericHostKeys   = []string{"DB_HOST", "DB_SERVER", "DBHOST", "MYSQL_HOST", "PGSQL_HOST", "DATABASE_HOST", "HOSTNAME", "SERVER", "HOST"}
	genericPortKeys   = []string{"DB_PORT", "DBPORT", "MYSQL_PORT", "DATABASE_PORT", "PORT"}
	genericPrefixKeys = []string{"DB_PREFIX", "TABLE_PREFIX", "DBPREFIX", "PREFIX"}
)

// phpLiteral captures a quoted PHP string, escape-aware: group 1 is a
// single-quoted body, group 2 a double-quoted one.
const phpLiteral = `(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")`

// genericAssignForms are the four shapes a generic config assigns a literal
// in: a define, a scalar variable, an array arrow, and an array-index
// assignment (`$db['default']['database'] = '…'`). %s is the quoted key.
var genericAssignForms = []string{
	`(?i)define\s*\(\s*['"]%s['"]\s*,\s*` + phpLiteral,
	`(?i)\$%s\s*=\s*` + phpLiteral,
	`(?i)['"]%s['"]\s*=>\s*` + phpLiteral,
	`(?i)\[\s*['"]%s['"]\s*\]\s*=\s*` + phpLiteral,
}

// genericKeyRes holds every key's compiled forms. Compiling once at init
// keeps detection allocation-free per file and, more importantly, keeps the
// table read-only: plan probes source and destination concurrently, so a
// lazily filled cache here would be a data race.
var genericKeyRes = func() map[string][]*regexp.Regexp {
	res := map[string][]*regexp.Regexp{}
	for _, keys := range [][]string{
		genericNameKeys, genericUserKeys, genericPassKeys,
		genericHostKeys, genericPortKeys, genericPrefixKeys,
	} {
		for _, key := range keys {
			if _, done := res[key]; done {
				continue
			}
			forms := make([]*regexp.Regexp, 0, len(genericAssignForms))
			for _, form := range genericAssignForms {
				forms = append(forms, regexp.MustCompile(strings.Replace(form, "%s", regexp.QuoteMeta(key), 1)))
			}
			res[key] = forms
		}
	}
	return res
}()

// genericAssign finds the first form in which key is assigned a quoted
// literal, returning the decoded value and the literal's range.
func genericAssign(masked []byte, key string) (value string, start, end int, ok bool) {
	for _, re := range genericKeyRes[key] {
		m := re.FindSubmatchIndex(masked)
		if m == nil {
			continue
		}
		// Groups: 2,3 = single-quoted body; 4,5 = double-quoted body. The
		// literal itself spans one byte either side of the body.
		if m[2] >= 0 {
			return phpUnescape(string(masked[m[2]:m[3]]), '\''), m[2] - 1, m[3] + 1, true
		}
		return phpUnescape(string(masked[m[4]:m[5]]), '"'), m[4] - 1, m[5] + 1, true
	}
	return "", 0, 0, false
}

// genericFirst walks a key list and returns the first assignment found.
func genericFirst(masked []byte, keys []string) (string, genericSlot, bool) {
	for _, key := range keys {
		if value, start, end, ok := genericAssign(masked, key); ok {
			return value, genericSlot{start: start, end: end}, true
		}
	}
	return "", genericSlot{}, false
}

// parseGenericConfig reads database credentials out of an arbitrary PHP
// config. Comments are masked first, so a commented-out old connection can
// never shadow the live one. Named assignments are tried before positional
// connect calls: only the former can be rewritten on the destination.
func parseGenericConfig(content []byte) *genericParse {
	masked := maskPHPComments(content)
	api := detectDBAPI(masked)

	name, nameSlot, found := genericFirst(masked, genericNameKeys)
	if found && name != "" {
		creds := &hostdb.Credentials{Name: name, Method: "config-parse"}
		slots := []genericSlot{{field: "name", start: nameSlot.start, end: nameSlot.end}}
		if v, s, ok := genericFirst(masked, genericUserKeys); ok {
			creds.User = v
			slots = append(slots, genericSlot{field: "user", start: s.start, end: s.end})
		}
		if v, s, ok := genericFirst(masked, genericPassKeys); ok {
			creds.Password = v
			slots = append(slots, genericSlot{field: "password", start: s.start, end: s.end})
		}
		if v, s, ok := genericFirst(masked, genericHostKeys); ok {
			applyHost(creds, v)
			slots = append(slots, genericSlot{field: "host", start: s.start, end: s.end})
		}
		if v, s, ok := genericFirst(masked, genericPortKeys); ok {
			if port, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && port > 0 && port < 65536 {
				creds.Port = port
				slots = append(slots, genericSlot{field: "port", start: s.start, end: s.end})
			}
		}
		if v, _, ok := genericFirst(masked, genericPrefixKeys); ok {
			creds.TablePrefix = v
		}
		if api == "pgsql" {
			creds.Driver = hostdb.DriverPostgres
		}
		return &genericParse{creds: creds, slots: slots, api: api}
	}

	if creds := parseGenericConnectCall(masked); creds != nil {
		if api == "pgsql" || hostdb.NormalizeDriver(creds.Driver) == hostdb.DriverPostgres {
			creds.Driver = hostdb.DriverPostgres
		}
		return &genericParse{creds: creds, api: api}
	}
	return nil
}

// Positional connection forms. The database is the fourth mysqli argument, or
// a separate select-db call; PDO carries everything in its DSN.
var (
	genericMysqli = regexp.MustCompile(`(?i)(?:new\s+mysqli|mysqli_connect)\s*\(\s*` + phpLiteral +
		`\s*,\s*` + phpLiteral + `\s*,\s*` + phpLiteral + `(?:\s*,\s*` + phpLiteral + `)?`)
	genericMysqlConnect = regexp.MustCompile(`(?i)mysql_connect\s*\(\s*` + phpLiteral +
		`\s*,\s*` + phpLiteral + `\s*,\s*` + phpLiteral)
	genericSelectDB = regexp.MustCompile(`(?i)mysqli?_select_db\s*\(\s*(?:\$[A-Za-z_][A-Za-z0-9_]*\s*,\s*)?` + phpLiteral)
	genericPDO      = regexp.MustCompile(`(?i)new\s+PDO\s*\(\s*` + phpLiteral +
		`(?:\s*,\s*` + phpLiteral + `)?(?:\s*,\s*` + phpLiteral + `)?`)
)

// parseGenericConnectCall reads credentials passed straight to a connection
// call. These cannot be rewritten (the literals carry no key naming what they
// are, and a wrong guess would point the site at the wrong database), but
// they are still worth reading: the dump and import need them.
func parseGenericConnectCall(masked []byte) *hostdb.Credentials {
	if m := genericPDO.FindSubmatch(masked); m != nil {
		if creds := parsePDODSN(quotedValue(m, 1)); creds != nil {
			// The DSN is the first literal; the optional second and third
			// are the user and password.
			if m[3] != nil || m[4] != nil {
				creds.User = quotedValue(m, 3)
			}
			if m[5] != nil || m[6] != nil {
				creds.Password = quotedValue(m, 5)
			}
			return creds
		}
	}
	if m := genericMysqli.FindSubmatch(masked); m != nil {
		creds := &hostdb.Credentials{
			User:     quotedValue(m, 3),
			Password: quotedValue(m, 5),
			Method:   "config-parse",
		}
		applyHost(creds, quotedValue(m, 1))
		if m[7] != nil || m[8] != nil {
			creds.Name = quotedValue(m, 7)
		}
		if creds.Name == "" {
			creds.Name = genericSelectedDB(masked)
		}
		if creds.Name == "" {
			return nil
		}
		return creds
	}
	if m := genericMysqlConnect.FindSubmatch(masked); m != nil {
		creds := &hostdb.Credentials{
			User:     quotedValue(m, 3),
			Password: quotedValue(m, 5),
			Name:     genericSelectedDB(masked),
			Method:   "config-parse",
		}
		applyHost(creds, quotedValue(m, 1))
		if creds.Name == "" {
			return nil
		}
		return creds
	}
	return nil
}

func genericSelectedDB(masked []byte) string {
	if m := genericSelectDB.FindSubmatch(masked); m != nil {
		return quotedValue(m, 1)
	}
	return ""
}

// parsePDODSN reads a PDO connection string: mysql:host=h;port=n;dbname=d.
func parsePDODSN(dsn string) *hostdb.Credentials {
	scheme, rest, ok := strings.Cut(dsn, ":")
	if !ok {
		return nil
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme != "mysql" && scheme != "pgsql" {
		return nil
	}
	creds := &hostdb.Credentials{Method: "config-parse"}
	if scheme == "pgsql" {
		creds.Driver = hostdb.DriverPostgres
	}
	for _, part := range strings.Split(rest, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "dbname":
			creds.Name = value
		case "host":
			creds.Host = value
		case "user":
			creds.User = value
		case "password":
			creds.Password = value
		case "port":
			if port, err := strconv.Atoi(value); err == nil {
				creds.Port = port
			}
		}
	}
	if creds.Name == "" {
		return nil
	}
	return creds
}

// detectDBAPI names the database extension the file actually calls, so the
// check gate can require the one this site needs instead of guessing. The
// order matters: "mysqli" contains "mysql", and a PDO DSN mentions mysql too.
func detectDBAPI(masked []byte) string {
	s := string(masked)
	switch {
	case strings.Contains(s, "pg_connect") || containsFold(s, "pgsql:"):
		return "pgsql"
	case containsFold(s, "new pdo") || containsFold(s, "mysql:host"):
		return "pdo_mysql"
	case strings.Contains(s, "mysqli"):
		return "mysqli"
	case strings.Contains(s, "mysql_connect") || strings.Contains(s, "mysql_query"):
		return "mysql" // removed in PHP 7 — the site needs rewriting, not just moving
	default:
		return ""
	}
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), substr)
}
