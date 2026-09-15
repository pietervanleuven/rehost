# Idea

A CLI to migrate a website from one shared hosting to another one using SSH.

Goal: provide an easy way to migrate a website from one shared hosting to another one
with as much auto detection as possible.

Main goal: lowering vendor lock for hosting a website.

The tool is universal (framework-agnostic recipes), open source, and runs locally —
no data through third-party clouds. See [PLAN.md](PLAN.md) for the full plan; this
document is the seed idea.

## Flow

`init` → `check` → `plan` → `migrate` → cutover report

- `migrate-cli init`: wizard asking for source **and** destination credentials,
  tests connectivity to both, creates a project file.
- `migrate-cli check`: compatibility gate between source and destination
  (PHP version/extensions, database version/charset, disk space, available tools).
  Rerunnable: fix the destination in its panel, run `check` again until green.
- `migrate-cli plan`: deep scan of the source to determine:
  - website framework (Drupal — priority, main goto CMS — WordPress, static; later Laravel, Joomla, …)
  - database type, credentials (extracted from config), size, dump feasibility
  - file inventory, suggested exclusions
  - IP address, DNS snapshot (we use dig-style lookups), email settings (if applicable)

  Does a dry-run to verify files & database dumps can be collected.
  If issues block. Use a checklist style progress bar table.
  Updates the project file with the migration plan.
- `migrate-cli migrate`: execute the migration and show stats of earlier migrations
  (duration, timestamp, database size, warnings) — written to a hidden folder on the
  source host.
  - **Idempotent**: rerunning is safe and incremental — first run does the bulk copy
    while the site stays live, a rerun near cutover only moves the delta.
  - **Maintenance mode**: enabled on the source before the final database dump +
    delta pass and disabled after (frameworks support this natively: Drupal
    `drush sset`, WordPress `.maintenance`, Laravel `artisan down`), so the
    snapshot is consistent and reruns converge. Crash-safe, with an `unlock`
    recovery command.
- Cutover: heads up for items not migrateable by the tool — host names / DNS
  (with TTL advice), email/MX, SSL, cron jobs.

## First focus

- Detect framework (Drupal, WordPress, static first)
- Analyze files and folders
- Check if db dump can be created
- Source↔destination compatibility check before anything else
- ~~Which language to create the tool in? PHP? Go?~~ → **Go** (see PLAN.md §2)
- ~~Analyze competing tools~~ → done (see PLAN.md §1): no open-source,
  framework-agnostic, SSH-based host-to-host migration CLI exists

## Future possible functionalities

- SFTP/FTP-only mode for hosts without SSH
- Add optimized configurations for platforms like: Plesk, one.com, Combell, Forge, Cloudpanel, etc.
- Auto DNS adjust
