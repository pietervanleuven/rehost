# AI Agent Guide — rehost

Canonical agent instructions. `CLAUDE.md` and `GEMINI.md` are symlinks to this file —
edit only `AGENTS.md`.

## Project Overview

An open-source, framework-agnostic CLI that migrates a website from one shared host to
another over SSH, with maximum auto-detection and a Terraform-style plan/apply workflow.
Main goal: lowering vendor lock-in for hosting a website.

- `docs/PLAN.md` — **the living spec**: competitive analysis, stack decision, framework
  support matrix, command flow, feature set, phased plan, risks. Read this first.
- `docs/TODO.md` — the living work list: field-validation gate, open decisions,
  phase checklists, known gaps. Keep it current when finishing or deferring work;
  PLAN.md stays the source of truth on scope conflicts.
- `docs/IDEA.md` — the seed idea; kept consistent with PLAN.md but PLAN.md wins on conflict.
- `docs/MARKETING.md` — business model & go-to-market: open-source local CLI (Apache-2.0)
  as the moat, open-core combo later, positioning ("Terraform for website migrations"),
  launch sequence, naming analysis.

## Current State

`v0.2.1` shipped 2026-08-30 via release-please + goreleaser (GitHub Releases
carries the cross-built archives; v0.1.0 was 2026-08-27, v0.2.0 2026-08-29);
the repo is public at github.com/pietervanleuven/rehost. Phases 0–3 (PLAN.md
§6) are feature-complete: SSH layer with capability probe, framework
detection + recursive site discovery, the `init` wizard, `plan` (deep scan,
dry-run `mysqldump | gzip` per site with PHP-helper fallback, tar-pipe
throughput sample, file manifest, persists detected `sites:`), the `check`
compatibility gate (blockers vs warnings in `internal/check`), layered DB
credential extraction and inspection, the DNS snapshot, file inventories,
and `migrate` (pre-flight, per-site file sync, maintenance choreography,
dump rewrite, DB import + verify, config rewrite; a converged run exits 0)
through `cutover` and `status`/`history`/`unlock` over
`.rehost/history.jsonl`. Recent hardening: `check`'s transfer rule now
reports the real manifest-driven tar-pipe (missing `tar`/`find` on either
host is a blocker — no rsync/SFTP transport exists) and gained a `db.dest`
warning per DB-backed site missing `dest_db`; history growth is bounded by
semantic-safe `state.Compact`; the docroot/database destination policies
share one `destPolicy` verdict.

