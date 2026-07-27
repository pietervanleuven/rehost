# migrate-cli — Marketing & Business Model

> Companion to [PLAN.md](PLAN.md) (competitive landscape §1) and [IDEA.md](IDEA.md).
> Research date: July 2026.

---

## 1. The core strategic question: open-source vs paid, SaaS vs local CLI

### 1.1 The trust constraint decides most of this for us

This tool asks users for **SSH credentials to two hosting accounts** — the keys to their
entire site, database, and often email. A closed-source binary or a SaaS that routes data
through a vendor cloud faces an enormous trust barrier for a new, unknown tool. The
incumbents prove the point in both directions:

- **Migrate Guru / BlogVault** (SaaS model): works well, but your site's data flows through
  their cloud — this is *the* recurring criticism, and PLAN.md lists it as a key limitation.
- **WP-Migrate** (closed-source paid CLI): $39–$149, respected, but WP-only — and its
  closed-source nature is exactly the wedge we can attack.
- **rclone** (the closest spiritual sibling: OSS Go CLI that holds credentials to remote
  systems): earned its trust *because* anyone can audit what happens to their credentials.

Our stated differentiator in PLAN.md is literally *"open source, local, auditable — no data
through third-party clouds."* A paid-closed or SaaS-first model would delete our own
positioning. **The core CLI must be open source and local. This is not the sacrifice — it is
the moat.**

### 1.2 Why a data-plane SaaS is the wrong architecture anyway

- Transfers are host-to-host over SSH; inserting a cloud middleman doubles bandwidth,
  adds a data-processing role under GDPR (relevant: our home market is EU), and recreates
  the exact model (BlogVault) we position against.
- Shared-hosting users are price-sensitive; per-migration SaaS pricing ($10–$50/site) caps
  the audience, and migrations are too infrequent for subscriptions to make sense for
  individuals.

### 1.3 Where money actually exists in this market

| Buyer | What they pay today | Evidence |
|---|---|---|
| Site owners (one-off) | $25–$90 (Fiverr) up to $149–$499 (pro services) per migration | Duplicator & Seahawk 2025/26 pricing surveys |
| Agencies / freelancers | $69–$499/yr for migration plugin licenses (Duplicator Pro, BlogVault, WP-Migrate) | vendor pricing pages |
| **Hosting providers** | **Free migrations as an acquisition cost** — Kinsta, SiteGround, DreamHost all staff or license migration tooling to win switchers | hosting comparison surveys |

The most interesting buyer is the **destination host**. A migration *to* them is a customer
acquisition event worth far more than the migration costs. Hosts already give migrations
away for free and pay staff/tools to do it. A universal, panel-agnostic importer is worth
real money to every host that is *not* Plesk/cPanel-locked.

