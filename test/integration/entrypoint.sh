#!/bin/bash
# Brings one environment up, then hands over to sshd. The test harness waits
# for /tmp/rig-ready, which is written only after the database is seeded — so
# a test never races the fixture load.
#
# RIG_ENV selects the environment:
#   mysql   MariaDB reachable as mysql/mysqldump (the common shared host)
#   mariadb the same server with the mysql-named symlinks removed, so
#           ResolveClientTools has to fall back to mariadb/mariadb-dump
#   nodump  MariaDB with every dump binary removed, forcing the PHP fallback
#   pgsql   PostgreSQL via psql/pg_dump
set -euo pipefail

RIG_ENV="${RIG_ENV:-mysql}"
DB_NAME="${DB_NAME:-sitedb}"
DB_USER="${DB_USER:-siteuser}"
# Deliberately awkward: a space, a single quote and a '#' (which starts a
# comment in my.cnf) exercise the defaults-file quoting rather than the happy
# path. A second, plain-password account is created for baseline tests so a
# quoting regression fails one test instead of all of them.
# Assigned in two steps on purpose: bash processes quotes inside
# ${var:-default} even within double quotes, so an apostrophe in the default
# would open a string that never closes.
DB_PASS="${DB_PASS:-}"
[ -n "$DB_PASS" ] || DB_PASS='p@ss w'\''rd#1'

# The same password escaped for a single-quoted SQL literal — the seeding
# statements need it even though rehost never does, because rehost passes
# passwords through defaults files and pgpass rather than through SQL. MySQL
# honors backslash escapes inside strings; PostgreSQL with
# standard_conforming_strings does not, so only the quote doubling is shared.
DB_PASS_MY="${DB_PASS//\\/\\\\}"
DB_PASS_MY="${DB_PASS_MY//\'/\'\'}"
DB_PASS_PG="${DB_PASS//\'/\'\'}"
PLAIN_USER="${PLAIN_USER:-plainuser}"
PLAIN_PASS="${PLAIN_PASS:-plainpass}"

log() { echo "[rig] $*"; }

setup_ssh() {
  if [ -z "${PUBKEY:-}" ]; then
    echo "[rig] PUBKEY is required" >&2
    exit 1
  fi
  echo "$PUBKEY" > /home/site/.ssh/authorized_keys
  chown site:site /home/site/.ssh/authorized_keys
  chmod 600 /home/site/.ssh/authorized_keys
  ssh-keygen -A >/dev/null
  cat > /etc/ssh/sshd_config.d/rig.conf <<'EOF'
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
UseDNS no
AcceptEnv REHOST_*
EOF
  # A noisy login banner is the shared-hosting reality that breaks naive
  # output parsing; keeping one here means the probe and every command
  # parser are tested against it rather than against a pristine stream.
  if [ "${RIG_MOTD:-1}" = "1" ]; then
    cat > /etc/motd <<'EOF'
=============================================
  Welcome to Example Shared Hosting
  Support: help@example.invalid
=============================================
EOF
  else
    : > /etc/motd
  fi
}

wait_for() {
  local what="$1" tries=0
  until eval "$2" >/dev/null 2>&1; do
    tries=$((tries + 1))
    if [ "$tries" -gt 120 ]; then
      echo "[rig] timed out waiting for $what" >&2
      exit 1
    fi
    sleep 1
  done
}

