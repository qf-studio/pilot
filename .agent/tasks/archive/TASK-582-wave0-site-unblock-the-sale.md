# TASK-582: Wave 0 — pilotcloud.dev copy update (unblock the sale)

**Status**: ✅ S3–S7 ALL MERGED 10-22 — S3/S4/S5 (PR#17/#18/#19, reviewed APPROVE) · S6 (PR#20 merged 13:35Z, reviewed 16:00Z APPROVE-w-notes) · S7 (PR#21 test red → autopilot fix PR#23 merged 13:59Z, unreviewed; `lib/site.ts` price constants still duplicated — fold in a later copy pass). Held: S1 (founder legal facts), S2 (Nelya PR). Was: 🚀 DISPATCHING 2026-10-22 — legs S3–S7 filed on `qf-studio/pilotcloud-site` (URLs in § Refs). S1 held (founder legal facts), S2 held (Nelya's workflow PR). Full roadmap: `.agent/system/project/project_pilot_cloud_roadmap_tickets.md`; decisions: `.agent/system/project/project_pilot_cloud_positioning.md` §1, §3, §4, §8.6.
**Created**: 2026-10-22
**Assignee**: Pilot (S3–S7) · founder (F1 legal facts, F2 Paddle resubmit, F4 review Nelya's PR) · Nelya (I4 deploy workflow)

---

## Context

The marketing site (`pilotcloud-site`, Next.js 15 static export, Bun, 9 pages) is built but not deployed, and its copy predates the 2026-10-21 positioning decisions. Wave 0 lands the copy that matches the decided identity, proof, pricing and product name, so the first deploy (after Nelya's workflow PR) ships the right site. Legal pages (S1) and the deploy (S2) stay blocked on founder / Nelya inputs and are not Pilot legs yet.

Order: S3 (hero) first — S4–S7 are independent of each other and of S3, but S7 (rename) touches every page, so it runs **last** (`Blocked by` the other four) to avoid merge conflicts.

## Known pitfalls (from memory)

- mem-195: a production UI once shipped the mock adapter. Every leg's acceptance includes `make build` + `make test` output pasted into the PR body.
- TASK-497 rev 14: the on-site price must equal the live Paddle catalog — the pricing leg pins the string and the price id.
- SOP Rule 3: dependencies are `Blocked by: #N` numeric refs only.

## Legs

### LEG S3 — repo: qf-studio/pilotcloud-site — title: feat(landing): hero and subtitle from the Pilot Cloud identity sentence; drop the orchestration-layer line

## Context
Pilot Cloud's identity sentence was decided 2026-10-21. The landing hero must say it. The older "orchestration layer that makes any coding agent better" framing belongs to the OSS/developer story and must not appear on the Cloud landing.

## Scope
- Replace the hero headline on the landing page (`app/page.tsx`, content under `lib/content`) with exactly:
  **Pilot Cloud is a hosted autonomous engineer that turns labeled tickets into reviewed pull requests on a dedicated box, for small teams with more tickets than engineers.**
- Subtitle (one line, exactly): **Label a ticket. Get a reviewed PR. Your code runs on a box only you use.**
- Remove every occurrence of the phrases "orchestration layer" and "makes any coding agent better" from the landing page and its content files. The self-hosted page is out of scope (separate issue).
- Do not change layout, styles, or other pages.

## Acceptance
- [ ] The landing page renders the headline and the subtitle above, verbatim.
- [ ] `grep -ri "orchestration layer" app lib` returns no matches on the landing page or its content files — paste the output into the PR body.
- [ ] `tests/landing.test.tsx` asserts the new headline text; the old headline assertion is removed.
- [ ] `make build` and `make test` pass — paste the output into the PR body.

## Out of scope
Pricing, proof counter, self-hosted page, product-name rename (separate issues).

### LEG S4 — repo: qf-studio/pilotcloud-site — title: feat(proof): build-time "Pilot built Pilot" merged-PR counter section on the landing page

## Context
The lead proof for Pilot Cloud is a number anyone can check on GitHub: how many merged PRs on `qf-studio/pilot` came from `pilot/*` branches. On 2026-10-20 it was 1,653 of 2,212. The site is a static export, so the number is fetched at build time and rendered with its date.

## Scope
- Add a build script (new file, plain name: scripts/fetch-proof.mjs) that queries the GitHub API for merged PRs on `qf-studio/pilot`, counts those whose head branch starts with `pilot/`, and the total, and writes a JSON file (new file: lib/content/proof.json) with `{ pilotMerged, totalMerged, asOf }` where `asOf` is an ISO date.
- The script uses `GITHUB_TOKEN` from the environment when present and works unauthenticated otherwise (paginate; stop on rate limit and keep the previous JSON if one exists). It never fails the build: on any error it logs a warning and leaves the existing JSON untouched.
- Commit an initial proof.json with `pilotMerged: 1653, totalMerged: 2212, asOf: "2026-10-20"`.
- Wire the script into `make build` before the Next.js build.
- Add a section on the landing page (`app/page.tsx`) directly under the hero: headline **Pilot built Pilot.**, line **1,653 of 2,212 merged PRs on qf-studio/pilot came from Pilot — as of 2026-10-20.** rendered from proof.json with thousands separators and the date, and a link to `https://github.com/qf-studio/pilot/pulls?q=is%3Apr+is%3Amerged+head%3Apilot%2F` with the text **Check it on GitHub**.
- Styling matches the existing stat-card row on the page (same bordered card component).

## Acceptance
- [ ] proof.json exists and the section renders its values with the date.
- [ ] A unit test renders the section from a fixture JSON and asserts the formatted line and the link href.
- [ ] Run the fetch script once locally and paste the output into the PR body (counts and date it wrote, or the warning path if rate-limited).
- [ ] `make build` and `make test` pass — paste the output into the PR body.

## Out of scope
Any weekly rebuild schedule (CI cron) — handled with the deploy workflow.

### LEG S5 — repo: qf-studio/pilotcloud-site — title: feat(pricing): pricing block — $499 a month, 100 shipped tickets, no seats, your own box; packs; fair-use wording

## Context
Pricing v3 was decided 2026-10-21. The site must show one plan with the ticket as the unit. Tokens and dollars-per-ticket never appear; "fair use" is the only token-related word. The price string must equal the live Paddle catalog.

## Scope
- Replace the pricing section on the landing page (`app/page.tsx`, content under `lib/content`) with:
  - Plan name **Pilot Cloud** — **USD 499 / month**.
  - Four bullets, verbatim: **100 shipped tickets a month** · **No seats — invite your whole team** · **Your own box — a dedicated instance only you use** · **Fair use of included AI; your own key takes over beyond it, at no extra fee**.
  - Packs line, verbatim: **Need more? 25 tickets for $99 or 100 tickets for $349, prepaid, valid 90 days.**
  - Footnote, verbatim: **A ticket counts when its pull request opens. No free tier: the first repo bootstrap is free and the card is entered before the first ticket.**
  - Primary button text **Start with one repo** linking to `https://console.pilotcloud.dev/register`.
- Keep the price string `USD 499 / month` in one constant with the code comment `// Paddle price id pri_01m4g870rh98m207cjwhjn8taq — keep equal to the live catalog`.
- Remove any remaining "included Sonnet token volume", "$299", "Team", "Design Partner" or token-count wording from the page.

## Acceptance
- [ ] The pricing section renders the plan, four bullets, packs line, footnote and button exactly as above.
- [ ] `grep -rniE "299|design partner|token volume|tokens included" app lib` returns no matches — paste the output into the PR body.
- [ ] A test asserts the price string `USD 499 / month` and the four bullets.
- [ ] `make build` and `make test` pass — paste the output into the PR body.

## Out of scope
Paddle checkout wiring, the refunds page text.

### LEG S6 — repo: qf-studio/pilotcloud-site — title: feat(self-hosted): "Self-hosted or Pilot Cloud" block — your code stays in your environment vs a dedicated box only you use; who reviews the PR

## Context
Rescoped 2026-10-22 after five no-op runs: the self-hosted page has no OSS-vs-Cloud comparison and no "secrets never leave" sentence, so the first version of this issue had nothing to change. The page (`app/self-hosted/page.tsx`) is: install cards → "Where it runs" (`targets`) → "What stays yours" (`safeguards`, a two-column `CardGrid`) → "License" → `PageCta`. Content lives in `lib/content/self-hosted.ts` as `Card` objects (`title`, `text`, `doc`) rendered by `CardGrid` from `components/page/blocks.tsx`. The hero lead already says "Your code stays in your environment." and stays as it is.

Pilot Cloud runs each tenant on a dedicated instance, so the hosted wording must not contradict the OSS claim: OSS = your environment; Cloud = a box only you use.

## Scope
- Add one section to `app/self-hosted/page.tsx` directly after the "What stays yours" section and before "License", using the same `nextSection` spacing and `SectionHead`:
  - `SectionHead` title **Self-hosted or Pilot Cloud**, lead **The same Pilot. Run it yourself, or let us run it for you on a box only you use.**
  - Below it a two-column `CardGrid` (`cols={2}`) rendered from a new export `comparison` in `lib/content/self-hosted.ts` with exactly two cards:
    1. title **Self-hosted** — text, verbatim: **Your code stays in your environment. Who reviews the PR: you.**
    2. title **Pilot Cloud** — text, verbatim: **Your code and keys stay on a dedicated box only you use. We never share compute between customers. Who reviews the PR: you, from the Review needed lane.**
  - Neither card links to a doc. If the `Card` type requires `doc`, make it optional and have the card renderer skip the link when it is absent; `safeguards` and `targets` keep their links.
- No other copy, layout or page changes. The compare page is out of scope.

## Acceptance
- [ ] The self-hosted page renders the section title, the lead and both card texts verbatim, in that order after "What stays yours".
- [ ] `tests/pages.test.tsx` (or a new test next to it) renders `SelfHostedPage` and asserts both card texts verbatim.
- [ ] `make build` and `make test` pass — paste the output into the PR body.

## Out of scope
Pricing, product-name rename (#16), the compare page, the docs site.

### LEG S7 — repo: qf-studio/pilotcloud-site — title: chore(copy): product name "Pilot Cloud" site-wide; "Console" only as the app URL

## Context
The product is named Pilot Cloud. "Pilot Console" was an earlier working name and still appears in copy. The app's hostname (`console.pilotcloud.dev`) does not change.

## Scope
- Replace "Pilot Console" with "Pilot Cloud" in all page copy, titles, metadata and the header/footer (`components/site-header.tsx`, `components/site-footer.tsx`, `app/layout.tsx`, every page under app/, content under `lib/content`).
- Keep the literal hostname `console.pilotcloud.dev` and any URL paths unchanged.
- Legal pages (`app/terms/page.tsx`, `app/privacy/page.tsx`, `app/refunds/page.tsx`): rename only; do not add or change legal text.

## Acceptance
- [ ] `grep -rn "Pilot Console" app components lib` returns no matches — paste the output into the PR body.
- [ ] `grep -rn "console.pilotcloud.dev" app components lib` still returns the same number of matches as before the change — paste both counts into the PR body.
- [ ] `make build` and `make test` pass — paste the output into the PR body.

Blocked by: #{{S3}}
Blocked by: #{{S4}}
Blocked by: #{{S5}}
Blocked by: #{{S6}}

## Held legs (not filed)

- **S1 feat(legal)**: terms, privacy, refunds with the legal entity, address and contact email — waits on founder facts (F1). Body = TASK-515 § S2.
- **S2 ci(deploy)**: Nelya's `deploy-quantflow-aws.yml` PR → review (F4) → first deploy → Paddle resubmit (F2). Body = TASK-515 § S3.

## Refs

- S3 → https://github.com/qf-studio/pilotcloud-site/issues/12
- S4 → https://github.com/qf-studio/pilotcloud-site/issues/13
- S5 → https://github.com/qf-studio/pilotcloud-site/issues/14
- S6 → https://github.com/qf-studio/pilotcloud-site/issues/15
- S7 → https://github.com/qf-studio/pilotcloud-site/issues/16
