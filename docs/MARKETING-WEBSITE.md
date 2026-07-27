# rehost.sh — Marketing Website: Design & Implementation Plan

> Companion to [MARKETING.md](MARKETING.md) (positioning §3, GTM §4, naming §5) and
> [PLAN.md](PLAN.md) (flow §4, features §5). Planning only — no implementation yet.
> Drafted 2026-07-27.

Decision recorded here: **the domain is `rehost.sh`** (user decision, 2026-07-27). This
effectively pins the `rehost` name from MARKETING.md §5. Still outstanding before anything
goes live: trademark sanity check, GitHub owner/org name (module path still
`placeholder/rehost`), and whether to also register `rehost.dev` as a defensive redirect.

---

## 1. Goals & non-goals

The website exists to do four things, in priority order:

1. **Convert a skeptical visitor into a first `rehost plan` run** — the demo moment.
   Everything on the home page serves this.
2. **Win the long-tail searches** people actually make when they want to leave a host
   ("migrate wordpress to new host ssh", "move drupal site to another server").
3. **Be the trust anchor**: open source, local, auditable, no cloud middleman. The site
   must *embody* this (no invasive tracking, no dark patterns), not just claim it.
4. **Carry the monetization surfaces that stay out of the CLI** (MARKETING.md §1.5):
   curated host list with disclosed affiliate links, concierge-migration service page.

Non-goals (for launch): pricing pages, user accounts, newsletter machinery, a blog with a
publishing cadence we can't sustain. No SaaS smell.

## 2. Domain strategy

- **`rehost.sh` is both the website and the install host.** Root serves HTML; the install
  script lives at a path: `curl -fsSL rehost.sh/install | sh`.
  - Rejected: user-agent sniffing on `/` (curl gets script, browser gets HTML). Clever but
    surprising, cache-hostile, and it undermines the "auditable" story — people should be
    able to open the exact URL they pipe to `sh` in a browser and read it.
- Subdomains: none at launch. Docs live at `rehost.sh/docs` (path, not `docs.` subdomain —
  keeps all SEO authority on one origin).
- Consider registering `rehost.dev` as a 301 → `rehost.sh` (cheap defense; MARKETING.md
  suggested both). Decide before launch; not a blocker.
- Email on the domain (`hello@`, `security@`) with SPF/DKIM/DMARC set up even if
  send-only — a `.sh` domain with no mail auth records is a phishing-report magnet.

## 3. Taglines

**Primary (hero):**

> **Move any website between hosts. Two commands. Your data never leaves your servers.**

Sub-line: `rehost plan` shows exactly what will happen. `rehost migrate` makes it so.
Open source, runs on your machine.

**Candidates bank** (for A/B, social cards, README, talk titles):

| Tagline | Use |
|---|---|
| "Terraform for website migrations" | Category line for dev audiences (HN, Lobsters) — instantly explains plan/apply |
| "Leave your host. Take everything." | Anti-lock-in emotional hook, social cards |
| "The last migration you'll do by hand" | Retargeting freelancers/agencies |
| "Plesk Site Import, without Plesk" | Comparison pages only (assumes panel knowledge) |
| "`ssh` in, `rehost` out" | T-shirt / sticker tier, not the hero |
| "Switch hosts like you switch branches" | Dev-adjacent alternative to the Terraform line |

Rules: the hero must contain "open source" and the local/no-cloud promise near it — that
pairing is the differentiator (MARKETING.md §1.1). Never promise "one-click" or "zero
downtime" (we do maintenance windows honestly; PLAN.md §4).

## 4. Site map & content inventory

### Launch (ships with v0.1 announcement)

| Page | Purpose | Content notes |
|---|---|---|
| `/` Home | Convert to first run | Hero tagline; **asciinema demo** of a real migration (the §4 GTM "show moment"); the 4-step flow diagram (init → check → plan → migrate → cutover); install block (script + brew + direct download tabs); "how it stays safe" trio (dry-run plan, idempotent reruns, maintenance-mode with crash-safe cleanup); framework support table (Drupal, WordPress, static now; Laravel/Joomla soon); GitHub stars badge; comparison teaser table from MARKETING.md §3 |
| `/install` | Human-readable install page | All methods with copy buttons; checksums and verification instructions; links to `/install` script source. The script itself served at same path with `text/x-sh` for curl (content negotiation or a `/install.sh` canonical) |
| `/docs` | Docs shell | Getting started, command reference (init/check/plan/migrate/cutover/status/unlock), how detection works, what migrates and what doesn't (the MVP scope guard: mail/DNS/SSL/cron = report with instructions — say this loudly, it preempts the #1 disappointment), troubleshooting, migrate.yaml reference ("no secrets are ever stored" gets its own callout) |
| `/faq` | Trust + SEO | See §6 below; marked up as `FAQPage` schema |
| `/security` | Trust anchor | Where credentials go (agent → keys → prompt, never stored), known_hosts strict + TOFU, what the tool executes on your servers, how to audit it, responsible disclosure, link to `security.txt` |
| `/hosts` | Compatibility matrix | Community-maintained "tested hosts" table (GTM §4.4) — even mostly-empty at launch, it invites contribution; later carries the disclosed affiliate/"verified destination" placements |