start_mariadb() {
  if [ ! -d /var/lib/mysql/mysql ]; then
    mariadb-install-db --user=mysql --datadir=/var/lib/mysql >/dev/null
  fi
  chown -R mysql:mysql /var/lib/mysql
  mkdir -p /run/mysqld && chown mysql:mysql /run/mysqld
  nohup mariadbd --user=mysql --skip-networking=0 --bind-address=127.0.0.1 \
    >/var/log/mariadb.log 2>&1 &
  wait_for "mariadb" "mariadb-admin ping --silent"

  mariadb -u root <<SQL
CREATE DATABASE \`${DB_NAME}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE \`${DB_NAME}_scratch\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE \`${DB_NAME}_restricted\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE \`${DB_NAME}_rewrite\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASS_MY}';
CREATE USER '${PLAIN_USER}'@'localhost' IDENTIFIED BY '${PLAIN_PASS}';
GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${DB_USER}'@'localhost';
GRANT ALL PRIVILEGES ON \`${DB_NAME}_scratch\`.* TO '${DB_USER}'@'localhost';
GRANT ALL PRIVILEGES ON \`${DB_NAME}\`.* TO '${PLAIN_USER}'@'localhost';
GRANT ALL PRIVILEGES ON \`${DB_NAME}_scratch\`.* TO '${PLAIN_USER}'@'localhost';
GRANT ALL PRIVILEGES ON \`${DB_NAME}_restricted\`.* TO '${DB_USER}'@'localhost';
GRANT ALL PRIVILEGES ON \`${DB_NAME}_rewrite\`.* TO '${DB_USER}'@'localhost';
FLUSH PRIVILEGES;
SQL
  # Seeded AS the site account, so it owns (is the DEFINER of) its own
  # routines and triggers — the shared-hosting norm, and what lets
  # mysqldump --routines work without extra grants.
  MYSQL_PWD="$DB_PASS" mariadb -u "$DB_USER" "${DB_NAME}" < /opt/fixtures/mysql.sql

  # A database holding a routine the site account may read but not
  # SHOW CREATE: mysqldump fails partway while gzip still exits 0, which is
  # precisely the silent truncation the dump's footer check exists to catch.
  mariadb -u root "${DB_NAME}_restricted" <<'SQL'
CREATE TABLE kept (id INT PRIMARY KEY) ENGINE=InnoDB;
INSERT INTO kept (id) VALUES (1);
CREATE FUNCTION root_owned() RETURNS INT DETERMINISTIC RETURN 1;
SQL
  log "mariadb seeded"
}

start_postgres() {
  local pgver pgbin datadir=/var/lib/postgresql/rig
  pgver="$(ls /usr/lib/postgresql | sort -n | tail -1)"
  pgbin="/usr/lib/postgresql/${pgver}/bin"
  mkdir -p "$datadir"
  chown postgres:postgres "$datadir"
  if [ ! -f "$datadir/PG_VERSION" ]; then
    # Socket connections trust, TCP demands a password. rehost connects over
    # TCP, so its pgpass staging is genuinely on trial rather than waved
    # through by a trust-everything cluster.
    su postgres -c "$pgbin/initdb -D $datadir --auth-local=trust --auth-host=scram-sha-256 -E UTF8" >/dev/null
    printf "listen_addresses = '127.0.0.1'\nport = 5432\n" >> "$datadir/postgresql.conf"
    chown postgres:postgres "$datadir/postgresql.conf"
  fi
  # The log lives under the data directory: postgres owns that, and /var/log
  # is root-only.
  su postgres -c "$pgbin/pg_ctl -D $datadir -l $datadir/postgres.log -w start" >/dev/null
  wait_for "postgres" "su postgres -c $pgbin/pg_isready"

  # Staged as a file so no quoting has to survive both the shell and su -c.
  cat > /tmp/pg-setup.sql <<SQL
CREATE USER "${DB_USER}" WITH PASSWORD '${DB_PASS_PG}';
CREATE DATABASE "${DB_NAME}" OWNER "${DB_USER}" ENCODING 'UTF8';
CREATE DATABASE "${DB_NAME}_scratch" OWNER "${DB_USER}" ENCODING 'UTF8';
SQL
  chmod 644 /tmp/pg-setup.sql
  su postgres -c "$pgbin/psql -v ON_ERROR_STOP=1 -f /tmp/pg-setup.sql" >/dev/null
  # Loaded as the site user over the trusted socket, so the fixture objects
  # are owned by the account rehost will connect as.
  su postgres -c "$pgbin/psql -v ON_ERROR_STOP=1 -U $DB_USER -d $DB_NAME -f /opt/fixtures/pgsql.sql" >/dev/null
  # libpq on PATH for the site user's non-login shells too.
  ln -sf "$pgbin/psql" /usr/local/bin/psql
  ln -sf "$pgbin/pg_dump" /usr/local/bin/pg_dump
  log "postgres seeded"
}

setup_ssh

case "$RIG_ENV" in
  mysql)
    start_mariadb
    ;;
  mariadb)
    # A host that ships MariaDB without the compatibility symlinks: the
    # probe finds no mysql/mysqldump and the tool resolver must adapt.
    rm -f /usr/bin/mysql /usr/bin/mysqldump /usr/bin/mysqladmin
    start_mariadb
    ;;
  nodump)
    start_mariadb
    # No dump binary of any name: the PHP helper is the only way out.
    rm -f /usr/bin/mysqldump /usr/bin/mariadb-dump
    ;;
  pgsql)
    start_postgres
    ;;
  *)
    echo "[rig] unknown RIG_ENV: $RIG_ENV" >&2
    exit 1
    ;;
esac

touch /tmp/rig-ready
log "ready (RIG_ENV=$RIG_ENV)"
exec /usr/sbin/sshd -D -e
