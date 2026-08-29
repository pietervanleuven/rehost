package recipe

import (
	"strings"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// Requirements is what a detected install needs from the destination host.
// The check gate compares these against the destination's capabilities.
// Extension names match `php -m` output (case-insensitive).
type Requirements struct {
	// MinPHP is the minimum PHP version the framework runs on; empty means
	// PHP is not needed at all.
	MinPHP string
	// RequiredExt lists PHP extensions without which the site cannot run
	// (missing one is a blocker).
	RequiredExt []string
	// RecommendedExt lists extensions the framework degrades without
	// (missing one is a warning).
	RecommendedExt []string
	// NeedsDB reports whether the framework stores content in MySQL/MariaDB.
	NeedsDB bool
}

// RequirementsFor returns the destination requirements of one detected
// install. Unknown frameworks and static sites need nothing.
func RequirementsFor(in detect.Install) Requirements {
	switch in.Framework {
	case "wordpress":
		return Requirements{
			MinPHP:         "7.2",
			RequiredExt:    []string{"mysqli"},
			RecommendedExt: []string{"curl", "gd", "mbstring", "openssl", "zip"},
			NeedsDB:        true,
		}
	case "drupal":
		return Requirements{
			MinPHP:         drupalMinPHP(in.Version),
			RequiredExt:    []string{"pdo_mysql", "gd", "xml"},
			RecommendedExt: []string{"curl", "mbstring", "openssl", "zip"},
			NeedsDB:        true,
		}
	case "joomla":
		return Requirements{
			MinPHP:         joomlaMinPHP(in.Version),
			RequiredExt:    dbDriverExt(in, "mysqli", "pgsql"),
			RecommendedExt: []string{"curl", "gd", "mbstring", "zip"},
			NeedsDB:        true,
		}
	case "prestashop":
		return Requirements{
			MinPHP:         prestashopMinPHP(in.Version),
			RequiredExt:    []string{"pdo_mysql", "gd", "intl", "zip"},
			RecommendedExt: []string{"curl", "mbstring", "openssl"},
			NeedsDB:        true,
		}
	case "craft":
		return Requirements{
			MinPHP:         craftMinPHP(in.Version),
			RequiredExt:    dbDriverExt(in, "pdo_mysql", "pdo_pgsql"),
			RecommendedExt: []string{"curl", "gd", "intl", "mbstring", "openssl", "zip"},
			NeedsDB:        true,
		}
	default:
		return Requirements{}
	}
}

// dbDriverExt picks the database extension requirement from the driver the
// detection recorded (Extra["db_driver"]): frameworks like Joomla and Craft
// run on MySQL/MariaDB or PostgreSQL, and demanding mysqli of a PostgreSQL
// site would false-block it.
func dbDriverExt(in detect.Install, mysqlExt, pgExt string) []string {
	if db.NormalizeDriver(in.Extra["db_driver"]) == db.DriverPostgres {
		return []string{pgExt}
	}
	return []string{mysqlExt}
}

// drupalMinPHP maps a Drupal core version to its minimum PHP version.
// Unknown versions get Drupal 8's floor: conservative enough to flag ancient
// destination PHP without false-blocking a modern one.
func drupalMinPHP(version string) string {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}
	switch major {
	case "7":
		return "5.6"
	case "8", "9":
		return "7.3"
	case "10":
		return "8.1"
	case "11":
		return "8.3"
	default:
		return "7.3"
	}
}