**Fleet size drives willingness to pay — but migration itself is monetized indirectly**
(July 2026 research). WPMU DEV prices multi-site WordPress management on a per-site ladder —
$5/mo (1 site) → $20/mo (10 sites) → $200/mo (unlimited, list $2,000/yr), explicitly aimed at
"freelancers, developers, in-house teams and agencies" managing fleets. This confirms the
agency/fleet segment is where premium prices hold, and validates a per-site-tier shape for our
own Pro tier (§1.5 stream #3). The important caveat: WPMU DEV, WP Engine, SiteGround and Kinsta
all treat *migration itself* as free/assisted onboarding that funnels into hosting — nobody
sells per-migration pricing standalone. So our fleet tier should be priced on ongoing
fleet-management value (multi-site orchestration, dashboards, reporting) and/or host
partnerships, **not** a per-migration fee — which is exactly the §1.5 sequencing.

### 1.4 Recommendation: open-source local CLI now, open-core combo later

**Phase 1 (now → v1.0): 100% open source, 100% local. License: Apache-2.0.**

- Apache-2.0 over MIT for the patent grant; over (A)GPL/BSL because our mission is
  *lowering lock-in* — a host embedding the tool in their onboarding is distribution, not
  theft. Monetize the hosts through partnership, not license enforcement.
- Revenue expectation for this phase: ~zero. Optimize for adoption, GitHub stars,
  and becoming the default answer to "how do I move a site between shared hosts."
  (Optional: GitHub Sponsors / Open Collective from day one — rclone-style.)

**Phase 2 (post-1.0, if traction): open-core combo. The data plane stays local and free
forever; paid offerings sit around it:**

1. **Host partnerships (B2B, highest potential).** "Verified destination profiles"
   (§3.3 of PLAN.md: Plesk, one.com, Combell, cPanel presets), co-marketed
   "switch to us" landing pages, or white-label embedding in host onboarding.
   Priced per-seat or per-migration-completed. The tool becomes a switcher pipeline
   hosts pay to be on the receiving end of.
2. **Agency/fleet tier (B2B, most predictable).** Paid features that only matter at
   volume: multi-site batch migrations, migration dashboards/history across clients,
   white-label PDF cutover reports, scheduled re-syncs, priority support. ~$99–$299/yr —
   the price band agencies already pay Duplicator/BlogVault.
3. **Optional control-plane SaaS (Tailscale model).** A web UI for orchestration,
   scheduling, and team visibility — but transfers remain peer-to-peer over SSH;
   the cloud never touches site data. This keeps the "your data never leaves your
   hosts" promise intact while enabling subscription revenue.
4. **Concierge migrations (services, opportunistic).** The $149–$499/site market
   exists; "we run the tool for you" is a zero-R&D offering. Good early revenue and a
   feedback goldmine, but it doesn't scale — treat as a side channel, not the business.

**Explicitly rejected:** data-plane SaaS (BlogVault clone), closed-source paid CLI
(WP-Migrate clone), license-gated core (BSL) — each one destroys the trust/anti-lock-in
positioning that is our only durable differentiator against better-funded incumbents.

### 1.4a AI/LLM: skip in the CLI, defer a paid AI layer (July 2026 research)

We researched whether AI would genuinely help — framework detection, diagnosing failed
migrations, parsing weird server configs — or whether it's marketing. The evidence says **skip
it in the local CLI now** and **defer** (don't kill) a possible advisory layer later:

- **The CLI's core tasks are a solved procedural problem, and no competitor uses AI.** Every
  production migration/detection tool examined — Plesk's Wappspector CLI and Site Import, WP
  Engine's Site Migration plugin, the cPanel ecosystem — is deterministic file/signature-based
  with zero ML. The one peer-reviewed ML detector *underperformed* rules (missed ~39% of
  WordPress, ~21% of Drupal sites). Technical detail in [PLAN.md §2.2](PLAN.md) and §7.
- **Developer sentiment is actively hostile to AI in our category.** Stack Overflow 2025: 46%
  distrust AI accuracy (vs 33% trust), top frustration is "almost right, but not quite" (66%),
  and ~76% don't want AI touching deployment/operations. For a tool that can corrupt a
  production site, "almost right" is the worst possible failure — deterministic safety *is* our
  pitch, and bolting on AI would undercut it.
- **What's deferred, not rejected:** a strictly *advisory*, opt-in LLM helper (e.g. "explain
  why `check` failed", "what does this odd config mean") — never in the execution path, BYOK,
  off by default per the trust guard. The survey shows AI-for-explanation is the one role
  developers tolerate (54% acceptance). But its value here is unproven, so it's a **defer**: no
  design work until real field data (PLAN.md §7's opt-in failure taxonomy) shows edge cases an
  LLM would actually close. **A paid AI SaaS tier is off the table until then** — there is no
  precedent for AI in migration tooling and no demonstrated willingness to pay for it.
- **If an AI layer ever ships, precedent points to BYOK, not a metered data plane.** Warp's
  $20/mo tier lets users bring their own OpenAI/Anthropic/Google keys — a packaging model that
  keeps us out of the LLM-cost and data-processing business, consistent with §1.2. (Note: a
  commonly-cited "Raycast sells AI as a metered add-on" claim did *not* survive verification, so
  the "AI as the paid tier" precedent is thinner than it looks — another reason to defer.)

### 1.5 Monetization playbook — streams, mechanics, sequencing

The structural insight: **every migration is a hosting purchase decision.** The user runs
this tool at the exact moment they choose a new host — the highest-value moment in the
entire hosting industry (hosts pay $50–$150+ affiliate commissions per signup for this
moment). We sit on that moment natively. Monetization streams, in the order to activate
them:

| # | Stream | Mechanics | Price point | When |
|---|---|---|---|---|
| 1 | **Hosting referrals / affiliate** | "Don't have a destination yet?" — a curated, compatibility-tested host list (from our own test matrix, §4) on the docs site + optional `plan` output link. Clearly disclosed. Never in the CLI's critical path | $50–$150 per signup (industry standard) | From v0.1 — zero product work |
| 2 | **Concierge migrations** | "We run it for you" service page; also converts every tool *failure* into a lead ("hit a blocker? we'll finish it") | $149–$399/site (market band) | From v0.1 — zero product work |
| 3 | **Pro / agency tier** | Paid license unlocks volume features: batch/fleet migrations, cross-client history dashboard, white-label PDF cutover reports, scheduled re-syncs, priority support. Core single-site migration stays free forever | $199–$299/yr per seat (Duplicator Pro $199, BlogVault $149–$499 anchor) | Post-1.0, gated on 25 prepays |
| 4 | **Host partnerships** | (a) "Verified destination" program: host pays for a maintained, tested destination profile + placement in the curated list; (b) white-label/embedded use in host onboarding ("switch to us" wizard powered by the engine); (c) per-completed-inbound-migration fee | (a) $500–$2k/mo per host; (b/c) negotiated; a switcher is worth 1–3 yrs × LTV to them | Post-1.0, needs traction as leverage |
| 5 | **Control-plane SaaS** | Team dashboard: orchestration, scheduling, audit log, migration status across clients — transfers stay host-to-host (Tailscale model) | $29–$99/mo per team | Only if #3 proves agency demand |
| 6 | **Sponsorship** | GitHub Sponsors / Open Collective; hosts sponsor for goodwill + logo | Beer money | From day one |

Rough revenue shapes (for calibration, not projection):
- Affiliate: 1,000 migrations/mo with 5% choosing a listed host × $75 avg ≈ **$45k/yr** — passive.
- Agency tier: 300 seats × $249 ≈ **$75k/yr** — the first "real business" milestone.
- One mid-size host partnership ≈ **$12–24k/yr** each; five hosts ≈ a salary.
- Concierge: capped by hours; treat as validation revenue and a feedback channel.

Trust rules that keep monetization from killing the project:
- The open-source CLI never nags, never phones home by default, never gates single-site
  migration features that used to be free.
- Affiliate recommendations live on the website, are ranked by *our own compatibility
  matrix* (objective, community-verifiable), and are labeled as paid placements.
- Paid features are things individuals never miss (fleet, white-label, dashboards) —
  the Duplicator/BlogVault feature split proves agencies accept this line.

### 1.6 Realistic scenarios — bear / base / bull

The willingness-to-pay is already proven by competitors (agencies pay Duplicator/BlogVault
$149–$499/yr for worse-scoped tools; hosts pay $50–$150 per acquired switcher). The open
variable is **adoption volume** — that gates every stream except consultancy, which pays
from migration #1. Consultancy is therefore the bridge in every scenario, not the
destination.

| | **Bear** | **Base** | **Bull** |
|---|---|---|---|
| Adoption at ~18 mo | Stalls: <1k stars, tens of migrations/mo, matrix covers a handful of hosts | Steady: several k stars, high hundreds of migrations/mo, community-maintained matrix | Default tool: "just use it" reflex on r/webhosting, thousands of migrations/mo |
| Revenue mix | ~100% consultancy + trickle of affiliate | Consultancy declining share; affiliate ~$30–50k/yr; agency tier passes the 25-prepay gate → first ~$25–75k/yr ARR | Agency tier $100k+/yr; 3–5 host partnerships $50–100k/yr; affiliate compounding; control-plane SaaS viable |
| What it is | A consultancy with a great in-house tool + a strong portfolio piece | A sustainable solo/duo product business | A small company — or an acquisition target (hosts & panel vendors: WebPros, host groups) for whom a panel-agnostic importer is strategic |
| Correct response | Keep it OSS, keep consulting, don't build paid features nobody prepaid for | Build Pro tier, start host-partnership conversations with volume data as leverage | Choose: scale it vs. sell it |

Two readings of this table:
- **The downside is a floor, not a failure.** The bear case still yields paid consulting,
  a reputation asset, and a maintained tool — because the cost side (solo OSS, no cloud
  data plane, no inventory) is near zero. Nothing about the model can lose material money.
- **The deciding variable is not the business model.** All three scenarios share the same
  model; they differ only in adoption. Adoption is decided by the v0.1 demo moment,
  the compatibility matrix, and per-host SEO content (§4) — so effort spent there moves
  the scenario, effort spent pre-building monetization features does not. Hence the
  hard gate in §6: no paid features before 25 agency prepays or a signed host.

---

## 2. Market snapshot

- WordPress alone powers ~43% of the web; the tools in PLAN.md §1 are almost all WP-only,
  which is the clearest signal that the framework-agnostic gap is real.
- Migration pricing is well-established ($25 freelance → $500 pro service → free-from-host),
  so the *value* of one migration is documented even though our CLI gives it away — that
  number ($150–$500) is the anchor for any future paid tier or host-partnership pricing.
- Hosting churn is driven by price hikes at renewal, performance, and support — churn events
  are constant, and every churn event is a migration. The blocker isn't demand; it's that
  self-serve migration between arbitrary shared hosts is scary. That fear is the product.
- Open-source precedent for the model: rclone (pure OSS + sponsorship, massive adoption),
  k6 (OSS → acquired by Grafana), ngrok (freemium control plane). The "open core with a
  local data plane" pattern is proven in dev tools.

## 3. Positioning

**One-liner:**
> Move any website between shared hosts over SSH. One command to plan, one to migrate.
> Open source, runs on your machine — your data never touches a third-party cloud.

**Category:** "Terraform for website migrations" — the `plan` → `migrate` workflow is the
signature feature no competitor has, and it borrows ergonomics every dev already trusts.

**Against each competitor class:**

| Against | Our message |
|---|---|
| WP plugins (Duplicator, All-in-One) | No plugin install, no wp-admin, no PHP upload limits, works beyond WordPress |
| SaaS migrators (BlogVault, Migrate Guru) | Your credentials and data stay between your two hosts and your laptop |
| Panel tools (Plesk Site Import, cPanel Transfer) | No panel required on either end — "Plesk Site Import without Plesk" |
| DIY rsync + mysqldump | Auto-detection, dry-run plan, serialized-safe search-replace, idempotent reruns — the safety rails your script doesn't have |

**Primary audiences (in order):**
1. Freelancers & small agencies who migrate client sites monthly (highest pain frequency,
   natural word-of-mouth, future paying tier).
2. Technical site owners fleeing a bad/expensive host (the Show-HN / Reddit r/webhosting crowd).
3. Hosting providers wanting an inbound-switcher tool (future revenue, not launch audience).

## 4. Go-to-market (launch sequence)

1. **v0.1 "show moment"** (PLAN.md Phase 3 exit): asciinema demo of a real WordPress site
   migrated between two real shared hosts with two commands → Show HN, r/webhosting,
   r/WordPress, Lobsters.
2. **Content wedge:** "How to migrate a WordPress site between any two hosts over SSH"
   — the searches people actually make; the CLI is the answer. Per-host guides
   (one.com → Combell, GoDaddy → SiteGround…) are infinite long-tail SEO.
3. **Distribution as marketing:** Homebrew, Scoop, deb/rpm, one-line install script
   (already in PLAN.md Phase 4) — every package manager listing is a discovery channel.
4. **Community proof:** a public "tested hosts" compatibility matrix that contributors
   extend — turns shared-hosting weirdness (our biggest risk) into a community flywheel.

## 5. Naming

`migrate-cli` can't survive to public release: it collides with **golang-migrate's
`migrate`** (the dominant DB-schema CLI) and is unsearchable. Candidates checked
July 2026 (web/GitHub collision scan — trademark & domain checks still needed before
committing):

| Name | Verdict | Notes |
|---|---|---|
| **rehost** ✅ recommended | Clean in this space | Says exactly what it does; "rehosting" is the established lift-and-shift term. `rehost plan` / `rehost migrate` reads perfectly. Generic word = weak trademark & harder SEO — mitigate with `rehost.dev` / `rehost.sh` and "rehost cli" as the search phrase |
| **decamp** ✅ runner-up | Clean (only a small creative agency + personal site) | "Break camp and move on" — captures the anti-lock-in ethos; brandable, ownable, great verb. Slightly less self-explanatory |
| sitehaul | Clean | Descriptive fallback; webhaul.com is parked, no dev-tool collisions |
| siteshift | ❌ taken | Crowded — including **an existing website-migration platform** (OpsHelp SiteShift) plus several agencies |
| sitehop | ❌ taken | Sitehop = existing cybersecurity hardware company (sitehop.com) |
| migrate-cli | ❌ replace | golang-migrate collision (already flagged in PLAN.md §5) |

**Recommendation: `rehost`.** For a CLI, discoverability and instant comprehension beat
brandability — a hosting forum comment saying "just use rehost" needs no explanation.
Register `rehost.dev` (docs) and `rehost.sh` (install script: `curl rehost.sh | sh`).
If domains/trademark fail the check, fall back to `decamp`.

## 6. Success metrics

| Phase | Metric |
|---|---|
| Launch (v0.1) | GitHub stars, HN/Reddit reception, installs via brew/script |
| Adoption (v1.0) | Completed migrations reported (opt-in telemetry or GitHub discussions), hosts in the compatibility matrix |
| Monetization test (post-1.0) | 1 host partnership signed OR 25 agency-tier prepay commitments before building paid features |

---

### Sources

- Duplicator — [Cost to Migrate a WordPress Site (2026)](https://duplicator.com/cost-to-migrate-wordpress-site/)
- Seahawk Media — [WordPress Migration Costs](https://seahawkmedia.com/wordpress/wordpress-migration-costs/)
- Superb Website Builders — [How Much Does It Cost to Migrate a Website?](https://superbwebsitebuilders.com/how-much-does-it-cost-to-migrate-a-website/)
- Palark — [How companies make millions on Open Source](https://palark.com/blog/open-source-business-models/)
- Wikipedia — [Open-core model](https://en.wikipedia.org/wiki/Open-core_model), [Rclone](https://en.wikipedia.org/wiki/Rclone), [k6](https://en.wikipedia.org/wiki/K6_(software))
- Name collision checks: [Sitehop (security co)](https://sitehop.com/solutions/), [SiteShift (OpsHelp migration platform)](https://www.opshelp.com/blog/introducing-siteshift-streamlined-website-migrations/), [golang-migrate](https://github.com/golang-migrate/migrate)

AI / fleet-tier research (July 2026):
- Plesk — [Wappspector (procedural CMS/framework detection CLI)](https://github.com/plesk/wappspector), [Site Import](https://docs.plesk.com/en-US/onyx/migration-guide/importing-websites.78361/)
- WP Engine — [Site Migration plugin docs (AI-free migration path)](https://wpengine.com/support/wp-engine-site-migration/)
- Stack Overflow — [2025 Developer Survey: AI trust & task adoption](https://survey.stackoverflow.co/2025/ai)
- WPMU DEV — [Pricing (per-site fleet ladder to $200/mo unlimited)](https://wpmudev.com/pricing/)
- Warp — [New pricing / BYOK (AI-tier packaging precedent)](https://www.warp.dev/blog/warp-new-pricing-flexibility-byok); Raycast — [Pricing (free-forever core + paid Pro)](https://www.raycast.com/pricing)
- HashiCorp BSL relicense backlash (license-gated-core anti-precedent) — [ITPro analysis](https://www.itpro.com/software/open-source/analysis-hashicorp-prioritizes-its-business-with-bsl-license-switch-but-community-upset-cannot-be-ignored)