# TASK-515: pilotcloud.dev landing page + legal pages (Terminal + Console, console-styled)

**Status**: 📋 Planned 2026-10-09 — awaiting founder confirmation of legal entity facts, then dispatch S0
**Created**: 2026-10-09
**Assignee**: Navigator (plan, infra request, Paddle resubmit) · Pilot (S0–S3 on the new repo, U1 on console-ui) · Nelya (I1) · founder (legal facts, Paddle verification)

---

## Context

**Problem**: Paddle website approval for `console.pilotcloud.dev` was submitted 2026-10-09 (status In review). Paddle requires the site to link to or contain terms of service, a privacy notice and a refund policy, show the product and its price, and make contact reachable. Nothing public exists today: the console root is an unauthenticated SPA shell with no legal route, the UI repo has no legal pages, and no policy drafts exist in any repo. The apex `pilotcloud.dev` has no DNS record (zone is Route 53, name servers `awsdns`). Paddle step 02 (account verification) and the whole L7 go-live (TASK-497) are gated on this.

**Goal**: A public marketing site at `https://pilotcloud.dev` that covers BOTH ways to run Pilot — the terminal/OSS version (free, BSL 1.1, bring your own Claude key) and Pilot Console (hosted, USD 499/month) — plus `/terms`, `/privacy`, `/refunds`. Visual layout mirrors the console's Usage page (founder screenshot 2026-10-09): dark canvas, section title + one-line subtitle, a row of three bordered stat-style cards, then one large bordered panel. Console login footer links to the three policy pages so `console.pilotcloud.dev` "links through" as Paddle requires.

**Founder decisions (2026-10-09)**: hosting = CloudFront + S3 on Nelya's pilot-fleet (not Vercel); scope = landing + legal, covers OSS Pilot too; layout = same as the Usage page screenshot.

---

## Known Pitfalls & Patterns

