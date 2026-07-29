# TODO

The living work list. [PLAN.md](PLAN.md) stays the spec and source of truth
for *what* and *why*; this file tracks *what's next and what's known to be
unfinished*. Check items off rather than deleting them; prune a section when
it is fully done. Keep entries honest — a gap nobody wrote down is a gap
that ships.

Status snapshot (2026-07-29): Phases 0–3 are feature-complete
(`init` / `check` / `plan` / `migrate` / `cutover` + `status` / `history` /
`unlock`). Nothing has run against a real shared host end-to-end yet —
field validation (§1) is the gate before anything ships.

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
- [ ] Full `migrate` with a `dest_db`: maintenance window visible on the
      source, dump→rewrite→import lands a working DB, config rewrite boots
      the site, second run is fast and ~zero (the idempotency proof), and
      `rehost cutover`'s smoke test passes pre-DNS
- [ ] Dest-root rebasing (no `dest_root` → home-relative path on the
      destination) lands files where the destination account actually
      serves them; `--delete` stand-down on a pruned (find exit 1) source
      listing behaves sanely on a host with unreadable directories
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
- [x] Maintenance window in `migrate`: enabled around the final dump +
      delta pass (write-ahead EventMaintenance record first), lifted on
      every exit path on a cancellation-proof context; failures point at
      `rehost unlock`
- [x] DB import on destination: `db.Import` streams the footer-verified
      local dump into `gunzip -c | mysql` (password over a 0600 FIFO,
      never argv/env/disk; progress from local byte offsets; post-import
      table-count verification) — wired into `migrate` 2026-07-29
- [x] Serialized-safe search-replace core: `internal/searchreplace`
      (`--precise` semantics, fuzzed round-trip, URL/docroot pair planner)
      — applied via `RewriteDump` to the dump stream locally between dump
      and import (no destination framework CLI needed), wired 2026-07-29
- [x] Wire dump→import→search-replace into `migrate` (2026-07-29):
      per-site `dest_db` block names the panel-created destination database
      (password prompted at runtime; verified in pre-flight — never CREATE
      DATABASE; non-empty databases refused unless rehost-created or
      `--onto-existing`, mirroring the docroot rule). Choreography:
      maintenance on → final dump → file delta pass → serialized-safe
      docroot rewrite of the dump → import + table-count verify →
      maintenance off. Remote `wp`/`drush` search-replace deferred — the
      local dump rewrite needs no destination CLI and runs before config
      rewrite exists; revisit if field data demands the remote path
- [x] Config rewrite (2026-07-29): `ConfigRewriter` recipe seam;
      wp-config.php DB defines and settings.php `$databases` entries spliced
      in place on the destination (everything else — `hash_salt`, salts,
      custom code — byte-exact; same-domain migrations keep
      `trusted_host_patterns` valid as-is); `drush cr` runs post-rewrite
      when the destination has drush, guidance otherwise; a config outside
      the docroot (unsynced) degrades to by-hand instructions
- [x] `status` / `history` commands over `.rehost/history.jsonl`: read-only,
      newest-first, styled/plain/JSON (`rehost.history.v1` /
      `rehost.status.v1`), empty history = exit 0
- [x] Cutover report (2026-07-29): `rehost cutover` — read-only go-live
      checklist with live DNS records + TTL-lowering advice, destination IP,
      MX-at-source warning, SSL note, source crontab listing, and per-site
      file counts from the persisted post-sync manifests
- [x] Post-migration checks (2026-07-29): `cutover` probes the destination
      over HTTP(S) with a dial override (hosts-file semantics without
      editing hosts; TLS unverified on purpose — pre-cutover cert); a
      failing probe leads the checklist with FIX FIRST. Table counts are
      verified at import time. Deeper diffs = field-validation material
- [ ] Idempotency proof — moved to the field-validation gate (§1): the
      convergent design is unit-tested; "second run is fast and ~zero" is
      only provable against real hosts

## 4. Known gaps & deferred polish (fine to ship Phase 3 without)

- [ ] Manifest has no checksums yet — size+mtime only (rsync's quick check);
      optional checksum mode for paranoid mode later
- [ ] Drupal multisite: credentials/dump/manifest cover the default site
      only; per-subsite `--uri` and per-site databases unhandled
- [x] PHP dump fallback now dumps views (after base tables), triggers and
      routines for parity with the mysqldump path (`--routines --triggers`;
      neither path dumps events); missing privileges degrade to
      `-- rehost: skipped …` comments, footer still means complete
- [x] `plan` rewriting migrate.yaml preserves hand-written YAML comments
      (2026-07-29): `File.Save` splices values into the existing document's
      YAML tree, keeping key order and comments on every section whose data
      did not change (a changed value subtree — e.g. `sites:` — is swapped;
      a fresh/unparseable file still writes header + full encode)
- [ ] `check` charset rule uses the destination mysql *client* version as a
      proxy for the server — replace with a real server check once migrate
      can create the destination database
- [x] DNS "lower your TTL now" advice: done in `check` output (`dns.ttl`
      warning above 3600s, ready-confirmation at ≤300s) and in the `cutover`
      report (per-A/AAAA record TTL note when above 3600s, advising 300 + one
      old-TTL wait before the flip)
- [ ] TUI: reports are static text; the bubbletea live checklist from the
      PLAN is deferred until the output stabilizes
- [x] `.rehost/history.jsonl` growth is bounded (2026-07-29): `state.Compact`
      rewrites the file (atomic temp+mv) once it passes `CompactThreshold`
      entries, keeping the last `CompactKeepRecent` plus the latest
      EventMigrate/EventMaintenance per site — so `MigratedSites` (refusal
      exemption) and `LockedSites` (unlock recovery) read back unchanged.
      Best-effort, wired at the record-writing tails of `plan --dry-run` and
      `migrate` (both hosts); `Record` itself stays a pristine single append
- [x] golangci-lint installed locally (2.12.2 via Homebrew); the whole
      repo lints clean — write-path Close errors are now checked, deliberate
      best-effort calls are explicit `_ =`
- [ ] Extension requirement lists (recipe/requirements.go) are a pragmatic
      first cut — revisit against real framework docs when field failures
      appear
- [x] `check`'s transfer rule described rsync/SFTP paths that do not exist
      (2026-08-27): it now reports the real manifest-driven tar pipe, and
      missing `tar` or `find` on either host is a blocker (the sync engine
      needs both; there is no other transport)
- [x] `dest_db` was invisible until migrate's pre-flight (2026-08-27):
      `check` now warns per database-backed site without one (`db.dest`),
      plan/check print next-step hints, and the documented flow is
      init → plan → check → migrate → cutover everywhere (PLAN.md §4
      updated — plan persists `sites:` so the user can attach
      `dest_root`/`dest_db` before the gate)
- [x] Dead code and speculative fallbacks removed (2026-08-27): the
      hardcoded `MigrateImplemented` status flag, the sequential probe's
      `which` fallback after `command -v`, and the live tar re-probe on a
      missing version banner (no banner now just means non-GNU: NUL lists
      off, the safe default); the docroot/database destination policies
      share one `destPolicy` verdict function
- [x] README refreshed for the Phase 3 feature set (status, migrate/cutover
      section, truthful check example) and LICENSE added (Apache-2.0,
      2026-08-27)

## 5. Later phases (pointers only — see PLAN.md §6)

- Phase 4: Laravel recipe, restricted-shell fallback matrix, retry/resume
  polish, exclusion presets, docs site, install script, integration rig
- Phase 5+: multisite fleets, `history`/`cutover` polish, partnerships
  (MARKETING.md); marketing site plan lives in MARKETING-WEBSITE.md
