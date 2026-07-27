# rehost

A framework-agnostic CLI that migrates a website — files **and** database — from
one shared host to another over SSH, with maximum auto-detection and a
Terraform-style `plan` → `migrate` workflow. The goal is to lower vendor lock-in
for website hosting.

It runs entirely on your machine. Your data flows directly between the two hosts
over SSH — never through a third-party cloud.

## Highlights

- **Uses the SSH you already have.** Agent keys, `~/.ssh/config` aliases, and
  `known_hosts` all work out of the box — if `ssh user@host` works, so does
  rehost. No new keys, config, or daemons. [Details ▾](#ssh-authentication)
- **Local and private.** No third-party cloud, no phone-home, no account. Data
  moves host-to-host over your own SSH connection.
- **Framework auto-detection.** Recognizes WordPress, Drupal (7 / 8+, incl.
  multisite), and static sites from file fingerprints — no manual configuration.
- **Capability probing with graceful fallbacks.** Detects what each host offers
  (`rsync`, `mysqldump`, framework CLIs, shell type, PHP version) and adapts,
  rather than assuming tools exist on a shared host.
- **A real `plan` step.** See exactly what a host is and what's on it before any
  changes — Terraform-style ergonomics for migrations.

> **Status: early development.** The connection layer, capability probing,
> framework detection, the `init` wizard, `rehost plan` (including `--dry-run`
> collection: verified DB dumps, file manifests, throughput sampling), and the
> `rehost check` compatibility gate work today. `migrate`, `status`, and
> `unlock` are placeholders that exit with a "not implemented yet" message. See
> [`docs/PLAN.md`](docs/PLAN.md) for the full spec and roadmap. There are no
> packaged binaries yet — build from source.

## Install

Requires Go 1.26+.

```bash
git clone <this-repo> && cd rehost-cli
make build          # produces ./rehost
```

## The workflow

```
init  ──▶  check  ──▶  plan  ──▶  migrate  ──▶  cutover
(wizard)   (compat     (deep      (idempotent   (DNS/mail/SSL
           gate)       scan)      execute)      report)
```

Every command re-derives its state from the live hosts rather than trusting a
cache, so any of them is safe to rerun at any point. Today `init` (wizard),
`plan` (capability + framework + size report, persists detected sites into
migrate.yaml), and `check` (compatibility gate: PHP, extensions, database
reachability and charset, disk space, DNS/mail) do real work; `check` exits
non-zero while blockers remain, so rerun it until it is green.

## Usage

Point `plan` at a host to see what it offers and what's installed on it:

```bash
./rehost plan user@your-host
./rehost plan user@your-host:2222        # non-default port
```

Example output:

```
source: u12345@your-host
  shell bash · Linux x86_64 · PHP 8.3.11 · home /home/u12345
  [ok]       rsync      /usr/bin/rsync  rsync 3.2.7
  [ok]       mysqldump  /usr/bin/mysqldump
  [ok]       mysql      /usr/bin/mysql
  [ok]       tar        /bin/tar
  [ok]       gzip       /bin/gzip
  [ok]       find       /usr/bin/find
  [missing]  wp
  [missing]  drush
  framework: wordpress 6.5.2  /home/u12345/public_html · config .../wp-config.php
             1.2 GiB · largest: wp-content 890.6 MiB, wp-includes 59.6 MiB
             suggested excludes: wp-content/cache 195.3 MiB
```

Detected frameworks so far: **WordPress**, **Drupal** (7 and 8+, incl. multisite),
and static sites. Detection searches recursively from the SSH account's home
(or from `--docroot`), handles multiple sites per account, and measures each
site's size with framework-aware exclusion suggestions (caches, backup dumps,
regenerable artifacts). When run from a project file, `plan` records the
detected sites into its `sites:` section for later commands.

### Compatibility gate

With both hosts in the project file, `check` verifies the destination can
actually run what the source hosts — before any migration work:

```bash
./rehost check
```

```
Compatibility check

  ✓ Websites on the source          1 wordpress
  ✓ File transfer strategy          rsync on both hosts — incremental delta sync
  ✓ Database credentials (source)   public_html: wpdb@localhost (via wp-cli)
  ✓ Database connectivity (source)  MySQL 10.6.18-MariaDB · 57 tables · 210.3 MiB
  ✓ PHP on the destination          PHP 8.2.20 ≥ 7.2 required by wordpress 6.5.2
  ! PHP extensions                  recommended extension(s) missing: zip
  ✓ Disk space on the destination   38.1 GiB free for 1.2 GiB of site data + 210.3 MiB of database

Green with 1 warning(s) — migration can proceed.
```

The model is **blockers vs warnings**: a blocker (missing PHP extension a
framework requires, unreachable database, not enough disk) means migration
cannot work and `check` exits non-zero — fix it and rerun until green. It
also snapshots DNS when the project file has a `domain:` and warns when MX
records point at the source (rehost migrates web only — a naive DNS cutover
would break mail). `check` changes nothing on either host.