### Fast-follow (weeks after launch)

| Page | Purpose |
|---|---|
| `/guides/<source>-to-<dest>` | The long-tail SEO engine (GTM §4.2): "Migrate WordPress from GoDaddy to SiteGround over SSH" etc. Template-driven from one canonical guide + host-specific deltas (SSH quirks, panel screenshots, DB naming). Start with 5–10 hand-written ones; never auto-generate thin pages |
| `/compare/duplicator`, `/compare/blogvault`, `/compare/wp-migrate`, `/compare/diy-rsync` | One page per competitor class using the MARKETING.md §3 table; honest, feature-matrix style — these rank for "X alternative" |
| `/services` | Concierge migrations (stream #2) — "hit a blocker? we'll finish it" |
| `/changelog` | Release notes (mirrors GitHub Releases), RSS feed |

## 5. Distribution: direct download vs brew — **direct download is required**

Brew-only is not viable, for four reasons:

1. **Homebrew-core won't take us at launch.** Core has notability bars (stars, age,
   maintenance history). Day-one brew means a custom tap:
   `brew install <owner>/tap/rehost` — real friction, and it still excludes everyone below.
2. **Our audience is substantially Linux-and-server.** Freelancers running the tool from a
   VPS, CI jobs, and Windows/WSL users don't have brew. Migrations are often run from a
   third machine that is *not* a Mac laptop.
3. **goreleaser already produces the artifacts.** Cross-platform archives on GitHub
   Releases are effectively free — the direct download costs nothing extra.
4. **Trust and auditability.** Direct artifacts with checksums (and later signatures) are
   the only path a security-conscious user will accept; `.sh`-domain curl-pipe alone
   raises eyebrows, brew alone hides the artifact.

**Install matrix to offer (in this display order):**

| Method | Command | When |
|---|---|---|
| Install script | `curl -fsSL rehost.sh/install \| sh` | Launch. Script must: detect OS/arch, download from GitHub Releases, **verify checksum**, install to `~/.local/bin` or `/usr/local/bin` with clear messaging, support `REHOST_VERSION` pin. Readable at the same URL in a browser |
| Direct download | GitHub Releases (darwin/linux/windows × amd64/arm64, `checksums.txt`) | Launch — this is the source of truth all other methods wrap |
| Homebrew tap | `brew install <owner>/tap/rehost` | Launch (goreleaser generates the formula) |
| `go install` | `go install github.com/<owner>/rehost/cmd/rehost@latest` | Launch — free, serves the Go crowd |
| homebrew-core, Scoop/winget, deb/rpm repos, AUR | — | Post-traction (each listing is a discovery channel, GTM §4.3, but each is a maintenance commitment) |

Blocked on: **the GitHub owner decision** (`placeholder/rehost`). The install script,
formula, and `go install` path all bake in the final URL — decide before the site ships.

Don't forget: signed checksums (minisign or cosign) and SBOM generation are cheap with
goreleaser — worth doing from v0.1 so the "auditable" page has teeth. macOS Gatekeeper:
either notarize or document the quarantine workaround; brew/script installs sidestep it.

## 6. FAQ (draft content — also the `FAQPage` schema source)

Trust cluster:
- **Where do my SSH credentials go?** Nowhere. rehost runs on your machine and connects
  directly to your two hosts. Credentials come from your SSH agent, key files, or a prompt;
  `migrate.yaml` has no field that can hold a secret. Nothing is sent to us — there's no
  server to send it to.
- **Is it really free? What's the catch?** The CLI is Apache-2.0 open source and single-site
  migration will always be free (see the trust rules in our docs). We plan to earn money
  around it later — from hosts and agencies, not from you.
- **Do you have access to my site or database?** No. There is no cloud component. You can
  read the source to verify.