A full pre-release code review (2026-08-28) was worked through end to end —
all 5 criticals, all 14 majors and every listed minor are fixed with
regression tests. Highlights: the mysqldump heredoc now binds to mysqldump
(it fed the credentials to gzip — every dump failed); distro MySQL banners
parse correctly (no more false utf8mb4 blockers); remote-controlled DB names
can no longer traverse local paths; Drupal rewrites are scoped to
`$databases['default']['default']` and WP defines are comment-masked;
`HostKeyAlgorithms`/keepalives fixed the SSH posture; manifests carry
symlinks; migrate takes an advisory destination run lock, gates before
prompting, and supports `--db-password-file`/`REHOST_DB_PASSWORD`; cutover
advice is honest about 404s, cached TTLs and SERVFAIL (TTLs are re-confirmed
at the domain's own nameserver); multisite installs are detected and refused;
exit code 2 now means "gate refused", 1 "operational failure".

Framework and engine expansion (2026-08-29): **Joomla, PrestaShop and Craft
CMS recipes** joined Drupal/WordPress/static (detection, layered credential
extraction, config rewrite, maintenance where the framework offers a
mechanism — Joomla's `$offline` splice, Craft's `craft off/on`; PrestaShop's
is a DB setting, honestly left to the back office). **MariaDB is explicit**
(the `mariadb`/`mariadb-dump` binary names are probed and used when the
mysql-named symlinks are absent) and **PostgreSQL is a second engine** in
`go-hostdb` (psql/pg_dump, pgpass-file credential staging under umask 077,
verified dump footer, ON_ERROR_STOP import; no PHP dump fallback; the
stored-URL rewrite is skipped for pg dumps — COPY-format data — with an
explicit warning). Credentials carry a normalized driver + resolved client
tools end to end; `dest_db` grows an optional `driver:` override. The check
gate is driver-aware and a `db.engine` rule warns on MySQL↔MariaDB
cross-migrations — **rehost never converts between engines** (a PostgreSQL
site needs a PostgreSQL destination; that mismatch is a blocker).

v0.2.1 (2026-08-30) closed two correctness bugs: `finalizeDumpFile` now waits
for the DEFINER-stripping goroutine before closing the dump reader (an early
`RewriteDump` error could race the closes against an in-flight inflate —
`gzip.Reader` is not safe for concurrent use), and two sites sharing a
destination are refused in two layers (`project.Validate` on duplicate
`root`/`dest_root`/`dest_db`, `checkDestCollisions` on an explicit `dest_root`
landing where another site's rebased default goes) — previously the second
site's import DROP TABLEd the first's tables and the run still exited 0.

**Field validation against a real Drupal and a real WordPress site on shared
hosting has still not happened** (docs/TODO.md §1) — this remains the top
priority and the gate before the marketing launch push, three releases
notwithstanding.

Release plumbing: the Homebrew cask in pietervanleuven/homebrew-tap is wired
in `.goreleaser.yaml` but has never published — the `HOMEBREW_TAP_GITHUB_TOKEN`
secret is unset, which 401'd and failed the whole v0.2.1 release run after the
archives had already shipped. The workflow now passes `--skip=homebrew` when
that secret is empty, so a tokenless release is green and merely cask-less;
setting the secret needs no workflow change.

Naming is final and shipped: CLI `rehost`, domain `rehost.sh`, repo and
module path `github.com/pietervanleuven/rehost`. Secrets are never stored —
migrate.yaml has no field that can hold one, passwords are prompted at
runtime.

## Commands

- `make build` / `go build -o rehost ./cmd/rehost` — build the binary.
- `make test` (`go test -race ./...`), `make vet`, `make lint` (golangci-lint),
  `make snapshot` (goreleaser cross-build, publishes nothing).
- `rehost plan [user@host[:port]]` — connect + capability + site detection +
  file inventory report; `--json` for machine output, plain text on non-TTY.
  When run from a project file it rewrites its `sites:` section; hand-written
  comments and layout on unchanged sections are preserved (the save path
  splices values into the existing YAML tree, swapping only what changed).
- `rehost init` — interactive wizard (TTY only): both hosts, connectivity test,
  writes migrate.yaml.
- `rehost check` — compatibility gate (PHP version/extensions per framework,
  DB tooling, manifest-driven tar-pipe transfer viability, a `db.dest`
  warning per DB-backed site missing `dest_db`, disk space); exits non-zero
  while blockers remain.
- `rehost status` / `rehost history` — read-only flow summary and run log
  from the source's `.rehost/history.jsonl` (empty history is a normal
  exit-0 outcome).
- `rehost migrate` — pre-flight (check gate + destination-state policy for
  docroots and `dest_db` databases; `--onto-existing`, `--delete`), then per
  site: file sync, maintenance window, final dump, delta pass, dump rewrite,
  DB import + verify, config rewrite (+ `drush cr`); a converged run exits 0.
- `rehost cutover` — read-only go-live checklist: destination HTTP probe
  (dial override, no hosts editing), DNS records + TTLs, MX/SSL/cron
  instructions; changes nothing anywhere.
- `rehost unlock` — clears maintenance mode left by an interrupted run
  (live probe over history; nothing to unlock = exit 0).

## Architecture

Five generic packages were extracted (2026-08-29) into standalone public
modules that rehost imports like any dependency: `go-ssh`, `go-dns`,
`go-searchreplace`, `go-transfer` and `go-hostdb` (repos under
github.com/pietervanleuven/, working copies under ~/Projects/). Package
names are unchanged except `db` → `hostdb`. A change these packages need now
lands in its own repo, gets a version tag, and is pulled into rehost via
`go get` — remember to tag and push the library before bumping rehost.

- `cmd/rehost` — thin main; version via goreleaser ldflags.
- `internal/cli` — cobra commands; output mode (styled/plain/JSON) resolved from
  `--json`/`--no-color`/`NO_COLOR`/TTY in one place (`options.outputMode`).
- `ssh` (**external module: github.com/pietervanleuven/go-ssh**, at
  ~/Projects/go-ssh) — `Config.Resolve()` honors `~/.ssh/config` (ProxyJump = honest
  error); `Dial` auth chain agent → keys → password prompts via the `Prompter`
  interface; known_hosts strict + TOFU (key mismatch always hard-fails); `Run`
  (non-zero exit ≠ Go error); `Probe` = one sentinel-delimited POSIX script with
  a per-command sequential fallback for restricted shells. The transport-free
  contract (`Result`, `Runner`, `ShellQuote`, `FirstLine`, `Tool`/
  `Capabilities` and the probe) lives in the `go-ssh/remote` subpackage; the
  root package aliases those names, so both paths name identical types. Every
  internal package except `cli` and `project` imports only `go-ssh/remote` —
  keep it that way: nothing below the orchestrator should compile the dial
  stack.
- `internal/project` — migrate.yaml schema v1, strict decode (unknown/secret
  fields rejected with guidance), atomic 0600 writes.
- `internal/tui` — `Renderer` (styled/plain/JSON) + `HuhPrompter`/
  `NonInteractivePrompter` + the init/plan wizard forms (huh stays out of cli);
  tui imports go-ssh, never the reverse.
- `internal/detect` — framework discovery over an `FS` abstraction
  (`NewShellFS` over any `remote.Runner` + local for tests): marker `Find`
  with walk fallback, `Scan`, realpath de-dup.
- `internal/recipe` — pluggable framework recipes (drupal, wordpress, joomla,
  prestashop, craft, static):
  detection fingerprints, destination `Requirements` (min PHP, extensions,
  needs-DB), layered credential extraction (framework CLI → PHP
  echo-helper with sentinel → config regex; transport errors abort, tool
  failures fall through), and the `Maintainer` seam (same layering;
  per-site tool failures are typed `ErrMaintenanceTool` so callers keep
  going). The capability seams' shared input lives here too: `Host`
  (runner + FS + capabilities) and the `Extractor` interface recipes
  implement.
- `searchreplace` (**external module: github.com/pietervanleuven/go-searchreplace**,
  at ~/Projects/go-searchreplace) — pure serialized-safe replacement core
  (wp search-replace --precise semantics, fuzzed round-trip invariant) +
  the URL/docroot replacement-pair planner + `RewriteDump`, which applies
  pairs inside a SQL dump's string literals (the local application point
  migrate uses between dump and import).