- **PITFALL** (98%, mem-195): a production UI build once shipped the mock adapter and two days of "passed" milestones were fixtures. → S3 deploy workflow guards the static export (no dev-origin strings, `index.html` present, policy pages present) before any S3 sync; every milestone needs one wire proof (curl through CloudFront).
- **PITFALL** (95%, mem-199): AWS WAF `LFI_QUERYSTRING` blocks query strings ending in `README.md` on both fleet ACLs. → No page on the site links to a README.md URL with a query string; the infra request names the WAF ACL to attach so Nelya scopes it.
- **PITFALL** (SOP onboarding Rule 1/3b/6): first issue on a new repo is scaffold-only; backticked paths that do not exist on main hold the task indefinitely; per-project quality gates must exist before the first run. → S0 is scaffold-only with a `.pilot/workflow.yaml`; dependent legs carry `Blocked by: #<S0>`; leg bodies name to-be-created files in plain text, never backticks.
- **PATTERN** (TASK-497 rev 14, ui#162): on-site price must equal the live catalog. → pricing section hardcodes USD 499 / month with the price id in a code comment, and the acceptance bullet pins the string.
- **PATTERN** (console-ui `deploy-quantflow-aws.yml`): build on `ubuntu-latest` → artifact → self-hosted `aws-pilot-fleet-deployer` → `aws s3 sync` (hashed assets immutable, rest short cache) → CloudFront invalidation → edge probe. → S3 is a port of that workflow.

---

## Acceptance Criteria

- [ ] `https://pilotcloud.dev/` and `https://www.pilotcloud.dev/` serve the landing over HTTPS through CloudFront (www redirects to apex, 301).
- [ ] `/terms`, `/privacy`, `/refunds` return 200 with real policy text naming the legal entity, address and contact email. No placeholders, no redirects to generic pages.
- [ ] Landing shows: product description, Terminal (free/BSL) vs Console (USD 499 / month, one plan, no trial, included Sonnet 5.5 token volume, overage on the customer's own key) comparison, how it works, FAQ, contact. Contact is reachable from the homepage in ≤ 2 clicks.
- [ ] The pricing string on the site equals the live Paddle catalog (`pri_01m4g870rh98m207cjwhjn8taq`, USD 499/month).
- [ ] Console login page footer links to the three policy URLs on the apex (ui leg U1).
- [ ] Lighthouse (mobile) performance ≥ 90, accessibility ≥ 95 on `/`; no layout shift from font loading.
- [ ] Paddle website approval resubmitted and status recorded in `.agent/system/references/reference_paddle_account.md`.

---

## Implementation

### Layout contract (from the Usage screenshot — applies to every section)

Canvas `#0b0c0e`, surface `#17191d`, hairline `#2b2e34`, ink `#eceef1`, text-2 `#b4b9c1`, muted `#868c96`, accent `#6fa1ff`, font Inter (tokens copied verbatim from pilot-console-ui `src/design-system/theme.css`, dark block; light block kept for `prefers-color-scheme: light`). Content max-width 1140px. Each section:

1. Header row: H2 (28–32px, semibold) + one-line subtitle in text-2 (left); optional control/CTA pill with hairline border (right).
2. Card row: three equal bordered cards (surface, hairline border, 12px radius), label in text-2 (14px) over a large value/claim in ink (32–40px, semibold).
3. Panel: one full-width bordered panel (same surface/border) with a panel title (20px) and its body (chart, list, table, or prose).

Sections, in order: **Hero** (title "AI that ships your tickets", subtitle "Navigator plans. Pilot executes.", CTAs: Install terminal · Start with Console) → **Two ways to run Pilot** (cards: Terminal · Console · Both share the same engine; panel: side-by-side feature table) → **How it works** (cards: Navigator plans · Pilot executes · Autopilot merges; panel: pipeline diagram or the README demo GIF) → **Pricing** (cards: Terminal — Free, BSL 1.1, your Claude key · Console — USD 499 / month · What is included; panel: plan details + "Pay with Paddle" note) → **FAQ** (panel) → **Contact** (cards: Email · GitHub · Discord; panel: short form-less contact text) → **Footer** (Terms · Privacy · Refunds · Docs · GitHub · © legal entity).

### S0 — scaffold (Pilot, new repo, no deps) — `feat(site): Next.js static-export scaffold with console theme tokens`
**Goal**: an empty but deployable site: Next.js 15 app router with `output: 'export'`, Tailwind v4 via `@tailwindcss/postcss`, Inter via `next/font`, theme.css tokens copied from console-ui, root layout with header + footer (footer already links /terms /privacy /refunds), placeholder `page.tsx` for `/`, `/terms`, `/privacy`, `/refunds`, `robots.ts`, `sitemap.ts`, `metadataBase https://pilotcloud.dev`. Bun as package manager (lockfile committed). CI: `bun run lint`, `bun run typecheck`, `bun run build` (static export to `out/`). `.pilot/workflow.yaml` with the three gates. README with run/build commands.
**Files (to be created)**: package.json, bun.lockb, next.config.ts, tsconfig.json, postcss.config.mjs, app/layout.tsx, app/globals.css, app/page.tsx, app/terms/page.tsx, app/privacy/page.tsx, app/refunds/page.tsx, app/robots.ts, app/sitemap.ts, components/site-header.tsx, components/site-footer.tsx, lib/site.ts (constants: URLs, price, contact), .github/workflows/ci.yml, .pilot/workflow.yaml, README.md.
**Evidence**: `bun run build` output pasted into the PR body; `bun run typecheck` output pasted into the PR body.

### S1 — landing page (Pilot, `Blocked by: #S0`) — `feat(site): landing page — hero, terminal vs console, how it works, pricing, FAQ, contact`
**Goal**: implement the seven sections per the layout contract with a reusable `Section` (header row + card row + panel) and `StatCard` component. Copy voice per `pilot-copywriter` (direct, no hype). Pricing from `lib/site.ts` constants; the USD 499 string appears exactly once in source. Pipeline diagram as inline SVG (no raster). Respect `prefers-reduced-motion`.
**Evidence**: `bun run build` output pasted into the PR body; a Vitest DOM test that renders `/` and asserts the strings "USD 499" and "/ month" and the three policy links exist; mutation pin: change the price constant to 299 -> the pricing test fails.

### S2 — legal pages (Pilot, `Blocked by: #S0`) — `feat(site): terms, privacy and refund policy pages`
**Goal**: three pages rendered from MDX files (content/legal/terms.mdx, privacy.mdx, refunds.mdx) with a shared `LegalLayout` (title, "Last updated", table of contents). Substance:
- **Terms**: product description (hosted Pilot Console + terminal software under BSL 1.1), account/eligibility, acceptable use, customer-provided API keys and token allowance/overage, IP and license, availability, termination, liability cap, governing law (Montenegro), Paddle as merchant of record for purchases.
- **Privacy**: controller = legal entity; data collected (account email, GitHub/Linear identities, repository metadata, usage/cost metrics, support messages); processors (AWS eu-central-1, Paddle, Anthropic, GitHub, Doppler); retention; rights (GDPR-style: access, deletion, portability); cookies (none beyond session); contact.
- **Refunds**: monthly subscription, cancel any time effective at period end, 14-day refund on the first charge on request, no partial refunds for unused allowance after 14 days, how to request (email), Paddle handles the transaction and issues the refund.
Entity facts come from the issue body (founder-confirmed): legal name, registered address, contact email. No placeholder tokens may remain in the rendered HTML.
**Evidence**: `bun run build` output pasted into the PR body; a test that renders each page and asserts no "{{" or "TODO" strings and that the entity name appears; mutation pin: remove the entity name from terms.mdx -> the legal pages test fails.

### S3 — deploy workflow (Pilot, `Blocked by: #S0`, needs I1 outputs) — `ci(site): deploy static export to S3 and invalidate CloudFront`
**Goal**: port console-ui's deploy workflow: `workflow_dispatch` with "deploy" confirmation + push to `main`; build job on `ubuntu-latest` (bun build → upload `out/` artifact); deploy job on `[self-hosted, aws-pilot-fleet-deployer]`: guards (index.html, terms/index.html, privacy/index.html, refunds/index.html exist; no `localhost`/`127.0.0.1` strings in the bundle), `aws s3 sync out/_next "s3://$BUCKET/_next" --delete --cache-control public,max-age=31536000,immutable`, `aws s3 sync out "s3://$BUCKET" --exclude "_next/*" --delete --cache-control public,max-age=300`, CloudFront invalidation `/*`, edge probe (`curl -sI https://pilotcloud.dev/` must contain `cloudfront` and 200; `/refunds` 200). `BUCKET` and `DISTRIBUTION_ID` as workflow env from I1.
**Evidence**: workflow run URL in the PR body; probe step output pasted into the PR body.

### I1 — infra (Nelya, Slack #infrastructure, Navigator writes the request)
1. S3 bucket `pilot-fleet-s3-site` (private, OAC) in the fleet account.
2. ACM certificate in us-east-1 for `pilotcloud.dev` + `www.pilotcloud.dev`, DNS-validated in the Route 53 zone.
3. CloudFront distribution: aliases apex + www, OAC origin to the bucket, default root object `index.html`, a CloudFront Function that (a) 301s `www.` → apex and (b) rewrites `/path` → `/path/index.html` for the static export, Brotli/gzip on, same WAF ACL as the console (scoped per infra#18 so policy pages are never 403).
4. Route 53 A/AAAA alias records for apex and www → the distribution.
5. IAM for the `aws-pilot-fleet-deployer` runner: `s3:ListBucket/PutObject/DeleteObject` on the bucket, `cloudfront:CreateInvalidation` on the distribution.
6. Return: bucket name, distribution id, confirmation that `https://pilotcloud.dev/` serves a placeholder 200 from the bucket.

### U1 — console login footer (Pilot, pilot-console-ui) — `feat(ui): login page footer links to terms, privacy and refund policy`
**Goal**: the unauthenticated login view gets a footer with three external links to `https://pilotcloud.dev/terms`, `/privacy`, `/refunds` (target _blank, rel noopener), styled with existing theme tokens. `Blocked by` nothing in that repo; publish only after S2 is live (the links 404 until then — accept, the deploy of U1 follows S2).
**Evidence**: `bun run test` output pasted into the PR body; a component test asserting the three hrefs; mutation pin: remove the refunds link -> the login footer test fails.

### N1 — Navigator / founder steps (not Pilot issues)
- Create the private repo `qf-studio/pilotcloud-site` (empty, `main`, branch protection like console-ui, Actions enabled, runner group access for `aws-pilot-fleet-deployer`).
- Register the repo on the box: `projects:` entry in the daemon config, execution mode `sequential` for the first cycle, quality gates from `.pilot/workflow.yaml` (SOP Rule 6). Verify the next dispatch logs that it loaded the workflow file.
- Fill S2's entity facts from the founder (legal name, registered address, contact email) before dispatch.
- Post I1 to Nelya; track in the Paddle reference.
- After S1+S2+S3 are live: resubmit Paddle website approval; record status; then TASK-497 L7 step 5 is closed.

---

## Out of Scope

- Moving the docs site (`pilot.quantflow.studio`) under `pilotcloud.dev` — link out only.
- Checkout flow changes, Paddle template sync, `BillingEnabled` flip, SSM writes (TASK-497 L7 post-approval).
- Blog, changelog, analytics/GTM, newsletter, cookie banner (no non-essential cookies, so none is required).
- Light-theme design polish beyond token mapping.
- Vercel hosting (founder decision: AWS).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the source lives | `site/` in the public pilot repo; `docs/`-style sync into a deploy repo; new private repo | New private repo `qf-studio/pilotcloud-site` | The deploy runs on the self-hosted fleet runner; exposing it to a public repo's workflows is unsafe. Mirrors console-ui exactly. |
| Hosting | Vercel; CloudFront + S3 on pilot-fleet; ECS container like quantflow.studio | CloudFront + S3 static (founder) | One vendor, same DNS/WAF/cert ownership as the console; a static export needs no container. |
| Framework | Next.js static export; Astro; plain Vite | Next.js 15 `output: 'export'` + Tailwind v4 | Same stack family as docs/ and console-ui; MDX for legal pages; Pilot has succeeded on this stack. |
| Theme | New marketing palette; copy console tokens | Copy console `theme.css` tokens verbatim | Founder wants the Usage-page look; one source of truth for brand colour. |
| Legal entity in content | Placeholders filled later; founder facts in the issue | Facts in the issue body, test forbids placeholders | A page with `{{LEGAL_NAME}}` fails Paddle review silently. |
| www handling | Separate bucket redirect; CloudFront Function | CloudFront Function 301 → apex | One distribution, one cert, no second bucket. |

---

## Verify

```bash
# in pilotcloud-site
bun run lint && bun run typecheck && bun run build && bun run test
# edge (after I1 + S3)
curl -sI https://pilotcloud.dev/ | grep -i 'HTTP/2 200\|cloudfront'
for p in terms privacy refunds; do curl -s -o /dev/null -w "$p %{http_code}\n" https://pilotcloud.dev/$p; done
curl -sI https://www.pilotcloud.dev/ | grep -i 'location: https://pilotcloud.dev/'
# no placeholders in the live HTML
curl -s https://pilotcloud.dev/terms | grep -c '{{' # expect 0
```

---

## Done

- [ ] S0, S1, S2, S3 merged on `pilotcloud-site`; U1 merged on pilot-console-ui and deployed.
- [ ] I1 delivered: apex + www resolve, 200 through CloudFront, bucket + distribution ids recorded here.
- [ ] Verify block passes end to end.
- [ ] Paddle website approval resubmitted; status recorded in the Paddle reference.
- [ ] Memory written: pattern "private deploy repo per public product site" if it held up.

---

## Refs

- Handoff brief: `.agent/.context-markers/2026-10-09_handoff-landing-page-session.md`
- Paddle facts and approval status: `.agent/system/references/reference_paddle_account.md`
- Billing contract, L7 step 5 audit list: `.agent/tasks/TASK-497-paddle-billing-integration.md`
- Token allowance / overage model: TASK-502
- Onboarding SOP (new repo rules): `.agent/sops/onboarding/new-project-issue-authoring.md`
- Deploy template: pilot-console-ui `.github/workflows/deploy-quantflow-aws.yml` (bucket `pilot-fleet-s3-console-ui`, distribution `E6U3NSSVQ0PTQ`)
- Theme tokens: pilot-console-ui `src/design-system/theme.css`
- Founder screenshot (layout reference): console Usage page, 2026-10-09

---

## Notes

- Dispatch order: N1 (repo + box config) → S0 → wait for merge → S1 ∥ S2 (sequential mode on the box serialises them anyway) → S3 once I1 returns ids → U1 any time after S2 is live.
- Leg bodies must not backtick to-be-created paths (SOP Rule 3b). This doc may; the nav-pilot handoff strips or rewrites them per leg.
- The price id `pri_01m4g870rh98m207cjwhjn8taq` goes in a source comment next to the constant, not in rendered HTML.

---

**Last Updated**: 2026-10-09
