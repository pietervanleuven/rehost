# TODO

The living work list. [PLAN.md](PLAN.md) stays the spec and source of truth
for *what* and *why*; this file tracks *what's next and what's known to be
unfinished*. Check items off rather than deleting them; prune a section when
it is fully done. Keep entries honest — a gap nobody wrote down is a gap
that ships.

Status snapshot (2026-07-27): Phases 0–2 are feature-complete
(`init` / `check` / `plan` incl. `--dry-run` collection). Nothing has run
against a real shared host end-to-end yet.

## 1. Field validation — the current gate (Phase 1 & 2 exit criteria)

Everything below is built and unit-tested but has never touched a real
shared host. Run `rehost plan --dry-run` and `rehost check` against real
hosting and verify:

- [ ] **WordPress site on shared hosting** (the plesk03 source in the local
      migrate.yaml is ready-made for this): detection, version, config file,
      credentials — zero manual input beyond SSH credentials
- [ ] **Drupal site on shared hosting** (maintainer's daily driver;
      multisite if available)
- [ ] `check` produces a sensible verdict and correctly flags a genuinely
      incompatible destination (e.g. missing PHP extension) — try to
      provoke one
- [ ] The **mysql invocation** (`--defaults-extra-file=/dev/stdin` heredoc)
      works against a real client/server — never yet run outside unit tests
- [ ] The **dump restores**: `gunzip < .rehost/dumps/<db>.sql.gz | mysql`
      into a scratch database, compare table counts
- [ ] The **PHP dump fallback** against a real database (locally only
      syntax- and quoting-tested — no MySQL on the dev machine)
- [ ] **Manifest convergence**: a second `plan --dry-run` right after the
      first reports a ~zero delta; the find degradation ladder on a
      restricted/busybox host if one is available
- [ ] DNS snapshot + mail warning against the real domain (add `domain:` to
      migrate.yaml)
- [ ] `migrate` file sync between two real hosts: files land byte-exact,
      second run is a ~zero delta, refusal/rerun-exemption behave, and the
      destination history record appears
- [ ] File issues found in the field here as new checkboxes

## 2. Open decisions — do not code around these

- [x] **Destination-state policy** (PLAN.md §7, design questions 4–5) —
      decided 2026-07-29: refuse non-empty destination docroot unless an
      explicit flag opts in (reruns onto a rehost-populated docroot exempt);
      additive sync by default, destination-only files reported and deleted
      only with `--delete`. Phase 3 is unblocked.
- [x] **Module-path rename**: done — module path is
      `github.com/pietervanleuven/rehost`, matching the GitHub repo
      (`pietervanleuven/rehost`). Nothing else gets published/registered
      under any name without checking (AGENTS.md).
- [ ] Secret storage stays "prompt at runtime, never store" unless field
      validation shows real pain (PLAN.md §7).

## 3. Phase 3 — migrate MVP (PLAN.md §6; unblocked — policy decided 2026-07-29)

- [x] Pre-flight = re-run `check` + source DB reachable + destination-state
      policy (refuse non-empty docroot unless rehost-created per destination
      history, or `--onto-existing`)
- [x] File sync: manifest-driven delta over a tar-pipe relay through the
      orchestrator (topology decided 2026-07-29, PLAN.md §5), gzip/NUL-list
      capability-gated, opt-in `--delete` with path-safety guards,
      EventMigrate history on both hosts; SFTP last resort still open, direct
      host-to-host rsync deferred — incremental on rerun
- [x] Maintenance-mode primitives + `unlock` recovery command: Maintainer
      recipe seam (WP wp-cli→file, Drupal drush→direct-DB fallback, static
      no-op), write-ahead EventMaintenance records, live-probe-first unlock
- [ ] Maintenance window in `migrate`: enable around final dump + delta
      pass, crash-safe cleanup on every exit path
- [x] DB import on destination: `db.Import` streams the footer-verified
      local dump into `gunzip -c | mysql` (password over a 0600 FIFO,
      never argv/env/disk; progress from local byte offsets; post-import
      table-count verification) — not yet wired into `migrate`
- [x] Serialized-safe search-replace core: `internal/searchreplace`
      (`--precise` semantics, fuzzed round-trip, URL/docroot pair planner)
      — application to the imported DB not yet wired
- [ ] Wire dump→import→search-replace into `migrate` (the choreography:
      maintenance on → final dump → delta pass → import → search-replace →
      maintenance off); use `wp`/`drush` search-replace remotely when
      present, the own core as fallback
- [ ] Config rewrite: wp-config.php; Drupal settings.php incl.
      `trusted_host_patterns` + `hash_salt`; `drush cr` post-import
- [x] `status` / `history` commands over `.rehost/history.jsonl`: read-only,
      newest-first, styled/plain/JSON (`rehost.history.v1` /
      `rehost.status.v1`), empty history = exit 0
- [ ] Cutover report: DNS instructions with current TTLs, MX warning, SSL
      re-issue note, crontab listing
- [ ] Post-migration checks: HTTP smoke test via hosts-override, file/table
      count diffs
- [ ] Idempotency proof: second `migrate` run is fast and changes nothing

## 4. Known gaps & deferred polish (fine to ship Phase 3 without)

- [ ] Manifest has no checksums yet — size+mtime only (rsync's quick check);
      optional checksum mode for paranoid mode later
- [ ] Drupal multisite: credentials/dump/manifest cover the default site
      only; per-subsite `--uri` and per-site databases unhandled
- [x] PHP dump fallback now dumps views (after base tables), triggers and
      routines for parity with the mysqldump path (`--routines --triggers`;
      neither path dumps events); missing privileges degrade to
      `-- rehost: skipped …` comments, footer still means complete
- [ ] `plan` rewriting migrate.yaml drops hand-written YAML comments
      (yaml.v3 re-encode) — comment-preserving writes or a separate state
      file
- [ ] `check` charset rule uses the destination mysql *client* version as a
      proxy for the server — replace with a real server check once migrate
      can create the destination database
- [ ] DNS "lower your TTL now" advice: done in `check` output (`dns.ttl`
      warning above 3600s, ready-confirmation at ≤300s); still to add to
      the cutover report once that exists (Phase 3)
- [ ] TUI: reports are static text; the bubbletea live checklist from the
      PLAN is deferred until the output stabilizes
- [ ] `.rehost/history.jsonl` on the source grows unbounded — rotation or
      pruning eventually
- [x] golangci-lint installed locally (2.12.2 via Homebrew); the whole
      repo lints clean — write-path Close errors are now checked, deliberate
      best-effort calls are explicit `_ =`
- [ ] Extension requirement lists (recipe/requirements.go) are a pragmatic
      first cut — revisit against real framework docs when field failures
      appear

## 5. Later phases (pointers only — see PLAN.md §6)

- Phase 4: Laravel recipe, restricted-shell fallback matrix, retry/resume
  polish, exclusion presets, docs site, install script, integration rig
- Phase 5+: multisite fleets, `history`/`cutover` polish, partnerships
  (MARKETING.md); marketing site plan lives in MARKETING-WEBSITE.md
