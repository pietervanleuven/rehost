package recipe

import (
	"strings"

	hostdb "github.com/pietervanleuven/go-hostdb"
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
	case "laravel":
		return Requirements{
			MinPHP:         laravelMinPHP(in.Version),
			RequiredExt:    laravelDBExt(in),
			RecommendedExt: []string{"ctype", "curl", "fileinfo", "mbstring", "openssl", "tokenizer", "xml"},
			// A sqlite app has no server database: the .sqlite file lives
			// under the project root and travels with the file sync.
			NeedsDB: in.Extra["db_driver"] != "sqlite",
		}
	case "generic-php":
		return Requirements{
			// A hand-rolled app declares no PHP requirement anywhere, so
			// there is nothing to read. 5.6 is a floor, not a guess at what
			// the app wants: it makes "the destination has no PHP at all"
			// a blocker without false-blocking a legacy app on a modern
			// host. The php.drift Info covers the real risk — a major-version
			// jump the app was never tested against.
			MinPHP:         "5.6",
			RequiredExt:    genericRequiredExt(in.Extra["db_api"]),
			RecommendedExt: genericRecommendedExt(in.Extra["db_api"]),
			NeedsDB:        true,
		}
	default:
		return Requirements{}
	}
}

// genericRequiredExt demands the database extension the config was seen
// calling. An unrecognized API requires nothing: blocking a migration on a
// guess would be worse than the warning genericRecommendedExt adds.
func genericRequiredExt(api string) []string {
	switch api {
	case "mysqli":
		return []string{"mysqli"}
	case "pdo_mysql":
		return []string{"pdo_mysql"}
	case "pgsql":
		return []string{"pgsql"}
	default:
		return nil
	}
}

// genericRecommendedExt warns rather than blocks. The "mysql" case is the
// removed mysql_* API: no modern destination has it, so requiring it would
// hard-block every realistic migration of a site that in truth needs its
// code updated — a warning naming the successor is the useful signal.
func genericRecommendedExt(api string) []string {
	switch api {
	case "mysql":
		return []string{"mysqli"}
	case "":
		return []string{"mysqli", "pdo_mysql"}
	default:
		return nil
	}
}

// dbDriverExt picks the database extension requirement from the driver the
// detection recorded (Extra["db_driver"]): frameworks like Joomla and Craft
// run on MySQL/MariaDB or PostgreSQL, and demanding mysqli of a PostgreSQL
// site would false-block it.
func dbDriverExt(in detect.Install, mysqlExt, pgExt string) []string {
	if hostdb.NormalizeDriver(in.Extra["db_driver"]) == hostdb.DriverPostgres {
		return []string{pgExt}
	}
	return []string{mysqlExt}
}

// laravelDBExt handles Laravel's extra connection: sqlite must demand
// pdo_sqlite, not be folded into MySQL by NormalizeDriver.
func laravelDBExt(in detect.Install) []string {
	if in.Extra["db_driver"] == "sqlite" {
		return []string{"pdo_sqlite"}
	}
	return dbDriverExt(in, "pdo_mysql", "pdo_pgsql")
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