- `hostdb` (**external module: github.com/pietervanleuven/go-hostdb**, at
  ~/Projects/go-hostdb; imported as `hostdb`) — `Credentials` (Password
  excluded from JSON, in-memory only); `Inspect` learns version, size,
  charset and table counts in one round trip, feeding the password to mysql
  via a defaults file on stdin (never argv/env); `Import` streams a verified
  local dump into the destination's mysql, password over a 0600 FIFO.
  `Dump` streams `mysqldump | gzip` while gunzipping in memory to verify the
  completion footer — the shell reports gzip's exit, not mysqldump's, so the
  footer is the truncation guard; `DumpPHP` is the same contract via a PHP
  helper (mysqli → PDO, gzip from PHP itself, creds over stdin) for hosts
  without mysqldump. Driver-aware throughout: `NormalizeDriver` folds config
  spellings to mysql/pgsql, `ResolveClientTools` picks mysql/mariadb/psql
  binary names from the probe, and the PostgreSQL paths stage the password
  in a umask-077 pgpass file (libpq refuses FIFOs) removed on the same
  command line. Transient staging lives under `$HOME`/`StageDir` — the
  library defaults to `.hostdb`; cli's init() pins it to `.rehost`. Helper
  diagnostics are prefixed `hostdb:`.
- `internal/check` — pure compatibility rule engine (`Run(Input) []Result`,
  blockers vs warnings) + best-effort remote gatherers (php -m, df, du);
  all remote I/O stays in the caller or behind the `runner` seam.
