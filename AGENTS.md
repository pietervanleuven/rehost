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

Phase 0 (foundation) and Phase 1 (check, scan & detect — PLAN.md §6) are
implemented: SSH layer with capability probe, framework detection + recursive
site discovery, the `init` wizard, the `check` compatibility gate
(blockers-vs-warnings in `internal/check`), layered DB credential extraction
(wp-cli/drush → PHP helper → regex), DB inspection (connectivity, version,
size, charset/utf8mb4), the DNS snapshot with mail-points-at-source warning
(optional `domain:` in migrate.yaml), file inventories with suggested
exclusions, and `plan` persisting detected sites into the project file.

Phase 2 (dry-run collection — PLAN.md §6) is feature-complete: `plan
--dry-run` streams a footer-verified `mysqldump | gzip` per site into local
`.rehost/dumps/` (PHP dump-helper fallback when mysqldump is missing),
samples tar-pipe throughput (capped), takes a file manifest (GNU `find
-printf`, paths-only fallback) persisted to `.rehost/manifests/` — reruns
report the delta as the incremental-convergence proof — and records run
history in `.rehost/history.jsonl` on the source.

Both phases still need field validation against a real Drupal and a real
WordPress site on shared hosting (Phase 1/2 exit criteria). Phase 3
(PLAN.md §6) is nearly feature-complete: `migrate` runs the pre-flight
(check gate + source-DB reachability + destination-state policy for both
docroots and `dest_db` databases — verified, never created; passwords
prompted at runtime) and, when green, converges each site: bulk file sync
over the tar-pipe relay, then the database choreography — maintenance on
(write-ahead EventMaintenance), verified final dump, file delta pass,
serialized-safe docroot rewrite of the dump (`searchreplace.RewriteDump`),
FIFO-fed import with table-count verify, config rewrite pointing the
synced wp-config.php / settings.php at the destination database (salts
preserved; `drush cr` when available), maintenance lifted on every exit
path (`unlock` recovers crashes). EventMigrate history lands per converged
site on both hosts; a converged run exits 0 and points at `rehost
cutover` — the read-only go-live checklist (destination HTTP probe via a
dial override, live DNS records + TTL advice, MX-at-source warning, SSL
note, source crontab). Phase 3 is feature-complete; field validation
(docs/TODO.md §1) is the gate before anything ships.

Session decisions (2026-07-27): binary/CLI name is `rehost` and the domain
will be `rehost.sh`; the GitHub repo is `pietervanleuven/rehost` and the module
path is `github.com/pietervanleuven/rehost` (the local checkout dir is still
`rehost-cli` — harmless, git does not care); secrets are never stored —
migrate.yaml has no field that can hold one, passwords are prompted at runtime.

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
  DB tooling, transfer strategy, disk space); exits non-zero while blockers
  remain.
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

- `cmd/rehost` — thin main; version via goreleaser ldflags.
- `internal/cli` — cobra commands; output mode (styled/plain/JSON) resolved from
  `--json`/`--no-color`/`NO_COLOR`/TTY in one place (`options.outputMode`).
- `internal/ssh` — `Config.Resolve()` honors `~/.ssh/config` (ProxyJump = honest
  error); `Dial` auth chain agent → keys → password prompts via the `Prompter`
  interface; known_hosts strict + TOFU (key mismatch always hard-fails); `Run`
  (non-zero exit ≠ Go error); `Probe` = one sentinel-delimited POSIX script with
  a per-command sequential fallback for restricted shells.
- `internal/project` — migrate.yaml schema v1, strict decode (unknown/secret
  fields rejected with guidance), atomic 0600 writes.
- `internal/tui` — `Renderer` (styled/plain/JSON) + `HuhPrompter`/
  `NonInteractivePrompter` + the init/plan wizard forms (huh stays out of cli);
  tui imports ssh, never the reverse.
- `internal/detect` — framework discovery over an `FS` abstraction (shell-based
  `SSHFS` + local for tests): marker `Find` with walk fallback, `Scan`,
  realpath de-dup.
