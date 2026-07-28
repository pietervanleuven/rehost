package recipe

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/db"
)

// credSentinel prefixes the JSON payload the PHP echo-helpers print, so
// config files that produce their own output cannot confuse the parser.
const credSentinel = "::REHOST-DB::"

// ExtractorFor returns the credential extractor of a framework's recipe, or
// nil when the framework has none (static) or is unknown.
func ExtractorFor(framework string) db.Extractor {
	for _, r := range All() {
		if r.Name() != framework {
			continue
		}
		if e, ok := r.(db.Extractor); ok {
			return e
		}
	}
	return nil
}

// decodeFirstJSON unmarshals the first JSON value found in s into v,
// tolerating CLI banners before it and any output after it.
func decodeFirstJSON(s string, v any) error {
	i := strings.IndexAny(s, "[{")
	if i < 0 {
		return errors.New("no JSON in output")
	}
	return json.NewDecoder(strings.NewReader(s[i:])).Decode(v)
}

// phpCredPayload is the shape both PHP echo-helpers print.
type phpCredPayload struct {
	Driver   string `json:"driver"`
	Name     string `json:"name"`
	User     string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     any    `json:"port"` // frameworks configure it as string or number
	Prefix   string `json:"prefix"`
}

// parseSentinelCreds extracts the credentials JSON a PHP helper printed
// after credSentinel, or nil if the sentinel or a database name is absent.
func parseSentinelCreds(stdout, method string) *db.Credentials {
	i := strings.Index(stdout, credSentinel)
	if i < 0 {
		return nil
	}
	var p phpCredPayload
	if err := decodeFirstJSON(stdout[i+len(credSentinel):], &p); err != nil || p.Name == "" {
		return nil
	}
	creds := &db.Credentials{
		Driver:      p.Driver,
		Name:        p.Name,
		User:        p.User,
		Password:    p.Password,
		TablePrefix: p.Prefix,
		Port:        toPort(p.Port),
		Method:      method,
	}
	applyHost(creds, p.Host)
	return creds
}

// applyHost stores a configured DB host, splitting a ":port" suffix into
// Port. A non-numeric suffix (a socket path like "localhost:/tmp/mysql.sock")
// stays in Host untouched.
func applyHost(creds *db.Credentials, host string) {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if port, err := strconv.Atoi(host[i+1:]); err == nil && port > 0 && port < 65536 {
			creds.Host, creds.Port = host[:i], port
			return
		}
	}
	creds.Host = host
}

// toPort coerces a JSON port value (number, numeric string, or absent).
func toPort(v any) int {
	switch p := v.(type) {
	case float64:
		return int(p)
	case string:
		n, _ := strconv.Atoi(p)
		return n
	default:
		return 0
	}
}

// firstConfigValue matches `'key' => 'value'` with either quote style, the
// syntax of Drupal's $databases entries.
func firstConfigValue(content []byte, key string) string {
	re := regexp.MustCompile(`['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*(?:'([^']*)'|"([^"]*)")`)
	if m := re.FindSubmatch(content); m != nil {
		if m[1] != nil {
			return string(m[1])
		}
		return string(m[2])
	}
	return ""
}
