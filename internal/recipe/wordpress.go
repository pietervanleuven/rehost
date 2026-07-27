package recipe

import (
	"context"
	"path"
	"regexp"

	"github.com/placeholder/rehost/internal/detect"
)

// WordPress detects a WordPress install (single or multisite).
//
// The reliable core marker is wp-includes/version.php. wp-config.php may sit
// in the docroot or one level above it (a supported WordPress layout), so
// both are checked. Version and table prefix come from those files.
type WordPress struct{}

func (WordPress) Name() string { return "wordpress" }

var (
	wpVersion     = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)
	wpTablePrefix = regexp.MustCompile(`\$table_prefix\s*=\s*['"]([^'"]+)['"]`)
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
			if prefix := firstSubmatch(wpTablePrefix, content); prefix != "" {
				install.Extra = map[string]string{"table_prefix": prefix}
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
	// docroot and no wp-settings.php sits beside it there.
	aboveRoot := path.Join(path.Dir(dir), "wp-config.php")
	ok, err = fs.Exists(ctx, aboveRoot)
	if err != nil {
		return "", err
	}
	if ok {
		return aboveRoot, nil
	}
	return "", nil
}
