package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/placeholder/rehost/internal/ssh"
)

// Inspection is what could be learned about one site's database from the
// source host. Connected=false with Reason set is an honest failure; the
// stats fields are best-effort and zero when unknown.
type Inspection struct {
	Connected     bool   `json:"connected"`
	Reason        string `json:"reason,omitempty"` // why not connected; never contains the password
	ServerVersion string `json:"server_version,omitempty"`
	Charset       string `json:"charset,omitempty"` // database default charset
	Tables        int    `json:"tables,omitempty"`
	SizeKB        int64  `json:"size_kb,omitempty"`
	UTF8MB4Tables int    `json:"utf8mb4_tables,omitempty"`
}

// inspectSQL learns everything in one round trip. Each row is label-first so
// parsing does not depend on statement order or optional rows.
const inspectSQL = `SELECT 'version', VERSION();
SELECT 'charset', DEFAULT_CHARACTER_SET_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = DATABASE();
SELECT 'tables', COUNT(*), COALESCE(ROUND(SUM(data_length+index_length)/1024),0) FROM information_schema.TABLES WHERE table_schema = DATABASE();
SELECT 'utf8mb4', COUNT(*) FROM information_schema.TABLES WHERE table_schema = DATABASE() AND TABLE_COLLATION LIKE 'utf8mb4%';`

// Inspect connects to a site's database with its extracted credentials and
// gathers version, charset, table count and size. The password travels to
// the mysql client via a defaults file on stdin (heredoc), so it never
// appears in the remote argv or environment. An error return is a transport
// failure; mysql-level failures come back as Connected=false.
func Inspect(ctx context.Context, r Runner, creds *Credentials) (*Inspection, error) {
	cmd := "mysql --defaults-extra-file=/dev/stdin --batch --skip-column-names --connect-timeout=10 -e " +
		ssh.ShellQuote(inspectSQL) + " " + ssh.ShellQuote(creds.Name) +
		" <<'REHOST_CNF'\n" + clientDefaults(creds) + "REHOST_CNF"
	res, err := r.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return &Inspection{Connected: false, Reason: sanitizeReason(res.Stderr, creds.Password)}, nil
	}
	insp := parseInspection(res.Stdout)
	insp.Connected = true
	return insp, nil
}

// clientDefaults renders the [client] section for --defaults-extra-file.
// Values are double-quoted with backslash escaping, the .cnf rule.
func clientDefaults(creds *Credentials) string {
	var b strings.Builder
	b.WriteString("[client]\n")
	host, socket := splitSocket(creds.Host)
	if host != "" {
		fmt.Fprintf(&b, "host=%s\n", cnfQuote(host))
	}
	if socket != "" {
		fmt.Fprintf(&b, "socket=%s\n", cnfQuote(socket))
	}
	if creds.Port != 0 {
		fmt.Fprintf(&b, "port=%d\n", creds.Port)
	}
	if creds.User != "" {
		fmt.Fprintf(&b, "user=%s\n", cnfQuote(creds.User))
	}
	fmt.Fprintf(&b, "password=%s\n", cnfQuote(creds.Password))
	return b.String()
}

// splitSocket separates MySQL's "localhost:/path/to/mysql.sock" convention.
func splitSocket(host string) (hostname, socket string) {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		if suffix := host[i+1:]; strings.HasPrefix(suffix, "/") {
			return host[:i], suffix
		}
	}
	return host, ""
}

// cnfQuote double-quotes a value for a MySQL options file.
func cnfQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// parseInspection reads the label-first batch rows of inspectSQL.
func parseInspection(stdout string) *Inspection {
	insp := &Inspection{}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch {
		case len(fields) >= 2 && fields[0] == "version":
			insp.ServerVersion = fields[1]
		case len(fields) >= 2 && fields[0] == "charset":
			insp.Charset = fields[1]
		case len(fields) >= 3 && fields[0] == "tables":
			insp.Tables, _ = strconv.Atoi(fields[1])
			insp.SizeKB, _ = strconv.ParseInt(fields[2], 10, 64)
		case len(fields) >= 2 && fields[0] == "utf8mb4":
			insp.UTF8MB4Tables, _ = strconv.Atoi(fields[1])
		}
	}
	return insp
}

// sanitizeReason keeps the first useful stderr line and hard-strips the
// password should a future mysql build ever echo it.
func sanitizeReason(stderr, password string) string {
	reason := strings.TrimSpace(stderr)
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	if password != "" {
		reason = strings.ReplaceAll(reason, password, "********")
	}
	if reason == "" {
		reason = "mysql exited non-zero without a message"
	}
	return reason
}
