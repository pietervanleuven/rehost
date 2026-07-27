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
- [ ] File issues found in the field here as new checkboxes

## 2. Open decisions — do not code around these

- [ ] **Destination-state policy** (PLAN.md §7, design questions 4–5):
      what `migrate` does with a non-empty destination docroot
      (refuse / overwrite / backup-then-write), and whether convergent sync
      deletes destination-only files. **Blocks Phase 3** — raise before
      writing any `migrate` semantics.
- [ ] **Name + GitHub owner**: module path is `github.com/placeholder/rehost`
      until decided (grep `placeholder/rehost` to rename). Nothing gets
      published/registered under any name without checking (AGENTS.md).
- [ ] Secret storage stays "prompt at runtime, never store" unless field
      validation shows real pain (PLAN.md §7).

## 3. Phase 3 — migrate MVP (PLAN.md §6; blocked on decision 2.1)

- [ ] Pre-flight = re-run `check` + DB reachable as `migrate`'s first step
- [ ] File sync: rsync-over-SSH when both ends have it; manifest-driven
      delta via tar-pipe else; SFTP last resort — all incremental on rerun
- [ ] Maintenance mode on the source around final dump + delta pass;
      crash-safe cleanup; `unlock` recovery command
- [ ] DB import on destination (charset-correct) + serialized-safe
      search-replace (port `wp search-replace` semantics; use `wp`/`drush`
      remotely when present)
- [ ] Config rewrite: wp-config.php; Drupal settings.php incl.
      `trusted_host_patterns` + `hash_salt`; `drush cr` post-import
- [ ] `status` / `history` commands over `.rehost/history.jsonl` (the state
      package already reads it)
- [ ] Cutover report: DNS instructions with current TTLs, MX warning, SSL
      re-issue note, crontab listing
- [ ] Post-migration checks: HTTP smoke test via hosts-override, file/table
      count diffs
- [ ] Idempotency proof: second `migrate` run is fast and changes nothing

## 4. Known gaps & deferred polish (fine to ship Phase 3 without)

- [ ] Give the remaining dry-run collectors the manifest-grade rigor pass
      from 79c397c (exit-code semantics, odd filenames, partial output):
      `inventory` still TrimSpaces paths, splits on newlines, ignores exit
      codes, and its `du -sk <root>/*/` breakdown misses dot-directories;
      `transfer.Throughput` ignores tar's exit status once bytes flowed.
      Lower stakes than manifests (informational, not synced from) — but
      the exclusion advice feeds real transfers in Phase 3.
- [ ] Manifest has no checksums yet — size+mtime only (rsync's quick check);
      optional checksum mode for paranoid mode later
- [ ] Drupal multisite: credentials/dump/manifest cover the default site
      only; per-subsite `--uri` and per-site databases unhandled
- [ ] PHP dump fallback skips views and does not dump routines/triggers
      (the mysqldump path does) — document or close the gap
- [ ] `plan --dry-run --json` emits two JSON documents (capability +
      dry-run) — consider a single envelope in a v2 schema
- [ ] `plan` rewriting migrate.yaml drops hand-written YAML comments
      (yaml.v3 re-encode) — comment-preserving writes or a separate state
      file
- [ ] `check` charset rule uses the destination mysql *client* version as a
      proxy for the server — replace with a real server check once migrate
      can create the destination database
- [ ] DNS: no "lower your TTL now" advice yet; belongs in check output and
      the cutover report
- [ ] TUI: reports are static text; the bubbletea live checklist from the
      PLAN is deferred until the output stabilizes
- [ ] `.rehost/history.jsonl` on the source grows unbounded — rotation or
      pruning eventually
- [ ] golangci-lint is not installed on the dev machine (CI covers it) —
      install locally for parity
- [ ] Extension requirement lists (recipe/requirements.go) are a pragmatic
      first cut — revisit against real framework docs when field failures
      appear

## 5. Later phases (pointers only — see PLAN.md §6)

- Phase 4: Laravel recipe, restricted-shell fallback matrix, retry/resume
  polish, exclusion presets, docs site, install script, integration rig
- Phase 5+: multisite fleets, `history`/`cutover` polish, partnerships
  (MARKETING.md); marketing site plan lives in MARKETING-WEBSITE.md