- `dns` (**external module: github.com/pietervanleuven/go-dns**, at
  ~/Projects/go-dns) — read-only domain snapshot (A/AAAA/CNAME/MX/NS/TXT + TTLs,
  MX targets resolved to IPs) over miekg/dns using the system resolvers;
  rehost never changes DNS.
- `internal/inventory` — per-site size picture over `du` (total, largest
  subdirectories, framework cache/backup dirs worth excluding), best-effort.
- `transfer` (**external module: github.com/pietervanleuven/go-transfer**, at
  ~/Projects/go-transfer) — tar-pipe throughput measurement (capped sample
  over the pipe the real migration would use) + file manifests (size/mtime
  via GNU `find -printf`, paths-only degradation, pure `Diff`, atomic gzipped
  persistence) + `Sync`, the manifest-driven tar-pipe relay (delta-only
  transfer through the orchestrator, opt-in deletions with path-safety
  guards, post-sync destination manifest as the convergence proof). The
  `.rehost-partial-transfer` marker name is part of the on-host contract —
  do not rename it.
- `internal/state` — append-only run history in `<home>/.rehost/` on the
  source (JSON lines, corrupt lines skipped on read); feeds status/history.
  `Record` is one atomic append; `Compact` bounds the file (atomic temp+mv
  rewrite past `CompactThreshold`) while preserving the `MigratedSites` and
  `LockedSites` reads, wired best-effort at the record-writing tails.

## Key Decisions (do not relitigate without the user)

- **Language: Go** (not PHP) — see PLAN.md §2 for the full rationale (Deployer's PHP-SSH
  failure, rclone precedent, Charm TUI, single-binary distribution).
- **Command flow:** `init` (wizard, both hosts) → `plan` (deep scan + dry-run; persists
  `sites:` for the user to attach `dest_root`/`dest_db`) → `check` (compatibility gate,
  rerunnable until green) → `migrate` (execute) → cutover report.
  Support commands: `status`, `history`, `unlock`, `cutover`.
