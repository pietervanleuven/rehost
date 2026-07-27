// Package recipe holds the framework detection recipes that plug into the
// detect engine. Drupal and WordPress are built in lockstep (Tier 1) so the
// engine never becomes WordPress-shaped; static validates the pipeline end.
//
// Later phases extend recipes with credential extraction and migration steps;
// this package currently implements detection only.
package recipe

import (
	"regexp"

	"github.com/placeholder/rehost/internal/detect"
)

// All returns the default recipe set in Scan order: specific frameworks
// first, the generic static fallback last.
func All() []detect.Recipe {
	return []detect.Recipe{
		Drupal{},
		WordPress{},
		Static{},
	}
}

// firstSubmatch returns the first capture group of re in content, or "".
func firstSubmatch(re *regexp.Regexp, content []byte) string {
	if m := re.FindSubmatch(content); m != nil {
		return string(m[1])
	}
	return ""
}