### Dry run

`plan --dry-run` proves the collection pipeline without touching a
destination:

```bash
./rehost plan --dry-run
```

- streams a **verified database dump** of every detected site into
  `.rehost/dumps/` next to the project file — `mysqldump` when the host has
  it, a PHP dump helper when it doesn't; a dump missing its completion
  footer is deleted, never mistaken for a good one (you also get a free,
  verified backup out of this)
- takes a **file manifest** (size + mtime of every file) into
  `.rehost/manifests/` — rerunning reports exactly what changed since the
  last run, the same delta an incremental migration would transfer
- samples the achievable **transfer rate** over the tar pipe a real
  migration would use (capped, ~15 s per site)
- records the run in `.rehost/history.jsonl` on the source host

### Output modes

`plan` renders three ways and picks automatically:

| Mode | When | Notes |
|---|---|---|
| Styled | interactive terminal | colored table with ✓/✗ |
| Plain | piped / non-TTY / `--no-color` / `NO_COLOR` | no ANSI, tab-aligned |
| JSON | `--json` | one versioned document per command (`rehost.plan-report.v2`, `rehost.check-report.v1`) |

```bash
./rehost plan user@host | cat            # plain
./rehost plan --json user@host | jq .    # machine-readable
```

### Project file

`./rehost init` walks you through creating a `migrate.yaml` (with a
connectivity test per host); you can also write it by hand:

```yaml
version: 1
name: my-site
domain: example.com            # optional; enables DNS checks + cutover report
source:
  host: source.example.com     # or an alias from ~/.ssh/config
  user: user
  auth: agent                  # agent | key | password (omit for auto)
destination:                   # optional until 'rehost check'
  host: dest.example.com
  user: user
```

`plan` maintains a `sites:` section in this file with what it detected
(framework, root, version) so later commands know what to migrate.

**The project file never stores secrets.** It holds only connection info;
passwords are prompted at runtime. A `password:` (or `secret:`/`token:`) key is
rejected on load.

## SSH authentication

**If `ssh user@host` already works for you, so does `rehost` — with no extra
setup.** rehost speaks the same SSH your system already uses: it honors your
`~/.ssh/config` (host aliases, `HostName`, `Port`, `User`, `IdentityFile`),
your ssh-agent, and your `~/.ssh/known_hosts`.

With the default `auth: auto`, it authenticates in this order:

1. **ssh-agent** — every key loaded in your agent is offered first. Run
   `ssh-add` before `rehost` and your key is used automatically, no prompt:
   ```bash
   ssh-add                       # load your key into the agent
   ./rehost plan user@host       # connects via the agent
   ```
2. **Default key files** — `~/.ssh/id_ed25519`, then `~/.ssh/id_rsa` (or the
   `key_path` in your project file). An **unencrypted** default key works
   without ssh-add. A **passphrase-protected** key that is *not* in the agent
   makes rehost prompt for its passphrase — this is the one case worth knowing
   about, because on the surface it looks like a stall until you type it. Adding
   the key to the agent (step 1) avoids the prompt entirely.
3. **Password** — only if key auth didn't succeed, and only on an interactive
   terminal. Prompted once per run; never written to disk or the project file.

Two conditions always apply, exactly as with plain `ssh`: the key must be
available (loaded in the agent, or an unencrypted file on disk), **and** it must
be authorized on the server (in the remote `~/.ssh/authorized_keys`). `ssh-add`
only loads the key locally — the host still decides whether to accept it.

You can pin the method per host in `migrate.yaml` with `auth: agent`,
`auth: key` (+ `key_path`), or `auth: password`.

### Host keys

rehost verifies host keys strictly against `~/.ssh/known_hosts`:

- **Known host** → connects.
- **Unknown host** → on a terminal, it shows the key's SHA256 fingerprint and
  asks whether to trust it (trust-on-first-use), then records it. Non-interactive
  runs refuse instead, printing the fingerprint so you can verify out of band.
- **Changed key** → always a hard failure with a man-in-the-middle warning. This
  is never overridable by a flag; if a host was legitimately reinstalled, remove
  its line from `~/.ssh/known_hosts` and reconnect.

### Non-interactive / CI

`--json` and any non-TTY run use a non-interactive prompter: instead of
blocking on a password, passphrase, or host-key confirmation, they fail
immediately with a clear message telling you what was needed. Use ssh-agent or
an unencrypted, already-known key for unattended runs.

## Development

```bash
make test     # go test -race ./...
make vet      # go vet ./...
make lint     # golangci-lint (if installed)
make build    # ./rehost
```

Commits follow [Conventional Commits](https://www.conventionalcommits.org). See
[`AGENTS.md`](AGENTS.md) for architecture and contribution conventions.

## License

Intended to be released under Apache-2.0 (open source, free, local — forever).
The core migration tooling will never gate single-site migrations or phone home.
