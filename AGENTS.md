# AI Agent Guide — migrate-cli

Canonical agent instructions. `CLAUDE.md` and `GEMINI.md` are symlinks to this file —
edit only `AGENTS.md`.

## Project Overview

An open-source, framework-agnostic CLI that migrates a website from one shared host to
another over SSH, with maximum auto-detection and a Terraform-style plan/apply workflow.
Main goal: lowering vendor lock-in for hosting a website.

- `docs/PLAN.md` — **the living spec**: competitive analysis, stack decision, framework
  support matrix, command flow, feature set, phased plan, risks. Read this first.
- `docs/IDEA.md` — the seed idea; kept consistent with PLAN.md but PLAN.md wins on conflict.
- `docs/MARKETING.md` — business model & go-to-market: open-source local CLI (Apache-2.0)
  as the moat, open-core combo later, positioning ("Terraform for website migrations"),
  launch sequence, naming analysis.

## Current State

Phase 0 (foundation) implemented: cobra skeleton, SSH layer with capability probe,
project file schema v1, CI + goreleaser (snapshot-only). Phase 1 (init wizard,
framework detection, check gate — see PLAN.md §6) is next.

Session decisions (2026-07-27): binary/CLI name is `rehost` (module path
`github.com/placeholder/rehost` until the GitHub owner is decided — grep for
`placeholder/rehost` to rename); secrets are never stored — migrate.yaml has no
field that can hold one, passwords are prompted at runtime.

## Commands

- `make build` / `go build -o rehost ./cmd/rehost` — build the binary.
- `make test` (`go test -race ./...`), `make vet`, `make lint` (golangci-lint),
  `make snapshot` (goreleaser cross-build, publishes nothing).
- `rehost plan [user@host[:port]]` — connect + capability report (the only real
  command); `--json` for machine output, plain text on non-TTY. `init`, `check`,
  `migrate`, `status`, `unlock` are stubs that exit non-zero.

## Architecture (Phase 0)

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
  `NonInteractivePrompter`; tui imports ssh, never the reverse.
- `internal/detect`, `internal/db`, `internal/transfer`, `internal/dns` do not
  exist yet — created in the phase that gives them content.

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
- **MVP scope guard:** the tool migrates site files + database. Mail, DNS, SSL, cron are
  a *report with instructions*, not migration targets.
- **Destination-state policy: UNDECIDED** (PLAN.md §7, design questions 4–5). What
  `migrate` does with a non-empty destination docroot (refuse/overwrite/backup, and whether
  convergent sync deletes destination-only files) and how a half-failed import onto an
  existing destination site is mitigated are open questions — do not assume an answer in
  code or docs; raise with the user when a task touches `migrate` semantics.
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
