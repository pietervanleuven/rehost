package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// dumpFileName maps a database name to a safe file name inside the local
// dumps directory. The name is remote-controlled — extracted from config
// files on the source host — so path bytes in it must never reach
// filepath.Join: a hostile "../../.ssh/authorized_keys" would otherwise be
// truncate-created on the operator's machine. A name that is already safe
// keeps its readable form; anything else keeps its safe bytes plus a short
// hash of the original so distinct names cannot collide after sanitizing.
func dumpFileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
	if safe == name && safe != "" && safe != "." && safe != ".." {
		return safe + ".sql.gz"
	}
	sum := sha256.Sum256([]byte(name))
	safe = strings.Trim(safe, ".")
	if safe == "" {
		safe = "db"
	}
	return safe + "-" + hex.EncodeToString(sum[:4]) + ".sql.gz"
}
