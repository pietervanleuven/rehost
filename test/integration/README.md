# Integration rig

Runs rehost's host-facing plumbing against a real sshd and a real database
server in a container.

```bash
make integration        # build the image if needed, run every environment
make integration-clean  # remove leftover containers and the image
```

Needs Docker. A build tag (`integration`) keeps it out of `make test`, so the
default suite stays fast and daemon-free.

## What this is not

It is deliberately **not** a CMS harness. Installing a stock WordPress or
Drupal would prove very little: what breaks real migrations is ten years of
plugins, a hand-edited config, a docroot symlinked somewhere odd and absolute
paths baked into the database — none of which a `composer create-project`
reproduces. The recipes are already unit-tested against file trees that model
those shapes better than a pristine install would.

What the unit tests *cannot* reach is the shell plumbing: pipelines whose exit
status comes from the wrong end, credentials staged through defaults files and
FIFOs, streams verified while they are still moving. Those bugs do not care
whether the CMS is default or filthy — they care whether there is a real
mysqld on the other end of a real SSH connection. That is what this provides,
and why the fixtures are hand-written to be nastier than any default install:
four-byte characters, serialized PHP whose byte-length prefixes must be
recomputed, binary blobs, views, triggers, routines, and a database whose
routine the site account may not read.

This narrows the field-validation gate in `docs/TODO.md` §1; it does not close
it. A container is a cooperative Linux box. A shared host is an adversarial
one — jailed shells, panel-specific layouts, disabled PHP functions, process
limits that kill a long dump. Those still need a real host.

## Environments

One image serves all of them; `entrypoint.sh` removes tooling and starts the
engine each variant calls for, so switching costs a container start rather
than an image build. `RIG_ENV` selects:

| `RIG_ENV` | What it models |
|---|---|
| `mysql` | MariaDB reachable as `mysql`/`mysqldump` — the common shared host |
| `mariadb` | the same server with the mysql-named symlinks removed, so `ResolveClientTools` must fall back to `mariadb`/`mariadb-dump` |
| `nodump` | no dump binary of any name, forcing the PHP dump fallback |
| `pgsql` | PostgreSQL over `psql`/`pg_dump`, with TCP auth requiring a password so the pgpass staging is genuinely exercised |

Two details are deliberate rather than incidental:

- **A noisy MOTD** is configured on every container. Shared hosts print
  banners, and the capability probe's sentinel exists to survive them; without
  a banner here that defence would never be tested.
- **An awkward database password** (`p@ss w'rd#1` — a space, a quote and the
  my.cnf comment character) is the default. A second, plain-password account
  exists so a quoting regression fails one test rather than all of them.

## What it covers

From `docs/TODO.md` §1, these no longer read "never run outside unit tests":

- the `mysql --defaults-extra-file=/dev/stdin` heredoc against a real client
  and server, including a password that needs my.cnf quoting
- the dump restoring into a scratch database, with table **and row** counts
  compared against an independent measurement
- the PHP dump fallback against a real database, restoring to the same counts
- MariaDB-only tool naming, end to end
- PostgreSQL: `pg_dump` over SSH, pgpass staging, `ON_ERROR_STOP` on import,
  and the staging directory left clean afterwards
- the serialized-data round trip: a dump rewritten from one host name to a
  **different-length** one, imported, then handed to PHP's own `unserialize`
- a genuinely truncated dump being caught by the completion-footer check
  rather than silently accepted

## What it found

The first full run caught a shipped bug: `footerComplete` in `go-hostdb` read
only a dump's last non-blank line. That is right for mysqldump, but a
`pg_dump` closes its footer with a bare `--` line and (since the 2025 security
releases) appends a `\unrestrict <token>` meta-command — so the footer is
never the last line, every PostgreSQL dump was rejected as truncated, and no
PostgreSQL site could be migrated at all. Fixed in go-hostdb v0.1.1, with the
real dump tails pinned as unit tests there.

## Adding a case

Fixtures live in `fixtures/`. Add adversarial data rather than realistic data
— the point is to be harder than production, not to look like it. New
environments go in `entrypoint.sh`'s `case` and need no new image.

Container logs are printed automatically when a test fails, and a failed
container is left un-removed only long enough to read them.
