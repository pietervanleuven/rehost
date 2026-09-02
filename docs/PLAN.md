# migrate-cli — Project Plan

> A universal, open-source CLI that migrates a website from one shared host to another over SSH,
> with maximum auto-detection and a Terraform-style `plan` → `migrate` workflow.
> Main goal: lower vendor lock-in for hosting a website.

Research date: July 2026. Companion document: [IDEA.md](IDEA.md).

---

## 1. Competitive Landscape

### 1.1 Summary

| Tool | Scope | SSH-based? | Price / License | Key limitation |
|---|---|---|---|---|
| WP-CLI (`wp db export`, `wp search-replace`, aliases) | WordPress only | Yes | Free, OSS | No orchestration — you script the whole workflow; WP-CLI needed on both hosts |
| WP-Migrate (wp-migrate.dev) | WordPress only | Yes (FTP fallback) | $39–$149, closed source | WP-only, macOS/Linux only, not open source |
| Wordmove (Ruby) | WordPress only | Yes | Free, OSS | Effectively unmaintained; Ruby install pain |
| Duplicator | WordPress only | No (package + installer.php) | Free / Pro $69–240/yr | Package build times out on resource-limited shared hosts |
| All-in-One WP Migration | WordPress only | No | Free (512 MB cap) / ~$99+ | Import size cap, wp-admin required, no incremental |
| Migrate Guru / BlogVault | WordPress only | No (runs on vendor cloud) | Free / $149–499/yr | Plugin on both ends; your data flows through a third-party SaaS |
| WPvivid / UpdraftPlus Migrator | WordPress only | No | Freemium | Plugin-based, PHP timeout/size constraints |
| WP Migrate DB Pro (+CLI addon) | WordPress only | No (plugin↔plugin HTTP) | Subscription | Plugin on both sites, paid |
| Plesk Site Import | Multi-CMS (WP, Joomla, Drupal, PrestaShop) | Yes (FTP fallback) | Free with Plesk | **Destination must run Plesk** |
| Plesk Migrator | Whole accounts | Yes (root) | Free with Plesk | Plesk destination; skips service settings |
| cPanel/WHM Transfer Tool | Whole accounts (files, DB, mail, DNS, cron, SSL) | Yes (root/WHM) | With cPanel license | Requires WHM/root on destination — end users can't run it |
| RunCloud / Forge / Ploi / CloudPanel | Panel-specific | n/a | SaaS | No self-serve universal importer; VPS panels, not shared hosting |
| Drush (`sql:sync`, `core:rsync`) | Drupal only | Yes (aliases) | Free, OSS | Drupal-only; archive tooling regressing (removed in Drush 13) |
| interconnect/it Search-Replace-DB | Any PHP+MySQL DB | No (runs on host) | Free, OSS | Only search-replace; upload-and-delete security risk |
| Transfer.website | 45+ CMSs | Credential-based SaaS | Paid per migration | Closed SaaS, data through vendor, not a CLI |
| DIY rsync + mysqldump scripts | Anything | Yes | Free | No detection, no dry-run, no safety rails, per-site hand-tuning |

### 1.2 Gap analysis — why this tool should exist

The landscape splits into four quadrants, and **their intersection is empty**:

1. **Framework-specific CLIs** (WP-CLI, Drush) — SSH-native but locked to one CMS and usually require their runtime on *both* hosts.
2. **WordPress plugins/SaaS** — great UX, but WP-only, need wp-admin + plugin install, hit PHP limits on shared hosts, and the best ones route data through third-party clouds.
3. **Panel tools** (Plesk Site Import, cPanel Transfer Tool) — the most complete migrations with real auto-detection, but they demand a specific panel (often with root) on the destination.
4. **DIY scripts** — universal in principle, zero safety rails in practice.

**No open-source, framework-agnostic, SSH-based, host-to-host migration CLI exists.** The closest
conceptual match is Plesk Site Import (credentials in → scan → detect → import) — migrate-cli is
essentially *"Plesk Site Import without Plesk."* The closest CLI is WP-Migrate (closed source,
paid, WP-only).

