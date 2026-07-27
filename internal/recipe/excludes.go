package recipe

import "github.com/placeholder/rehost/internal/detect"

// genericExcludes never belong in a migration regardless of framework.
var genericExcludes = []string{".git", "node_modules"}

// ExcludeSuggestionsFor returns root-relative directories that are safe to
// leave behind when they exist: caches and backup dumps the framework (or
// its ecosystem plugins) regenerates or re-creates. The inventory measures
// which of them actually exist and what they weigh.
func ExcludeSuggestionsFor(in detect.Install) []string {
	switch in.Framework {
	case "wordpress":
		return append([]string{
			"wp-content/cache",
			"wp-content/uploads/cache",
			"wp-content/w3tc-cache",
			"wp-content/updraft",
			"wp-content/ai1wm-backups",
			"wp-content/backups-dup-pro",
		}, genericExcludes...)
	case "drupal":
		return append([]string{
			// css/js/php are compiled artifacts, styles are regenerable
			// image derivatives — Drupal rebuilds all of them.
			"sites/default/files/css",
			"sites/default/files/js",
			"sites/default/files/php",
			"sites/default/files/styles",
		}, genericExcludes...)
	default:
		return genericExcludes
	}
}