- `internal/recipe` — pluggable framework recipes (drupal, wordpress, static):
  detection fingerprints, destination `Requirements` (min PHP, extensions,
  needs-DB), layered credential extraction (framework CLI → PHP
  echo-helper with sentinel → config regex; transport errors abort, tool
  failures fall through), and the `Maintainer` seam (same layering;
  per-site tool failures are typed `ErrMaintenanceTool` so callers keep
  going).
- `internal/searchreplace` — pure serialized-safe replacement core
  (wp search-replace --precise semantics, fuzzed round-trip invariant) +
  the URL/docroot replacement-pair planner + `RewriteDump`, which applies
  pairs inside a SQL dump's string literals (the local application point
  migrate uses between dump and import).
- `internal/db` — `Credentials` (Password excluded from JSON, in-memory only)
  + the `Extractor` seam recipes implement; `Inspect` learns version, size,
  charset and table counts in one round trip, feeding the password to mysql
  via a defaults file on stdin (never argv/env); `Import` streams a verified
  local dump into the destination's mysql, password over a 0600 FIFO.
- `internal/check` — pure compatibility rule engine (`Run(Input) []Result`,
  blockers vs warnings) + best-effort remote gatherers (php -m, df, du);
  all remote I/O stays in the caller or behind the `runner` seam.
- `internal/dns` — read-only domain snapshot (A/AAAA/CNAME/MX/NS/TXT + TTLs,
  MX targets resolved to IPs) over miekg/dns using the system resolvers;
  rehost never changes DNS.
- `internal/inventory` — per-site size picture over `du` (total, largest
  subdirectories, framework cache/backup dirs worth excluding), best-effort.
- `internal/transfer` — tar-pipe throughput measurement (capped sample over
  the pipe the real migration would use) + file manifests (size/mtime via
  GNU `find -printf`, paths-only degradation, pure `Diff`, atomic gzipped
  persistence) + `Sync`, the manifest-driven tar-pipe relay (delta-only
  transfer through the orchestrator, opt-in deletions with path-safety
  guards, post-sync destination manifest as the convergence proof).
- `internal/db` dump side: `Dump` streams `mysqldump | gzip` while gunzipping
  in memory to verify the completion footer — the shell reports gzip's exit,
  not mysqldump's, so the footer is the truncation guard. `DumpPHP` is the
  same contract via a PHP helper (mysqli → PDO, gzip from PHP itself, creds
  over stdin) for hosts without mysqldump. `ssh.Client.Stream` is the
  streaming exec primitive (`Run` wraps it).
- `internal/state` — append-only run history in `<home>/.rehost/` on the
  source (JSON lines, corrupt lines skipped on read); feeds status/history
  in Phase 3.

## Key Decisions (do not relitigate without the user)

- **Language: Go** (not PHP) — see PLAN.md §2 for the full rationale (Deployer's PHP-SSH
  failure, rclone precedent, Charm TUI, single-binary distribution).
- **Command flow:** `init` (wizard, both hosts) → `check` (compatibility gate, rerunnable
  until green) → `plan` (deep scan + dry-run) → `migrate` (execute) → cutover report.
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
  - Scopes (once code exists, mirror package names): `ssh`, `detect`, `db`, `transfer`,
    `tui`, `project`, `dns`, `recipe/drupal`, `recipe/wordpress`, … Docs-only changes:
    `docs: …` without scope is fine.
  - Breaking changes: `!` after type/scope (`feat(ssh)!: …`) + `BREAKING CHANGE:` footer.
  - Release automation will be built on top of this later (semver derivation,
    changelog) — assume commit messages are machine-parsed, so follow the format
    strictly from the very first commit.
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
- Name collision warning: the binary/repo name "migrate-cli" collides with
  golang-migrate's `migrate`; MARKETING.md §5 recommends renaming to `rehost`
  (fallback: `decamp`) but the decision isn't final — do not publish/register
  anything under any name without checking with the user.
