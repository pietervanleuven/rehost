package recipe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pietervanleuven/rehost/internal/detect"
)

// tree writes a set of files (relative POSIX paths → contents) under a fresh
// temp dir and returns an FS rooted there.
func tree(t *testing.T, files map[string]string) (detect.FS, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return detect.NewDirFS(root), root
}

func detectAt(t *testing.T, r detect.Recipe, fs detect.FS, dir string) *detect.Install {
	t.Helper()
	got, err := r.Detect(context.Background(), fs, dir)
	if err != nil {
		t.Fatalf("%s.Detect: %v", r.Name(), err)
	}
	return got
}

func TestDrupalModern(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/core/lib/Drupal.php":        "class Drupal {\n  const VERSION = '10.3.1';\n}",
		"public_html/sites/default/settings.php": "<?php $databases = [];",
	})
	got := detectAt(t, Drupal{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected Drupal detection")
	}
	if got.Version != "10.3.1" {
		t.Errorf("Version = %q, want 10.3.1", got.Version)
	}
	if got.ConfigFile != "public_html/sites/default/settings.php" {
		t.Errorf("ConfigFile = %q", got.ConfigFile)
	}
	if len(got.Sites) != 1 || got.Sites[0] != "default" {
		t.Errorf("Sites = %v, want [default]", got.Sites)
	}
}

func TestDrupal7(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"www/misc/drupal.js":             "Drupal.behaviors = {};",
		"www/includes/bootstrap.inc":     "<?php\ndefine('VERSION', '7.98');",
		"www/sites/default/settings.php": "<?php $databases = [];",
	})
	got := detectAt(t, Drupal{}, fs, "www")
	if got == nil || got.Version != "7.98" {
		t.Fatalf("Drupal 7 detection wrong: %+v", got)
	}
}

func TestDrupalModernRejectsCoreDir(t *testing.T) {
	// A Drupal 8+ tree: the site root is "web"; "web/core" is the framework's
	// own core directory and must not be reported as a second install.
	fs, _ := tree(t, map[string]string{
		"web/index.php":                  "<?php",
		"web/core/lib/Drupal.php":        "const VERSION = '10.1.6';",
		"web/core/misc/drupal.js":        "Drupal.behaviors = {};",
		"web/sites/default/settings.php": "<?php",
	})
	if got := detectAt(t, Drupal{}, fs, "web"); got == nil || got.Version != "10.1.6" {
		t.Fatalf("site root should detect as Drupal 10.1.6, got %+v", got)
	}
	if got := detectAt(t, Drupal{}, fs, "web/core"); got != nil {
		t.Errorf("web/core is the framework's core dir, not a site: got %+v", got)
	}
}

func TestDiscoverDrupal8Once(t *testing.T) {
	// End-to-end reproduction of the double-detection: discovery must yield a
	// single Drupal install, not one for the root and a phantom for core/.
	fs, _ := tree(t, map[string]string{
		"httpdocs/web/index.php":                  "<?php",
		"httpdocs/web/core/lib/Drupal.php":        "const VERSION = '10.1.6';",
		"httpdocs/web/core/misc/drupal.js":        "Drupal.behaviors = {};",
		"httpdocs/web/sites/default/settings.php": "<?php",
	})
	got, err := detect.Discover(context.Background(), fs, []string{"."}, All(),
		detect.FindOptions{Prune: detect.DefaultPrune})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Drupal 8 should be found once, got %d: %+v", len(got), got)
	}
	if got[0].Root != "httpdocs/web" || got[0].Version != "10.1.6" {
		t.Errorf("install = %+v, want root httpdocs/web version 10.1.6", got[0])
	}
}

func TestDrupalMultisite(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/core/lib/Drupal.php":            "const VERSION = '9.5.0';",
		"public_html/sites/default/settings.php":     "<?php",
		"public_html/sites/example.com/settings.php": "<?php",
		"public_html/sites/all/modules/x.module":     "<?php", // 'all' is shared, not a site
	})
	got := detectAt(t, Drupal{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected Drupal detection")
	}
	if len(got.Sites) != 2 {
		t.Fatalf("Sites = %v, want default + example.com", got.Sites)
	}
	// ConfigFile must prefer default even when another site sorts first.
	if got.ConfigFile != "public_html/sites/default/settings.php" {
		t.Errorf("ConfigFile = %q, want the default site", got.ConfigFile)
	}
}

