package recipe

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// PrestaShop fingerprints a shop by its config/defines.inc.php, which both
// generations keep. Credentials moved between generations: 1.6 defines
// _DB_*_ constants in config/settings.inc.php, 1.7/8 returns a parameters
// array from app/config/parameters.php — the recipe handles both shapes.
// PrestaShop is MySQL-only.
type PrestaShop struct{}

func (PrestaShop) Name() string { return "prestashop" }

func (PrestaShop) Markers() []string { return []string{"config/defines.inc.php"} }

var (
	// 1.7/8: `const VERSION = '8.1.4';` in app/AppKernel.php.
	psKernelVersion = regexp.MustCompile(`const\s+VERSION\s*=\s*'([0-9.]+)'`)
	// 1.6: `define('_PS_VERSION_', '1.6.1.24');` in config/settings.inc.php.
	psDefineVersion = regexp.MustCompile(`define\(\s*'_PS_VERSION_'\s*,\s*'([0-9.]+)'\s*\)`)
)

func (p PrestaShop) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	ok, err := fs.Exists(ctx, path.Join(dir, "config", "defines.inc.php"))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	install := &detect.Install{Framework: p.Name(), Root: dir}

	// Version, richest source first.
	if content, err := fs.ReadFile(ctx, path.Join(dir, "app", "AppKernel.php")); err == nil {
		install.Version = firstSubmatch(psKernelVersion, content)
	}

	// Config file per generation; 1.7/8 keeps an empty settings.inc.php shim
	// around, so parameters.php wins when both exist.
	modern := path.Join(dir, "app", "config", "parameters.php")
	legacy := path.Join(dir, "config", "settings.inc.php")
	if ok, err := fs.Exists(ctx, modern); err != nil {
		return nil, err
	} else if ok {
		install.ConfigFile = modern
	} else if ok, err := fs.Exists(ctx, legacy); err != nil {
		return nil, err
	} else if ok {
		install.ConfigFile = legacy
	}

	if install.ConfigFile != "" {
		if content, err := fs.ReadFile(ctx, install.ConfigFile); err == nil {
			masked := maskPHPComments(content)
			if install.Version == "" {
				install.Version = firstSubmatch(psDefineVersion, masked)
			}
			prefix := firstConfigValue(content, "database_prefix")
			if prefix == "" {
				prefix = wpDefine(masked, "_DB_PREFIX_")
			}
			if prefix != "" {
				install.Extra = map[string]string{"table_prefix": prefix}
			}
		}
	}
	return install, nil
}

// prestashopMinPHP maps a PrestaShop version to its minimum PHP: 8.x needs
// 7.2.5, 1.7 needs 5.6, 1.6 ran on 5.2.
func prestashopMinPHP(version string) string {
	switch {
	case version == "":
		return "5.6"
	case strings.HasPrefix(version, "1.6"), strings.HasPrefix(version, "1.5"):
		return "5.2"
	case strings.HasPrefix(version, "1."):
		return "5.6"
	default: // 8+, 9+
		return "7.2"
	}
}
