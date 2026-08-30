package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	hostdb "github.com/pietervanleuven/go-hostdb"
)

// credSentinel prefixes the JSON payload the PHP echo-helpers print, so
// config files that produce their own output cannot confuse the parser.
const credSentinel = "::REHOST-DB::"

// ExtractorFor returns the credential extractor of a framework's recipe, or
// nil when the framework has none (static) or is unknown.
func ExtractorFor(framework string) Extractor {
	for _, r := range All() {
		if r.Name() != framework {
			continue
		}
		if e, ok := r.(Extractor); ok {
			return e
		}
	}
	return nil
}

// credLayer is one rung of a credential-extraction ladder: skipped unless
// available, aborting on error (transport failures must not fall through to
// a weaker layer), returning on a non-nil result, falling through otherwise.
type credLayer struct {
	available bool
	extract   func(ctx context.Context) (*hostdb.Credentials, error)
}

// extractLayered walks the ladder every recipe's ExtractCredentials shares:
// framework CLI (authoritative) → PHP echo-helper → config-file parse.
// Keeping the fall-through contract here means a change to the layering
// rules cannot reach one framework and miss another.
func extractLayered(ctx context.Context, layers []credLayer) (*hostdb.Credentials, error) {
	for _, l := range layers {
		if !l.available {
			continue
		}
		creds, err := l.extract(ctx)
		if err != nil || creds != nil {
			return creds, err
		}
	}
	return nil, nil
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
func parseSentinelCreds(stdout, method string) *hostdb.Credentials {
	i := strings.Index(stdout, credSentinel)
	if i < 0 {
		return nil
	}
	var p phpCredPayload
	if err := decodeFirstJSON(stdout[i+len(credSentinel):], &p); err != nil || p.Name == "" {
		return nil
	}
	creds := &hostdb.Credentials{
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
// stays in Host untouched. IPv6 literals are only split in the bracketed
// [addr]:port form — a bare "::1" must never be read as host ":" port 1.
func applyHost(creds *hostdb.Credentials, host string) {
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i >= 0 {
			addr, rest := host[1:i], host[i+1:]
			if port, ok := strings.CutPrefix(rest, ":"); ok {
				if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
					creds.Host, creds.Port = addr, p
					return
				}
			}
			if rest == "" {
				creds.Host = addr
				return
			}
		}
		creds.Host = host
		return
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && strings.IndexByte(host, ':') == i {
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
// syntax of Drupal's $databases entries. The literal pattern is escape-aware
// so a value like 'it\'s' is captured whole and decoded, not cut at the
// escaped quote.
func firstConfigValue(content []byte, key string) string {
	re := regexp.MustCompile(`['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")`)
	// Match against a comment-masked copy so the commented @code example in a
	// stock settings.php is not read as real credentials. String-literal bytes
	// survive masking, so the captured value equals the original.
	if m := re.FindSubmatch(maskPHPComments(content)); m != nil {
		return quotedValue(m, 1)
	}
	return ""
}