- **What does it execute on my hosts?** Only capability-probed, POSIX-safe commands; `plan`
  shows you everything before `migrate` touches anything. The dry-run output is the contract.

Capability cluster:
- **Which frameworks are supported?** Drupal (7/8+/multisite), WordPress (incl. multisite),
  and static sites today; Laravel, Joomla, and generic PHP+MySQL next. (Table, kept in
  sync with PLAN.md §3.)
- **Does my host need root / a control panel / anything installed?** No panel, no root, no
  agent installed on either host. You need SSH access on both ends; rehost probes for
  `rsync`/`mysqldump`/framework CLIs and falls back when they're missing.
- **What about my email / DNS / SSL / cron jobs?** rehost migrates files + database. For
  everything else it produces a cutover report with exact instructions (current DNS + TTLs,
  MX warnings, SSL re-issue steps, your crontab). Deliberately: half-automating mail
  migration is how sites get hurt.
- **Will my site go down?** The bulk copy runs while your site stays live. Only the final
  delta + DB dump happens inside a short framework-native maintenance window, and `unlock`
  recovers it even after a crash.
- **What happens if the migration fails halfway?** Rerun it. Every step is idempotent —
  rerunning converges on "destination matches source" and only transfers what's missing.
- **My host only offers SFTP/FTP, no SSH.** Not yet supported; it's on the roadmap
  (PLAN.md §5.3). SSH shell access on both hosts is required today.
- **Windows?** The CLI runs on Windows (native binary or WSL) and migrates between
  Linux hosts. The hosts themselves must be Unix-like.

Comparison cluster:
- **How is this different from Duplicator / All-in-One WP Migration?** No plugin install, no
  wp-admin, no PHP upload limits — and it isn't WordPress-only.
- **How is this different from Migrate Guru / BlogVault?** Their cloud relays your site's
  data; rehost moves it host-to-host with nothing in between.
- **Why not just rsync + mysqldump myself?** You can — rehost adds what your script doesn't
  have: auto-detection, a dry-run plan, serialized-safe search-replace, charset-correct
  dumps, idempotent reruns, and crash-safe maintenance mode.

## 7. Findability (SEO/GEO) requirements

**Keyword strategy:**
- Head terms we won't win soon: "website migration" (SaaS incumbents own it). Skip.
- Winnable intent terms: "migrate wordpress to new host ssh", "move drupal site to another
  server", "migrate website between shared hosts", "site migration cli", "X to Y migration"
  per-host pairs (the `/guides/` engine — each pair is a page, effectively zero
  competition), "<competitor> alternative".
- Brand term: "rehost cli" — use it consistently (site title, README, release names)
  because bare "rehost" is a generic word we'll never own (MARKETING.md §5).

**Technical requirements (launch checklist):**
- Static HTML, no client-side rendering for content; Core Web Vitals green by construction.
- One `<h1>` per page, real meta descriptions, canonical URLs, `sitemap.xml`, `robots.txt`.
- Structured data: `SoftwareApplication` (home), `FAQPage` (/faq), `HowTo` (guides),
  `BreadcrumbList` (docs/guides).
- OpenGraph/Twitter cards with generated per-page images (guides get "GoDaddy → SiteGround"
  visual cards — they get shared in forum answers).
- **`llms.txt` + clean markdown availability of docs** — in 2026, "how do I migrate my
  site" is asked to assistants as often as to Google; being the citable, scrapeable answer
  is the new SEO (GEO). Docs pages should be reachable as `.md`.
- Search Console + Bing Webmaster registered from day one; privacy-friendly analytics only
  (Plausible or GoatCounter, self-hosted or EU-hosted; no cookie banner needed — matches
  the trust positioning and our EU base).
- The GitHub README is a landing page too: same hero, demo GIF, install block, deep links
  into rehost.sh — most dev-tool traffic decides there and never reaches the site.
- Backlink flywheel: every package-manager listing, the compatibility matrix (contributors
  link to it), and answering real questions on r/webhosting / Stack Overflow with guide
  links (sparingly, honestly).

## 8. Design direction

- **Terminal-first aesthetic**: the product's own Charm/lipgloss output *is* the brand.
  Dark theme default with light mode support; monospace accents; real (not mocked)
  terminal output everywhere — screenshots must be reproducible from actual runs.
- The asciinema recording is the centerpiece; autoplay muted-style (CSS-paused until
  visible), with a copyable command transcript below for accessibility and SEO.
