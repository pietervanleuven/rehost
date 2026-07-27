package db

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/placeholder/rehost/internal/ssh"
)

// phpDumpScript dumps a MySQL database as gzipped SQL on stdout without
// mysqldump — the fallback for shared hosts that ship PHP but no client
// tools. It reads its connection config as one JSON line on stdin (so the
// password never enters argv or the environment), connects via mysqli or
// PDO (WordPress hosts have mysqli, Drupal hosts pdo_mysql), and writes a
// dump the Go verifier accepts: DROP + SHOW CREATE TABLE + batched INSERTs
// per base table (views are skipped), with the "-- Dump completed" footer
// only after every table succeeded. Strings are escaped by the driver,
// non-UTF-8 values become 0x-hex literals, NULL stays NULL. Written for
// `php -r`, so there is no opening <?php tag; backticks are built with
// chr(96) so the script can live in a Go raw string literal.
const phpDumpScript = `
$cfg = json_decode(stream_get_contents(STDIN), true);
if (!is_array($cfg) || !isset($cfg['name']) || $cfg['name'] === '') {
  fwrite(STDERR, "rehost: no database config on stdin\n");
  exit(1);
}
if (!function_exists('gzopen')) {
  fwrite(STDERR, "rehost: the PHP zlib extension is missing\n");
  exit(1);
}
$host = isset($cfg['host']) && $cfg['host'] !== '' ? $cfg['host'] : 'localhost';
$port = isset($cfg['port']) ? (int)$cfg['port'] : 0;
$user = isset($cfg['user']) ? $cfg['user'] : '';
$pass = isset($cfg['password']) ? $cfg['password'] : '';
$socket = null;
$colon = strpos($host, ':');
if ($colon !== false && substr($host, $colon + 1, 1) === '/') {
  $socket = substr($host, $colon + 1);
  $host = substr($host, 0, $colon);
}
try {
  if (function_exists('mysqli_connect')) {
    mysqli_report(MYSQLI_REPORT_OFF);
    $db = @mysqli_connect($host, $user, $pass, $cfg['name'], $port ? $port : 3306, $socket);
    if (!$db) {
      throw new Exception('connect failed: ' . mysqli_connect_error());
    }
    mysqli_set_charset($db, 'utf8mb4');
    $all = function ($sql) use ($db) {
      $res = mysqli_query($db, $sql);
      if ($res === false) {
        throw new Exception(mysqli_error($db));
      }
      $rows = array();
      while ($row = mysqli_fetch_row($res)) {
        $rows[] = $row;
      }
      mysqli_free_result($res);
      return $rows;
    };
    $each = function ($sql, $cb) use ($db) {
      $res = mysqli_query($db, $sql, MYSQLI_USE_RESULT);
      if ($res === false) {
        throw new Exception(mysqli_error($db));
      }
      while ($row = mysqli_fetch_row($res)) {
        $cb($row);
      }
      mysqli_free_result($res);
    };
    $quote = function ($s) use ($db) {
      return "'" . mysqli_real_escape_string($db, $s) . "'";
    };
  } elseif (class_exists('PDO') && in_array('mysql', PDO::getAvailableDrivers())) {
    $dsn = 'mysql:dbname=' . $cfg['name'] . ';charset=utf8mb4;';
    if ($socket !== null) {
      $dsn .= 'unix_socket=' . $socket;
    } else {
      $dsn .= 'host=' . $host;
      if ($port) {
        $dsn .= ';port=' . $port;
      }
    }
    $db = new PDO($dsn, $user, $pass, array(PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION));
    $db->setAttribute(PDO::MYSQL_ATTR_USE_BUFFERED_QUERY, false);
    $all = function ($sql) use ($db) {
      $st = $db->query($sql);
      $rows = $st->fetchAll(PDO::FETCH_NUM);
      $st->closeCursor();
      return $rows;
    };
    $each = function ($sql, $cb) use ($db) {
      $st = $db->query($sql);
      while ($row = $st->fetch(PDO::FETCH_NUM)) {
        $cb($row);
      }
      $st->closeCursor();
    };
    $quote = function ($s) use ($db) {
      return $db->quote($s);
    };
  } else {
    throw new Exception('neither mysqli nor pdo_mysql is available');
  }

  $literal = function ($v) use ($quote) {
    if ($v === null) {
      return 'NULL';
    }
    if (is_int($v) || is_float($v)) {
      return (string)$v;
    }
    $s = (string)$v;
    if ($s === '') {
      return "''";
    }
    if (!preg_match('//u', $s)) {
      return '0x' . bin2hex($s);
    }
    return $quote($s);
  };

  $gz = gzopen('php://stdout', 'wb6');
  if ($gz === false) {
    throw new Exception('cannot open gzip stream on stdout');
  }
  $tick = chr(96);
  gzwrite($gz, "SET NAMES utf8mb4;\nSET FOREIGN_KEY_CHECKS=0;\n");
  foreach ($all("SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'") as $row) {
    $table = $row[0];
    $bq = $tick . str_replace($tick, $tick . $tick, $table) . $tick;
    $create = $all('SHOW CREATE TABLE ' . $bq);
    if (!isset($create[0][1])) {
      throw new Exception('SHOW CREATE TABLE returned nothing for ' . $table);
    }
    gzwrite($gz, "\nDROP TABLE IF EXISTS " . $bq . ";\n");
    gzwrite($gz, $create[0][1] . ";\n");
    $prefix = 'INSERT INTO ' . $bq . ' VALUES ';
    $batch = '';
    $rows = 0;
    $each('SELECT * FROM ' . $bq, function ($r) use ($gz, $literal, $prefix, &$batch, &$rows) {
      $vals = array();
      foreach ($r as $v) {
        $vals[] = $literal($v);
      }
      $tuple = '(' . implode(',', $vals) . ')';
      if ($rows > 0 && ($rows >= 500 || strlen($batch) + strlen($tuple) >= 524288)) {
        gzwrite($gz, $prefix . $batch . ";\n");
        $batch = '';
        $rows = 0;
      }
      $batch .= ($rows > 0 ? ',' : '') . $tuple;
      $rows++;
    });
    if ($rows > 0) {
      gzwrite($gz, $prefix . $batch . ";\n");
    }
  }
  gzwrite($gz, "\n-- Dump completed on " . gmdate('Y-m-d H:i:s') . " GMT\n");
  gzclose($gz);
} catch (Exception $e) {
  fwrite(STDERR, 'rehost: dump failed: ' . $e->getMessage() . "\n");
  exit(1);
}
`

