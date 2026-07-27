package recipe

import (
	"context"
	"path"
	"regexp"

	"github.com/placeholder/rehost/internal/detect"
)

// Drupal detects Drupal 7 and 8+ (including multisite).
//
// Markers (PLAN §3): core/lib/Drupal.php (8+), misc/drupal.js (7),
// sites/*/settings.php. Version comes from core/lib/Drupal.php (8+) or
// includes/bootstrap.inc (7).
type Drupal struct{}

func (Drupal) Name() string { return "drupal" }

// Markers are the root-level fingerprints: core/lib/Drupal.php for 8+ and
// misc/drupal.js for 7. settings.php is deliberately excluded — it appears
// per subsite and would over-report multisite installs.
func (Drupal) Markers() []string {
	return []string{"core/lib/Drupal.php", "misc/drupal.js"}
}

var (
	drupalModernVersion = regexp.MustCompile(`const\s+VERSION\s*=\s*'([^']+)'`)
	drupal7Version      = regexp.MustCompile(`define\(\s*'VERSION'\s*,\s*'([^']+)'\s*\)`)
)

func (d Drupal) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	modernCore := path.Join(dir, "core", "lib", "Drupal.php")
	legacyMarker := path.Join(dir, "misc", "drupal.js")

	isModern, err := fs.Exists(ctx, modernCore)
	if err != nil {
		return nil, err
	}
	isLegacy := false
	if !isModern {
		if isLegacy, err = fs.Exists(ctx, legacyMarker); err != nil {
			return nil, err
		}
	}
	if !isModern && !isLegacy {
		return nil, nil
	}

	install := &detect.Install{Framework: d.Name(), Root: dir}

	if isModern {
		if content, err := fs.ReadFile(ctx, modernCore); err == nil {
			install.Version = firstSubmatch(drupalModernVersion, content)
		}
	} else {
		bootstrap := path.Join(dir, "includes", "bootstrap.inc")
		if content, err := fs.ReadFile(ctx, bootstrap); err == nil {
			install.Version = firstSubmatch(drupal7Version, content)
		}
	}

	if err := d.findSites(ctx, fs, dir, install); err != nil {
		return nil, err
	}
	return install, nil
}

// findSites populates ConfigFile and Sites by walking sites/, treating any
// subdirectory (other than the shared "all") that holds a settings.php as a
// configured site. "default" is the standard single site; more than that
// means multisite.
func (Drupal) findSites(ctx context.Context, fs detect.FS, dir string, install *detect.Install) error {
	sitesDir := path.Join(dir, "sites")
	ok, err := fs.IsDir(ctx, sitesDir)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	entries, err := fs.List(ctx, sitesDir)
	if err != nil {
		return err
	}

	for _, name := range entries {
		if name == "all" {
			continue
		}
		siteDir := path.Join(sitesDir, name)
		isDir, err := fs.IsDir(ctx, siteDir)
		if err != nil {
			return err
		}
		if !isDir {
			continue
		}
		settings := path.Join(siteDir, "settings.php")
		hasSettings, err := fs.Exists(ctx, settings)
		if err != nil {
			return err
		}
		if !hasSettings {
			continue
		}
		install.Sites = append(install.Sites, name)
		if name == "default" || install.ConfigFile == "" {
			install.ConfigFile = settings
		}
	}
	return nil
}