- Color: pick a two-color identity (suggest: terminal green/amber on near-black) and reuse
  it in the CLI's lipgloss theme so tool and site are visibly the same thing.
- No stock photos, no illustration-of-people-at-laptops. Diagrams in the PLAN.md §4 ASCII
  style, redrawn cleanly.
- Accessible: WCAG AA contrast, keyboard navigable, `prefers-reduced-motion` respected.

## 9. Implementation plan

**Stack recommendation: Astro + Starlight** (docs) on **Cloudflare Pages**.
- Astro: static output, content collections fit the guides/compare template model,
  MD/MDX authoring, zero JS by default.
- Starlight gives docs search (Pagefind), sidebar, dark mode, i18n-ready for free.
- Cloudflare Pages: free tier, previews per PR, and trivial header/redirect rules for
  serving `/install` as a shell script. (Alternative: Hugo if we want Go-ecosystem purity —
  but Starlight's docs ergonomics win; either is fine, decide once.)
- Repo: separate `website` repo under the same GitHub owner (keeps CLI repo lean; docs
  versioning not needed pre-1.0).

**Phasing (tied to product phases, PLAN.md §6):**

| Site phase | Ships when | Scope |
|---|---|---|
| W0 — Claim | Now (domain bought) | Parked page: logo-less wordmark, one-liner, GitHub link, `security.txt`, mail DNS records. No waitlist theater |
| W1 — Launch site | With v0.1 "show moment" (product Phase 3 exit) | Everything in §4 "Launch": home + demo, install (script + releases + tap), docs, FAQ, security, empty-but-inviting hosts matrix. This is the Show-HN target |
| W2 — Growth | Weeks after launch | First 5–10 migration guides, comparison pages, changelog + RSS, services page |
| W3 — Monetization surfaces | Post-1.0, gated per MARKETING.md §6 | Affiliate-labeled host list, verified-destination program page, agency-tier waitlist (only after the 25-prepay gate conversation) |

**Definition of done for W1:** a stranger on a slow connection can go from first pageview
to a completed `rehost plan` against their own site in under 5 minutes, without leaving
the docs.

## 10. Must-not-forget checklist

Legal/compliance (EU base):
- [ ] Privacy policy (short — we collect nothing; analytics disclosure) and legal notice/imprint
- [ ] Affiliate disclosure page *before* the first affiliate link exists (trust rule, §1.5)
- [ ] "Not affiliated with the hosts named" disclaimer on guides/matrix; use host names nominatively only
- [ ] Trademark screen on "rehost" in software classes before spending on branding
- [ ] License page: Apache-2.0, and the trust guard in writing ("single-site migration free forever, no phone-home by default") — publishing the promise makes it credible

Security/trust:
- [ ] `security.txt` + `security@rehost.sh` from day W0
- [ ] Install script: checksum-verified, readable in browser, versioned, no `sudo` by default
- [ ] Checksums + signing (minisign/cosign) + SBOM in goreleaser config
- [ ] DNSSEC on the domain; SPF/DKIM/DMARC even if send-only; CAA records

Blockers to resolve before W1:
- [ ] **GitHub owner/org name** — bakes into install script, tap, `go install`, module path (`placeholder/rehost` grep-rename)
- [ ] Decide `rehost.dev` defensive registration
- [ ] Reserve the name on package registries we'll use later (brew tap name, scoop bucket) once the owner exists

Content quality gates:
- [ ] Every terminal screenshot/cast reproducible from a real run (no mockups)
- [ ] The "what does NOT migrate" section is prominent, not buried — honesty about mail/DNS/SSL is a differentiator, not a weakness
- [ ] Claims audit before launch: nothing promises zero-downtime, one-click, or "all frameworks"
- [ ] FAQ answers match actual CLI behavior (recheck at each release; FAQ drift = trust rot)

Measurement (maps to MARKETING.md §6):
- [ ] Plausible goals: install-script fetches, GitHub outbound clicks, docs "getting started" completion
- [ ] GitHub Releases download counts + tap analytics as the adoption metric
- [ ] Search Console queries review monthly → feeds which `/guides/` pages to write next

## 11. Open questions (for the user)

1. GitHub owner/org name — the single blocker for install URLs (module path rename ready via grep).
2. Register `rehost.dev` too, or `rehost.sh` only?
3. Website repo: same org `website` repo (recommended) or in-repo `/website` dir?
4. Who receives `hello@`/`security@` mail (personal vs shared inbox)?
5. W0 parked page: publish as soon as the domain resolves, or wait until closer to v0.1?