// dumpPHPCmd builds the remote command. The script travels as argv — it
// contains no secrets — while the credentials travel as one JSON line on
// stdin via a quoted heredoc, so the password never reaches remote argv or
// the environment: the same discipline as Inspect and Dump. display_errors
// goes to stderr so a PHP fatal can never corrupt the gzip stream, and
// error_reporting=0 keeps site-config notices out of the way.
func dumpPHPCmd(creds *Credentials) string {
	// Marshal cannot fail on a plain struct of strings and ints.
	cfg, _ := json.Marshal(struct {
		Host     string `json:"host,omitempty"`
		Port     int    `json:"port,omitempty"`
		Name     string `json:"name"`
		User     string `json:"user,omitempty"`
		Password string `json:"password"`
	}{creds.Host, creds.Port, creds.Name, creds.User, creds.Password})
	return "php -d display_errors=stderr -d error_reporting=0 -r " + ssh.ShellQuote(phpDumpScript) +
		" <<'REHOST_CREDS'\n" + string(cfg) + "\nREHOST_CREDS"
}

// DumpPHP streams a gzipped SQL dump produced by the PHP helper into w,
// with the same on-the-fly verification as Dump: the stream is gunzipped
// in memory to count bytes and tables and to confirm the completion footer
// the helper only prints after every table succeeded — the guard against a
// silently truncated dump. A verification failure returns the stats
// alongside the error so callers can report what did arrive.
func DumpPHP(ctx context.Context, s Streamer, creds *Credentials, w io.Writer) (*DumpStats, error) {
	stats := &DumpStats{}
	start := time.Now()

	pr, pw := io.Pipe()
	analyzed := make(chan struct{})
	go func() {
		defer close(analyzed)
		analyzeDump(pr, stats)
	}()

	counted := &countingWriter{}
	res, err := s.Stream(ctx, dumpPHPCmd(creds), io.MultiWriter(w, counted, pw))
	pw.Close()
	<-analyzed
	stats.CompressedBytes = counted.n
	stats.Duration = time.Since(start)

	if err != nil {
		return stats, err
	}
	if res.ExitCode != 0 {
		return stats, fmt.Errorf("php dump helper failed: %s", sanitizeReason(res.Stderr, creds.Password))
	}
	if !stats.FooterOK {
		return stats, fmt.Errorf("dump of %s is incomplete — the PHP helper's completion footer is missing (%s of SQL received)",
			creds.Name, humanBytes(stats.Bytes))
	}
	return stats, nil
}