- **Idempotency is a core principle:** every step is convergent ("make destination match
  source"), rerunning `migrate` is safe and incremental; resume-after-crash = rerun.
- **Maintenance mode** on the source (framework-native mechanisms) around the final
  DB dump + delta pass; crash-safe cleanup + `unlock` recovery.
- **Framework priority:** Drupal first (maintainer's main CMS), WordPress and static
  alongside in Tier 1; build Drupal + WordPress recipes in lockstep so the engine never
  gets WP-shaped. Frameworks are pluggable *recipes*, not engine branches.
- **No AI/LLM in the engine** (resolved July 2026; PLAN.md §2.2, §7 and MARKETING.md §1.4a).
  Detection and every migration step are procedural — matches all production migration tools
  (Plesk, cPanel, WP Engine: AI-free) and the fingerprinting state of the art (Plesk Wappspector
  is the reference). An *advisory-only* LLM helper (explain a failure, BYOK, off by default,
  never in the execution path) and any paid AI tier are **deferred**, not designed in — revisit
  only if field data shows edge cases an LLM would close. Do not add AI to detection, planning,
  or execution without the user.
- **MVP scope guard:** the tool migrates site files + database. Mail, DNS, SSL, cron are
  a *report with instructions*, not migration targets.
- **Destination-state policy (decided 2026-07-29, PLAN.md §7 questions 4–5):** `migrate`
  refuses a non-empty destination docroot by default; an explicit flag (working name
  `--onto-existing`) opts into converging onto it, with reruns onto a rehost-populated
  docroot exempt from the refusal (idempotency). File sync is additive by default —
  destination-only files are reported, deleted only with an rsync-style `--delete` flag.
  Refuse-by-default is also the Phase 3 mitigation for half-failed imports; destination
  snapshots before import are post-MVP polish. Implementation decisions (2026-07-29):
  a site with no `dest_root` gets its home-relative source path rebased onto the
  destination home (never the source's absolute path verbatim); an unlistable-but-existing
  destination docroot aborts rather than reading as "empty"; `--delete` stands down for a
  run whose source manifest is pruned (find exit 1 — deletions need a proven-complete
  listing); each converged site records EventMigrate immediately so partial multi-site
  runs rerun without friction.
- **Trust guard (MARKETING.md §1):** the core CLI stays open source, free, and local
  forever. Never add a cloud data plane, phone-home/telemetry by default, nagging, or
  gates on single-site migration features — monetization lives *around* the tool
  (host partnerships, agency/fleet tier, services), not inside it.

## Planned Stack (PLAN.md §2.1)

cobra · Charm v2 (bubbletea/bubbles/lipgloss/huh) · `golang.org/x/crypto/ssh` + `pkg/sftp` ·
`kevinburke/ssh_config` · `miekg/dns` · `gopkg.in/yaml.v3` · errgroup · goreleaser.

## Conventions

- **Commits: Conventional Commits** (https://www.conventionalcommits.org). Format:
  `type(scope): description` — lowercase, imperative, no trailing period.
  - Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`, `perf`.
  - Scopes mirror this repo's package names: `cli`, `detect`, `check`, `project`,
    `tui`, `inventory`, `state`, `recipe/drupal`, `recipe/wordpress`, … Docs-only
    changes: `docs: …` without scope is fine. `ssh`, `db`, `transfer`, `dns` and
    `searchreplace` now name *other* repos — a change there is committed and
    released in that repo, and the version bump lands here as `build(deps): …`.
  - Breaking changes: `!` after type/scope (`feat(cli)!: …`) + `BREAKING CHANGE:` footer.
  - **Releases are automated from these commits** (`.github/workflows/release.yml`):
    release-please accumulates every `feat`/`fix`/`perf` (and breaking change) on
    `main` into a "release PR" that bumps the version and CHANGELOG; merging that PR
    tags `vX.Y.Z`, cuts the GitHub release, and goreleaser attaches the cross-built
    archives. Pre-1.0: breaking changes bump the minor, not the major
    (`bump-minor-pre-major`). Commit messages are machine-parsed — follow the format
    strictly; a stray `feat`/`fix` will move the version.
- **Comments: sparse.** Godoc comments on exported identifiers stay (that's Go
  convention), and inline comments are for what the code *can't* say — invariants,
  protocol quirks, why a fallback exists. Never narrate the next line
  (`// take the manifest`), add step/section headers inside a function, or explain
  a change to the reviewer. When a comment merely restates the code, delete it.
- **Linting: `make lint` stays green** (golangci-lint standard set + gofmt; CI enforces).
  Don't weaken `.golangci.yml` to pass — fix the code. errcheck is strict: check the error
  on any write-path `Close`/flush (a swallowed gzip or temp-file error can persist a
  truncated manifest/project file), and mark deliberate best-effort calls with an explicit
  discard (`_ =` / `_, _ =`) — cleanup closes, error-path removals, reader closes, terminal
  writes. The `tui` renderers funnel terminal writes through the `fprintf`/`fprintln`
  helpers rather than `_ =`-ing every `fmt.Fprint`. Gotcha: a bare `golangci-lint run`
  hides duplicate findings (`max-same-issues` defaults to 3) — use
  `--max-same-issues 0 --max-issues-per-linter 0` for the true list.
- Docs live in `docs/`. Keep IDEA.md and PLAN.md consistent when decisions change;
  PLAN.md is the source of truth.
- TUI output must have a plain-text/JSON fallback for non-TTY (CI) use.
- Every migration step needs a capability-probe-first design with graceful fallbacks —
  never assume `rsync`/`mysqldump`/framework CLIs exist on a shared host.
- Credentials: use obvious placeholders (`user@source.example.com`, `<db-password>`) in
  docs, examples, and the project file; real secret storage is an open design question
  (PLAN.md §7) — flag it whenever code would persist a secret.

## Multi-Agent Notes

- This file is shared by all AI agents (Claude Code, Gemini, etc.). Keep instructions
  tool-agnostic; tool-specific config belongs in each tool's own dotfolder
  (create when needed).
