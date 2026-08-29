package recipe

import (
	"context"
	"path"
	"regexp"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// WordPress detects a WordPress install (single or multisite).
//
// The reliable core marker is wp-includes/version.php. wp-config.php may sit
// in the docroot or one level above it (a supported WordPress layout), so
// both are checked. Version and table prefix come from those files.
type WordPress struct{}

func (WordPress) Name() string { return "wordpress" }

// Markers fingerprint a WordPress root by its core version file.
func (WordPress) Markers() []string {
	return []string{"wp-includes/version.php"}
}

var (
	wpVersion     = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)
	wpTablePrefix = regexp.MustCompile(`\$table_prefix\s*=\s*['"]([^'"]+)['"]`)
	// MULTISITE=true marks a network install, which rehost refuses to
	// half-migrate (the check gate blocks on it).
	wpMultisite = regexp.MustCompile(`define\(\s*['"]MULTISITE['"]\s*,\s*(?i:true)\s*\)`)
)

func (w WordPress) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	versionFile := path.Join(dir, "wp-includes", "version.php")
	ok, err := fs.Exists(ctx, versionFile)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	install := &detect.Install{Framework: w.Name(), Root: dir}
	if content, err := fs.ReadFile(ctx, versionFile); err == nil {
		install.Version = firstSubmatch(wpVersion, content)
	}

	// wp-config.php: docroot first, then one directory up.
	configFile, err := w.findConfig(ctx, fs, dir)
	if err != nil {
		return nil, err
	}
	if configFile != "" {
		install.ConfigFile = configFile
		if content, err := fs.ReadFile(ctx, configFile); err == nil {
			masked := maskPHPComments(content)
			if prefix := firstSubmatch(wpTablePrefix, masked); prefix != "" {
				install.Extra = map[string]string{"table_prefix": prefix}
			}
			if wpMultisite.Match(masked) {
				if install.Extra == nil {
					install.Extra = map[string]string{}
				}
				install.Extra["multisite"] = "true"
			}
		}
	}
	return install, nil
}

func (WordPress) findConfig(ctx context.Context, fs detect.FS, dir string) (string, error) {
	inRoot := path.Join(dir, "wp-config.php")
	ok, err := fs.Exists(ctx, inRoot)
	if err != nil {
		return "", err
	}
	if ok {
		return inRoot, nil
	}
	// WordPress reads wp-config.php from the parent when absent from the
	// docroot. But if the parent is itself a WordPress root (a nested install),
	// that config belongs to the parent site, not this one — don't borrow it.
	parent := path.Dir(dir)
	parentIsWP, err := fs.Exists(ctx, path.Join(parent, "wp-includes", "version.php"))
	if err != nil {
		return "", err
	}
	if parentIsWP {
		return "", nil
	}
	aboveRoot := path.Join(parent, "wp-config.php")
	ok, err = fs.Exists(ctx, aboveRoot)
	if err != nil {
		return "", err
	}
	if ok {
		return aboveRoot, nil
	}
	return "", nil
}