func TestWordPressInDocroot(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/wp-includes/version.php": "<?php $wp_version = '6.5.2';",
		"public_html/wp-config.php":           "<?php\n$table_prefix = 'wp_';",
	})
	got := detectAt(t, WordPress{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected WordPress detection")
	}
	if got.Version != "6.5.2" {
		t.Errorf("Version = %q, want 6.5.2", got.Version)
	}
	if got.ConfigFile != "public_html/wp-config.php" {
		t.Errorf("ConfigFile = %q", got.ConfigFile)
	}
	if got.Extra["table_prefix"] != "wp_" {
		t.Errorf("table_prefix = %q, want wp_", got.Extra["table_prefix"])
	}
}

func TestWordPressConfigAboveDocroot(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/wp-includes/version.php": "<?php $wp_version = '6.4';",
		"wp-config.php":                       `<?php $table_prefix = "wp_";`, // one level up
	})
	got := detectAt(t, WordPress{}, fs, "public_html")
	if got == nil {
		t.Fatal("expected WordPress detection")
	}
	if got.ConfigFile != "wp-config.php" {
		t.Errorf("ConfigFile = %q, want the parent-level wp-config.php", got.ConfigFile)
	}
	if got.Extra["table_prefix"] != "wp_" {
		t.Errorf("table_prefix = %q", got.Extra["table_prefix"])
	}
}

func TestWordPressNestedDoesNotBorrowParentConfig(t *testing.T) {
	// A blog nested inside another WordPress site, with no wp-config of its own.
	fs, _ := tree(t, map[string]string{
		"httpdocs/wp-includes/version.php":      "<?php $wp_version = '6.5';",
		"httpdocs/wp-config.php":                "<?php $table_prefix = 'wp_';",
		"httpdocs/blog/wp-includes/version.php": "<?php $wp_version = '6.4';",
	})
	got := detectAt(t, WordPress{}, fs, "httpdocs/blog")
	if got == nil {
		t.Fatal("nested WordPress should still be detected")
	}
	if got.ConfigFile != "" {
		t.Errorf("nested site must not borrow the parent site's wp-config, got %q", got.ConfigFile)
	}
}

func TestStaticMatchesIndexOnly(t *testing.T) {
	withIndex, _ := tree(t, map[string]string{"public_html/index.html": "<h1>hi</h1>"})
	if got := detectAt(t, Static{}, withIndex, "public_html"); got == nil {
		t.Error("static site with index.html should be detected")
	}

	bare, _ := tree(t, map[string]string{"public_html/notes.txt": "just files"})
	if got := detectAt(t, Static{}, bare, "public_html"); got != nil {
		t.Errorf("bare directory must not be reported as a static site, got %+v", got)
	}
}

func TestRecipesDoNotFalsePositive(t *testing.T) {
	fs, _ := tree(t, map[string]string{"public_html/readme.txt": "empty account"})
	for _, r := range []detect.Recipe{Drupal{}, WordPress{}} {
		if got := detectAt(t, r, fs, "public_html"); got != nil {
			t.Errorf("%s should not match a non-framework dir, got %+v", r.Name(), got)
		}
	}
}

func TestScanWithAllRecipes(t *testing.T) {
	fs, _ := tree(t, map[string]string{
		"public_html/wp-includes/version.php": "<?php $wp_version = '6.5';",
		"public_html/wp-config.php":           "<?php $table_prefix = 'wp_';",
		"www/index.html":                      "<h1>static</h1>",
	})
	got, err := detect.Scan(context.Background(), fs, detect.DocrootCandidates(""), All())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 installs (wordpress + static), got %d: %+v", len(got), got)
	}
	// Sorted by framework: static, then wordpress.
	if got[0].Framework != "static" || got[1].Framework != "wordpress" {
		t.Errorf("frameworks = [%s %s]", got[0].Framework, got[1].Framework)
	}
	if got[1].Root != "public_html" {
		t.Errorf("wordpress root = %q, want public_html", got[1].Root)
	}
}