**Our differentiators:**
- Open source, local, auditable — no data through third-party clouds.
- Framework auto-detection (WordPress, Drupal, Laravel, Joomla, static, generic PHP) with credential extraction from config files.
- Works with only SSH + standard host binaries; graceful degradation when tools are missing.
- A true **`plan` step** (Terraform ergonomics) — no existing migration tool offers one; only rsync `-n` and Search-Replace-DB have dry-runs.

### 1.3 Realities of shared hosting to design around

- Some plans have **no SSH at all** (that's why plugin/SaaS migrators thrive) → SFTP/FTP fallback dramatically widens the audience (both WP-Migrate and Plesk Site Import concluded this). Keep it on the roadmap, not in the MVP.
- Restricted/jailed shells; `mysqldump`, `rsync`, or `zip` may be absent → capability probing first, fallbacks per capability (e.g. a dropped-in PHP dump helper when `mysqldump` is missing).
- MySQL usually bound to localhost → dumps must run remotely, then transfer.
- DB names/users are panel-prefixed (`u12345_wp`) and destination panels often pre-create them → never assume we can `CREATE DATABASE`; ask/verify instead.
- CPU/time limits kill long-running processes → chunked operation, resume/retry.

---

## 2. Language & Stack Decision

**Decision: Go.** PHP was seriously considered given the audience (PHP devs migrating PHP sites), but:

| Criterion | Go | PHP |
|---|---|---|
| SSH/SFTP library maturity | `golang.org/x/crypto/ssh` + `pkg/sftp` — maintained by the Go team, battle-tested by rclone against thousands of quirky shared hosts; concurrent chunked transfers | phpseclib3 is impressive but single-threaded; **Deployer — the flagship PHP remote-ops tool — deprecated PHP SSH libraries and shells out to system `ssh`** (issue #980). Disqualifying for a tool whose core competency *is* programmatic SSH |
| TUI (checklist progress tables) | Charm stack (bubbletea/bubbles/lipgloss/huh) — best in any language, v2 actively maintained | symfony/console tables/progress bars; no live event-loop TUI equivalent |
| Distribution | One static ~12 MB binary per OS; goreleaser → Homebrew, Scoop, deb/rpm, install.sh | PHAR + PHP runtime requirement, or ~50 MB static-php binaries with a young toolchain |
| Windows story | First-class | Weak |
| Parallel transfers | goroutines/errgroup — natural | No threads; process-spawning workarounds |

The "audience is PHP devs" argument concerns *contributors*, not *runtime*: users see
`brew install migrate-cli` and a nice TUI, never the implementation language (WordPress devs
happily use `gh`, `rclone`, DDEV — all Go). The PHP domain knowledge (parsing `wp-config.php`,
`.env`, `settings.php`) gets encoded in the tool regardless of language. Rust (SSH ecosystem
younger), Node (distribution story broken: pkg deprecated, SEA immature), and Python
(PyInstaller fragility, AV false-flags) were evaluated and rejected.

### 2.1 Go library stack

| Concern | Library |
|---|---|
| Command framework | `spf13/cobra` |
| TUI event loop + components | `charm.land/bubbletea/v2`, `bubbles/v2` (progress, table, spinner) |
| Styling / prompts | `charm.land/lipgloss/v2`, `huh/v2` (setup wizard) |
| Non-TTY fallback logging | `charm.land/log` or `slog` (CI-friendly plain output) |
| SSH transport | `golang.org/x/crypto/ssh` (+ `ssh/agent`, `ssh/knownhosts`) |
| SSH config respect | `kevinburke/ssh_config` (honor `~/.ssh/config`, ProxyJump) |
| SFTP | `pkg/sftp` with concurrent reads/writes enabled |
| DNS (dig-like) | `miekg/dns` (full record control, custom resolvers, TTL inspection) |
| `.env` parsing | `joho/godotenv`; regex/heuristics for PHP configs |
| Project file | YAML via `gopkg.in/yaml.v3` |
| Parallelism | `golang.org/x/sync/errgroup` |
| Release | `goreleaser` → GitHub Releases, Homebrew tap, Scoop, deb/rpm |

### 2.2 Key architecture principles (lessons from prior art)

- **Capability probing first** (rclone-style): on connect, detect available remote tools (`rsync`, `mysqldump`, `wp`, `drush`, `tar`, `gzip`, PHP version, shell type) and select strategies accordingly. Every step has a preferred path and fallbacks.
- **Remote-pipe transfers**: stream `mysqldump | gzip` on source → destination session stdin through the local client — no full local round-trip; fall back to SFTP staging when shells are restricted.
- **Recipe/task model** (Deployer's lesson): framework-specific detection and migration steps as composable tasks, so adding a CMS is adding a recipe, not forking the engine.
- **Credential extraction layered by capability** (WP-Migrate's approach): (1) use the framework's own CLI if present (`wp config get`, `drush status`); (2) else upload a tiny PHP helper that `include`s the config and echoes values (robust against creative configs), then delete it; (3) else regex-parse.
- **Detection is deliberately procedural — no ML/LLM.** Framework detection is a solved
  file-fingerprint problem, and the entire industry implements it that way with zero AI:
  Plesk's open-source [Wappspector](https://github.com/plesk/wappspector) CLI (used in
  WebPros/cPanel production) covers 23 frameworks — WordPress, Drupal 6–10, Joomla, Laravel,
  Symfony, TYPO3, PrestaShop, static builders — with pure file-existence + content-regex
  matchers (`wp-includes/version.php`, Drupal's `system.info.yml`, Laravel's `artisan`); it is
  a near-perfect reference for our `internal/detect` recipes. OWASP WSTG endorses only
  rule-based fingerprinting (WhatWeb, Wappalyzer, BlindElephant). Two findings settle the
  question against AI: (a) the one peer-reviewed ML alternative (OPTICS clustering, CMES 2024)
  *underperformed* rules, missing ~39% of WordPress and ~21% of Drupal sites; (b) the documented
  heuristic failure modes (hidden/renamed signatures, HTTP-level ambiguity) belong to
  *adversarial remote HTTP* fingerprinting — our cooperative SSH filesystem access sees the
  canonical files regardless, so they don't apply. The robustness lever is *multi-signal*
  detection (combine independent fingerprints), not a model. For exact version pinning,
  BlindElephant's static-file-checksum technique is directly reusable over SSH.
- **Detect and enumerate, don't assume one site**: scan the account and list all installs found (Plesk Site Import model).
- **Never blind-copy source config**: rewrite destination `wp-config.php` / `.env` / `settings.php` with new DB credentials.
- **Serialized-data-safe search-replace** is table stakes — silent corruption of PHP-serialized data is the single most cited migration failure mode. `wp search-replace --precise` is the bar.
- **Idempotent, convergent steps**: every step is "make destination match source", not "perform action". Rerunning `migrate` must be safe and incremental — file sync transfers only deltas (rsync semantics), DB dump/import overwrites deterministically, search-replace and config rewrite are no-ops when already applied. This also collapses "resume after crash" and "second-pass delta sync" into the same mechanism: just run it again.
- **Maintenance mode on the source** during the critical window: use the framework's native mechanism (WP: `.maintenance` file / `wp maintenance-mode activate`; Drupal: `drush state:set system.maintenance_mode 1`; Laravel: `php artisan down`) before the final DB dump + delta file pass, disable after. This freezes writes so the snapshot is consistent and reruns converge. Must be crash-safe: always attempt cleanup on exit, and provide an explicit recovery command in case the tool dies mid-run.

---

## 3. Supported Frameworks

A framework counts as *supported* when it has all six recipe pieces: detection fingerprints,
credential extraction, DB dump/import strategy, serialized-safe search-replace rules, config
rewrite, and maintenance-mode integration. The recipe architecture (§2.2) means adding a
framework is adding a recipe, not touching the engine.

**Database engines (decided 2026-08-29):** rehost migrates MySQL-family (MySQL and
MariaDB — one toolchain; hosts shipping only the `mariadb`/`mariadb-dump` binary names
are handled) and PostgreSQL (`psql`/`pg_dump`, no PHP fallback). **rehost never converts
between engines**: a PostgreSQL site needs a PostgreSQL destination (blocker otherwise),
and MySQL↔MariaDB cross-migrations import as-is with a `db.engine` warning naming the
divergences to test for. Engine conversion is a different product, not a roadmap item.

| Tier | Framework | Detection markers | Maintenance mode | Notes |
|---|---|---|---|---|
| **1 — MVP** | **Drupal** (7, 8+, multisite) | `sites/default/settings.php`; `core/lib/Drupal.php` (8+); `misc/drupal.js` (7) | `drush state:set system.maintenance_mode 1` (D8+) / `drush vset` (D7); direct DB fallback | **Priority — maintainer's main CMS.** Prefer Drush when present (`sql:dump`, `cr`, `sset`); else parse the `$databases` array. Multisite: enumerate `sites/*/settings.php`. Post-import: `drush cr`, rewrite `trusted_host_patterns`, preserve `hash_salt`, handle private files path + config-sync dir |
| **1 — MVP** | **WordPress** (incl. multisite) | `wp-config.php`, `wp-includes/` | `.maintenance` file / `wp maintenance-mode activate` | Largest audience. `wp-config.php` may sit above docroot; serialized search-replace essential; `$table_prefix` |
| **1 — MVP** | Static sites | none of the others match | n/a | Files only — trivial, but validates the whole pipeline |
| **2 — shipped 2026-08** | **Joomla** (3.x–5.x) | `libraries/src/Version.php` (3.8+); `libraries/cms/version/version.php` (3.0–3.7) | `$offline` property spliced in `configuration.php` (atomic write) | Credentials as JConfig class properties; `dbtype` recorded — Joomla runs on MySQL/MariaDB **or PostgreSQL**, requirements follow the driver |
| **2 — shipped 2026-08** | **PrestaShop** (1.6, 1.7/8) | `config/defines.inc.php`; version from `AppKernel.php` / `_PS_VERSION_` | none (a DB setting — back-office step, generic live-dump warning) | Both credential shapes: `_DB_*_` defines (1.6) and the `parameters.php` array (1.7/8); MySQL-only |
| **2 — shipped 2026-08** | **Craft CMS** (3–5) | `craft` console script confirmed against `composer.json`; version from `composer.lock` | `php craft off` / `on` (3.5+) | **Project root is the migration unit** (vendor/, config/, storage/ travel with web/); creds in `.env` (`CRAFT_DB_*`/`DB_*`/URL form); MySQL/MariaDB **or PostgreSQL** |
| **2 — shipped 2026-09** | **Laravel** (6–12) | `artisan` console script confirmed against `composer.json`; version from `composer.lock` | `php artisan down` / `up` (status via `storage/framework/down`) | **Project root is the migration unit** (vendor/, bootstrap/, config/, storage/ travel with public/); creds in `.env` (`DB_*`/URL form); MySQL/MariaDB **or PostgreSQL**; `DB_CONNECTION=sqlite` (the 11+ skeleton default) = a file DB that travels with the file sync, no server database |
| **2 — shipped 2026-09** | **Generic PHP + database** | no fingerprint markers: confirmed at the conventional docroots (and `--docroot`) by `index.php` + a config file yielding a database name | none — no framework to ask, so migrate dumps the live site and says so | The long tail of hand-rolled PHP. Reads `define()`, scalar, `=>` and `$cfg['key'] =` forms plus positional `mysqli_connect`/`mysql_connect`/`new PDO` DSNs; MySQL/MariaDB **or PostgreSQL**. Rewrites only the literals it parsed (positional calls degrade to guidance). The called API (`mysqli`/`pdo_mysql`/`pgsql`) becomes the extension requirement; the removed `mysql_*` API warns rather than blocks |
| 3 — later | Magento, Symfony/generic Composer | `app/etc/env.php`; `composer.json` require inspection | varies | Driven by user demand post-1.0 |

---

## 4. The Natural Flow

```
init  ──▶  plan  ──▶  check  ──▶  migrate  ──▶  cutover
(wizard)   (deep       (compat     (idempotent   (DNS/mail/SSL
            scan +      gate)       execute,      report +
            dry-run)    fix & rerun rerun=delta)  verify)
```

1. **`migrate-cli init`** — interactive wizard (source *and* destination credentials up front),
   connectivity + auth test on both, writes the project file. Both hosts from the start: most
   fatal problems live on the destination, so surface them before any work is done.
2. **`migrate-cli plan`** — deep scan and dry-run: full detection, credential extraction, DB dump
   feasibility, file inventory + exclusions, DNS snapshot, detected sites written to the
   project file — where the user then attaches per-site `dest_root`/`dest_db` before the gate.
3. **`migrate-cli check`** — the **compatibility gate**. Light source scan (framework + versions),
   then source requirements vs destination capabilities:
   - PHP version + required extensions for the detected framework (e.g. Drupal needs `gd`, `pdo_mysql`, `mbstring`…)
   - DB server flavor/version, charset/collation support (utf8mb4)
   - disk space on destination vs source size; inode headroom
   - remote tool availability both ends (`mysqldump`, `tar`, `find`, framework CLI)
   - a `dest_db` named for every database-backed site
   Output is the checklist table with **blockers vs warnings**. It's rerunnable until green: fix
   the destination (bump PHP version in the panel, create the DB, free space), run `check` again.
4. **`migrate-cli migrate`** — idempotent execution. First run = bulk copy while the site stays
   live; rerun near cutover = delta only, inside the maintenance-mode window. Records stats.
5. **Cutover** — final report (printed after `migrate`, re-printable via `migrate-cli cutover`):
   DNS change instructions with current TTLs, MX/mail warning, SSL re-issue note, crontab listing,
   verification steps (hosts-file test) before flipping DNS.

Support commands: `status` (where am I in the flow, what's green), `history` (past migrations +
stats), `unlock` (clear maintenance mode after a crash).

Every command re-derives state from reality (the two hosts + project file) rather than trusting
cached state, so any command can be rerun at any point — the flow is a checklist you converge
through, not a state machine you can wedge.

---

## 5. Feature Set

### 5.1 MVP (must-have)

- `migrate-cli init`: credential wizard for both hosts, connectivity/auth test, project file creation
- `migrate-cli check`: source↔destination compatibility gate (§4), rerunnable until green
- `migrate-cli plan`: deep source scan:
  - framework detection (Tier 1: Drupal, WordPress, static — see §3) via file fingerprints
  - docroot discovery, file inventory (count, total size, large files, cache dirs)
  - DB credential extraction, DB size, dump feasibility check (`mysqldump` present? charset?)
  - server info: PHP version, outbound IP, disk usage
  - DNS snapshot via dig-equivalent: A/AAAA, MX, NS, TXT + current TTLs
  - blockers reported and **hard-stop** if unresolvable; warnings otherwise
  - updates the **project file** (`migrate.yaml`) with the full migration plan
- Checklist-style progress table (TUI), with plain-text output when not a TTY
- `migrate-cli migrate`: execute against the project file:
  - **idempotent & incremental**: rerunning converges — only changed files transfer, DB is re-dumped and re-imported deterministically, already-applied steps are detected and skipped
  - typical flow: first run does the bulk copy while the site stays live; a rerun near cutover moves only the delta
  - maintenance mode on source (framework-native: `.maintenance` / `drush smm` / `artisan down`) around the final DB dump + delta pass, auto-disabled after — with crash-safe cleanup and a recovery command
  - dump DB on source (correct charset), transfer, import on destination
  - file sync with rsync *semantics* (mtime/size delta, exclusions). Topology decided
    2026-07-29: a **manifest-driven tar-pipe relay through the orchestrator's machine** —
    rehost holds SSH connections to both hosts and pipes tar source→destination; works
    wherever tar+gzip exist and never assumes the hosts can reach each other. Direct
    host-to-host rsync (needs source→destination SSH, rare on shared hosting) is a
    deferred optimization; SFTP stays the last resort for tar-less hosts
  - serialized-safe URL/path search-replace
  - config rewrite on destination with new credentials
  - post-checks + migration stats (duration, sizes, warnings) recorded in a hidden state folder on the source host (per IDEA.md) and locally
- Heads-up report for non-migratable items: MX pointing at source (mail won't move!), DNS cutover with TTL advice, SSL re-issuance, cron jobs (enumerate crontab and show it)

### 5.2 Core (v1.x, expected by users)

- Resume/retry on interrupted transfer (falls out of idempotent design: rerun = resume)
- Maintenance mode support beyond the big three frameworks; `--no-maintenance` flag for zero-downtime-tolerant cases
- Destination pre-flight: PHP version/extensions vs framework requirements, disk space, DB server version
- Post-migration verification before DNS cutover (temp-URL / hosts-file test guidance, HTTP smoke test)
- Exclusion presets (cache, logs, `node_modules`, backup dirs) + user-defined
- Tier 2 frameworks: Laravel, Joomla, generic PHP+MySQL (see §3)
- PHP dump-helper fallback when `mysqldump` is absent
- Multi-install enumeration (pick which site under the account to migrate)
- Migration history/stats view (`migrate-cli migrate --stats` or `history` subcommand)

### 5.3 Extras (future)

- SFTP/FTP-only mode (no SSH shell) — widens audience massively
- Panel-optimized profiles: Plesk, cPanel, one.com, Combell, Forge, CloudPanel, RunCloud (paths, DB naming, API hooks where available)
- Auto DNS adjustment via registrar/DNS APIs (Cloudflare first), TTL lowering ahead of cutover
- Email account inventory & migration guidance (imapsync integration?)
- ~~PostgreSQL support~~ shipped 2026-08 (psql/pg_dump engine; pgpass-file credential discipline; no PHP dump fallback; stored-URL rewrite inside pg dumps still open — COPY-format data). SQLite still future
- Rollback command; scheduled/cron-driven re-sync; CI mode (JSON output)
- Jump-host / bastion support (mostly free via ssh_config respect)

---

## 6. Phased Plan

### Phase 0 — Foundation (week 1)
**Goal: a runnable skeleton with the right bones.**
- Init Go module, repo layout (`cmd/`, `internal/detect`, `internal/tui`, `internal/project`, `internal/check`, `internal/recipe`, `internal/inventory`, `internal/state`; the generic SSH, DB, transfer, DNS and search-replace layers were later extracted into the standalone `go-ssh`/`go-hostdb`/`go-transfer`/`go-dns`/`go-searchreplace` modules)
- Cobra skeleton: `init`, `check`, `plan`, `migrate`, `status`, `unlock`, `version`; goreleaser + GitHub Actions CI (lint, test, cross-build) from day one
- Conventional Commits from the first commit (see AGENTS.md); release automation (changelog, semver) added later on top
- SSH connection layer: key/agent/password auth, `~/.ssh/config` + known_hosts respect, capability probe (`which rsync mysqldump tar gzip php wp drush`, shell type, PHP version)
- Project file schema (`migrate.yaml`) v1 + load/save
- **Exit criteria:** `migrate-cli plan` connects to a real shared host and prints its capability report.

### Phase 1 — Check, Scan & Detect (weeks 2–3)
**Goal: `init` → `plan` → `check` produce a complete, honest picture — and catch destination problems before any migration work.**
- `init` wizard (huh form): both hosts' credentials, connectivity test, project file
- Framework detection engine (recipe interface + fingerprints): **Drupal and WordPress first-class** (Drupal is the maintainer's daily driver — build both recipes in lockstep so the engine never gets WP-shaped), plus static; scan upward from docroot (wp-config.php/`.env` may sit above it); enumerate multiple installs, incl. Drupal multisite. Reference implementation: Plesk [Wappspector](https://github.com/plesk/wappspector)'s matcher set (§2.2) — port the fingerprint patterns rather than reinventing them; add BlindElephant-style file checksums for exact version pins
- `check` compatibility gate: per-framework PHP version/extension requirements vs destination, DB version + utf8mb4 support, disk space, tool availability both ends — rerunnable until green
- Credential extraction (layered: framework CLI (`drush status`, `wp config get`) → PHP echo-helper → regex)
- DB inspection: connect/dump feasibility, size, charset (utf8 vs utf8mb4), table prefix
- File inventory with size breakdown and suggested exclusions
- DNS snapshot module (A/AAAA, MX, NS, TXT, TTLs) + "mail points at source" warning logic
- TUI checklist table (bubbletea) + plain/JSON output fallback
- Blockers vs warnings model; `plan` writes the project file
- **Exit criteria:** `init` + `plan` + `check` against a real Drupal site and a real WordPress site on shared hosting yield a correct project file with zero manual input beyond credentials; `check` correctly flags an incompatible destination (e.g. missing PHP extension) *before* any migration work.

### Phase 2 — Dry-run collection (week 4)
**Goal: prove the pipeline without touching a destination.**
- Remote `mysqldump | gzip` streaming with fallback to PHP dump helper; verify dump integrity (row counts / footer check)
- Tar-pipe file collection dry-run (measure achievable throughput, test excludes)
- Convergence bookkeeping: file manifest with mtime/size/checksums so reruns compute deltas even without rsync on the host; interrupted run = partial state, rerun completes it
- Hidden state folder on source (`.migrate-cli/`) for stats/history
- **Exit criteria:** dry-run produces a valid DB dump + file manifest and records stats; rerunning after an interrupt completes incrementally instead of starting over.

### Phase 3 — Migrate MVP (weeks 5–7)
**Goal: end-to-end migration of a Drupal site and a WordPress site between two real shared hosts.**
- Final pre-flight (re-run `check` + DB reachable, credentials valid) as `migrate`'s first step
- File sync: manifest-driven delta via tar-pipe relay through the orchestrator (topology decision, §5); SFTP as last resort; direct rsync deferred — all paths incremental on rerun
- Maintenance-mode orchestration: enable on source before final DB dump + delta pass, disable after; `defer`-style cleanup on any exit path + `migrate-cli unlock` recovery command
- DB import on destination (charset-correct), serialized-safe search-replace (port `wp search-replace` semantics; use `wp`/`drush` remotely when present)
- Config rewrite with destination credentials (wp-config.php; Drupal settings.php incl. `trusted_host_patterns` + `hash_salt`); Drupal post-import: `drush cr`
- Cutover report + `cutover`/`status`/`history` commands wired into the flow
- Post-migration checks: HTTP smoke test via hosts-override, diff of file counts, DB table counts
- Cutover report: DNS instructions with current TTLs, MX warning, SSL re-issue note, crontab listing
- Migration stats recorded (duration, timestamp, DB size, warnings) — source hidden folder + local history
- **Exit criteria:** a real Drupal site and a real WordPress site migrated host-to-host with only `init` → `plan` → `check` → `migrate`, verified working on the destination; running `migrate` a second time completes in a fraction of the time and changes nothing (idempotency proof); killing the tool mid-run leaves no stuck maintenance mode after `unlock`. **This is the public v0.1 / show-HN moment.**

### Phase 4 — Hardening & breadth (weeks 8–10)
**Goal: it works on hosts we didn't test on.**
- Laravel migration recipe complete; static sites; Drupal multisite edge cases (shared codebase, per-site DBs)
- Fallback matrix testing against restricted shells (no rsync, no mysqldump, jailed SFTP)
- Retry/resume polish, chunking under CPU/time limits; idempotency test suite (run twice, assert zero changes on second pass)
- Exclusion presets, `--exclude`, `--dry-run` on migrate
- Docs site, install script, Homebrew/Scoop; integration test rig (Docker images mimicking cPanel/Plesk-like layouts)
- **Exit criteria:** green migrations across a matrix of 4–5 real hosting providers (e.g. one.com, Combell, a cPanel host, a Plesk host).

### Phase 5 — v1.0 polish
- Joomla + generic PHP+MySQL recipes (Tier 2 complete); multi-install selection UX
- Migration history command; JSON/CI output mode
- Security review (credential handling, helper-script cleanup, known_hosts strictness)
- v1.0 release + announcement

### Phase 6+ — Post-1.0 (prioritize by feedback)
- SFTP/FTP-only mode (no shell) — biggest audience expansion
- Panel-optimized profiles (Plesk/cPanel APIs, one.com, Combell, Forge, CloudPanel)
- Auto DNS adjust (Cloudflare/registrar APIs), TTL pre-lowering
- Email inventory/migration guidance, PostgreSQL support, rollback command

---

## 7. Risks & Open Questions

| Risk | Mitigation |
|---|---|
| Shared-host weirdness is unbounded (jailed shells, missing binaries, odd layouts) | Capability probing + layered fallbacks; build the provider test matrix early (Phase 4); design every step to degrade gracefully |
| Serialized-data corruption during search-replace | Port proven `wp search-replace` semantics; prefer running `wp`/framework CLI remotely when available; extensive test fixtures |
| Destination can't create DBs (panel-managed) | Verify-don't-create; instruct the user to create DB in panel, then validate credentials in pre-flight |
| Long transfers killed by host limits | Chunked ops, resume bookkeeping, nice/ionice where available |
| Scope creep toward "migrate everything" (mail, DNS, SSL) | MVP migrates site + DB only; everything else is a *report with instructions* — that's already more than competitors do |
| No-SSH hosts lock out part of the audience | Accept for MVP; SFTP/FTP mode is the top post-1.0 item |
| Crash leaves source stuck in maintenance mode | Cleanup on every exit path (incl. signal handling); `migrate-cli unlock` recovery command; use framework-native mechanisms that admins recognize and can remove by hand (`.maintenance` file, `artisan up`) |
| Search-replace applied twice corrupts data | Old→new replace is naturally idempotent (second pass finds nothing), but guard the edge case where old URL is a substring of the new one; record applied replacements in migration state |

**Resolved (July 2026 research): the engine ships no AI/LLM.** Detection, capability probing,
and every migration step are procedural — this matches every production migration tool examined
(Plesk, cPanel ecosystem, WP Engine's Site Migration plugin: all AI-free) and the state of the
art in fingerprinting (§2.2). A strictly *advisory* LLM helper (e.g. "explain why `check`
failed", BYOK, off by default, never in the execution path) is **deferred**, not designed in —
its value is unproven and Stack Overflow 2025 shows ~76% of developers don't want AI touching
deployment/operations, the exact category we occupy where deterministic safety *is* the pitch.
Revisit only if field data justifies it (see below). Business-side framing of this decision and
the paid-tier reasoning live in MARKETING.md §1.4a.

Data gap worth instrumenting: **what fraction of real-world shared-host migrations need manual
intervention, and of what kind** (missing binaries, odd PHP setups, serialized-data edge cases)?
No migration vendor publishes this, and it's the number that would decide whether an advisory AI
layer ever pays off. Candidate: an opt-in, anonymized failure taxonomy after launch (opt-in only —
never violates the no-telemetry-by-default trust guard).

Open questions to settle during Phase 0–1:
1. Project file location: repo-style `./migrate.yaml` vs `~/.config/migrate-cli/<project>/`? (Lean: current dir, like Terraform.)
2. Store secrets in the project file, OS keychain, or prompt-per-run? (Lean: reference-only in file; keychain via `zalando/go-keyring` as an option.)
3. Name check: "migrate-cli" collides with golang-migrate's `migrate` CLI (DB schema migrations) — consider a distinct binary name before public release.

Design questions 4–5 — **DECIDED (user decision, 2026-07-29)**, the destination-state policy
that unblocks Phase 3:

4. **What state is the destination allowed to be in?** `migrate` **refuses a non-empty
   destination docroot by default** with a clear message; an explicit opt-in flag (working
   name `--onto-existing`, final name at implementation time) converges onto it. Exception:
   a docroot that a previous `rehost migrate` populated is not "non-empty" for this rule —
   reruns must stay friction-free for idempotency (detect via the run history / a marker,
   design at implementation time). Sync is **additive by default**: destination-only files
   inside the docroot are *reported*, not deleted; an rsync-style **`--delete`** flag enables
   true mirror convergence (deletions listed before acting). Honest consequence, accepted:
   a default rerun is convergent for everything except deletions — the cutover report must
   list surviving destination-only files so nothing lingers silently.
   *Implemented 2026-07-29, with these decisions:* the default destination docroot rebases
   the source's home-relative path onto the destination home (`/home/alice/public_html` →
   `/home/bob/public_html`; explicit `dest_root` overrides); an existing docroot that
   cannot be listed aborts the pre-flight instead of counting as "empty"; `--delete` is
   skipped for any run whose source file listing was pruned (find exit 1) since deletions
   demand a proven-complete listing; converged sites record EventMigrate one by one so a
   partial multi-site failure regains idempotent reruns for the finished sites.
5. **Half-failed migrate onto a non-empty destination.** The refuse-by-default policy above
   is the cheap Phase 3 mitigation: fresh docroots are the normal path, and converging onto
   an existing site requires the explicit flag, at which point the pre-flight warns that
   rollback does not exist yet. Snapshotting the destination DB (and optionally docroot)
   before import remains desirable polish — post-MVP, disk quotas permitting.