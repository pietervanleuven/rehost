package recipe

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// Joomla fingerprints a Joomla site by its core version class. Credentials
// and the database driver live in configuration.php as public properties of
// the JConfig class; the site can run MySQL/MariaDB or PostgreSQL, so the
// detected dbtype is recorded for the driver-aware rules downstream.
type Joomla struct{}

func (Joomla) Name() string { return "joomla" }

// Markers cover Joomla 3.8+ / 4 / 5 (libraries/src) and 3.0–3.7
// (libraries/cms); anything older is out of scope.
func (Joomla) Markers() []string {
	return []string{"libraries/src/Version.php", "libraries/cms/version/version.php"}
}

var (
	// Modern Version.php constants: `public const MAJOR_VERSION = 4;` (the
	// `public` is absent on Joomla 3.8–3.10).
	joomlaMajor = regexp.MustCompile(`(?:public\s+)?const\s+MAJOR_VERSION\s*=\s*(\d+)`)
	joomlaMinor = regexp.MustCompile(`(?:public\s+)?const\s+MINOR_VERSION\s*=\s*(\d+)`)
	joomlaPatch = regexp.MustCompile(`(?:public\s+)?const\s+PATCH_VERSION\s*=\s*(\d+)`)
	// Older style: `const RELEASE = '3.7';` + `const DEV_LEVEL = '5';`, or
	// the same as `public $RELEASE = …` instance properties before that.
	joomlaRelease  = regexp.MustCompile(`(?:const\s+RELEASE|\$RELEASE)\s*=\s*'([0-9.]+)'`)
	joomlaDevLevel = regexp.MustCompile(`(?:const\s+DEV_LEVEL|\$DEV_LEVEL)\s*=\s*'(\d+)'`)
)

func (j Joomla) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	var versionFile string
	for _, marker := range j.Markers() {
		p := path.Join(dir, marker)
		ok, err := fs.Exists(ctx, p)
		if err != nil {
			return nil, err
		}
		if ok {
			versionFile = p
			break
		}
	}
	if versionFile == "" {
		return nil, nil
	}

	install := &detect.Install{Framework: j.Name(), Root: dir}
	if content, err := fs.ReadFile(ctx, versionFile); err == nil {
		install.Version = joomlaVersion(content)
	}

	config := path.Join(dir, "configuration.php")
	ok, err := fs.Exists(ctx, config)
	if err != nil {
		return nil, err
	}
	if ok {
		install.ConfigFile = config
		if content, err := fs.ReadFile(ctx, config); err == nil {
			masked := maskPHPComments(content)
			extra := map[string]string{}
			if dbtype := joomlaProperty(masked, "dbtype"); dbtype != "" {
				extra["db_driver"] = dbtype
			}
			if prefix := joomlaProperty(masked, "dbprefix"); prefix != "" {
				extra["table_prefix"] = prefix
			}
			if len(extra) > 0 {
				install.Extra = extra
			}
		}
	}
	return install, nil
}

// joomlaVersion composes the version from whichever constant style the core
// file uses; empty when neither is recognizable.
func joomlaVersion(content []byte) string {
	if major := firstSubmatch(joomlaMajor, content); major != "" {
		v := major
		if minor := firstSubmatch(joomlaMinor, content); minor != "" {
			v += "." + minor
			if patch := firstSubmatch(joomlaPatch, content); patch != "" {
				v += "." + patch
			}
		}
		return v
	}
	if release := firstSubmatch(joomlaRelease, content); release != "" {
		if dev := firstSubmatch(joomlaDevLevel, content); dev != "" {
			return release + "." + dev
		}
		return release
	}
	return ""
}

// joomlaProperty matches `public $key = 'value';` (or var/protected, either
// quote style, escape-aware) in comment-masked configuration.php source.
func joomlaProperty(masked []byte, key string) string {
	re := regexp.MustCompile(`(?:public|var|protected)\s+\$` + regexp.QuoteMeta(key) +
		`\s*=\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")`)
	if m := re.FindSubmatch(masked); m != nil {
		return quotedValue(m, 1)
	}
	return ""
}

// joomlaMinPHP maps a Joomla major version to its minimum PHP.
func joomlaMinPHP(version string) string {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}
	switch n, _ := strconv.Atoi(major); {
	case n >= 5:
		return "8.1"
	case n == 4:
		return "7.2"
	case n == 3:
		return "5.3"
	default:
		return "7.2"
	}
}
