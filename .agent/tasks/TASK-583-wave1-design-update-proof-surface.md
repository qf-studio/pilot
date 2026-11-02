# TASK-583: Wave 1 — design update + proof surface (Run Story, Review needed, briefs, pen test)

**Status**: 🚧 **11-02 audit — wave-1 code complete except the briefs trio; exit evidence open.** MERGED + LIVE (console `prod-0.1.51` = main; UI deploy 10-27 at main `659063ae5`): U1–U5, U9, U8, U6 (ui#277), U7 (ui#282 → PR#285), U19 (ui#280 → PR#284), X2 (ui#283 → PR#286), TASK-590 W1 (ui#279 → PR#281) · C1 (console#440 → PR#442), C2 (#450 → PR#452), C3, C7, C8 (#424: $300 ceiling + packs), C14 (#449 → PR#451), X1 (#453 → PR#454) · P1, P9 (+ fixes #5812–#5814), P10, P11 fix (#5798 → PR#5819), P16, P18 (#5857 → PR#5859). **Still held**: C6 brief store + Briefs API + SES email — gated on **I3 only** (Nelya SES sender; P9 merged 10-22) → U11, U12 after C6. **Wave-1 exit NOT met**: the P11 real run with agent turns is not archived on pilot#5793 (hard gate, line 5) and I2 (pen test on canary, Nelya) waits on it; Run Story on a Done card, share page, Context panel and the daily-cap banner are not exercised on the canary after 10-27 (the console smoke 10-26 12:30Z predates them). Open: pilot#5810 (P17 chat events, design; U9 renders `—`), ui#266 (mode-pills decision, founder), D1–D5/D7 Figma (founder; gate U11/U12 only). **Unfiled from the PR#451 (C14) review**: ordinal read outside the org lock · lossy 0037 down path · standing reader built twice · no RLS on `ticket_pack_draws`. History to 10-26 15:10Z: git log on this line + README Recent 10-22/10-26.

**P11 hard gate (10-22 review):** PR#5795 merged with a report of 8 PASS, but every executor run had failed in ~2 s (`exit status 1`) and the harness only errors on a nil result → false-success. Fix filed as #5798; the gate stays open until a real run with agent turns is archived.
**Created**: 2026-10-22
**Assignee**: Pilot (U/C/P legs) · founder (D1–D5, D7 in Figma; F4) · Nelya (I2, I3)

---

## Context

Wave 1 makes the console look like the 2026-10-21 Figma design and puts the proof unit (the Run Story) and the human's one job (Review needed) on screen. The pen-test harness (P11) runs in parallel on the daemon track because it is a hard gate before the first private-repo tenant.

Design facts extracted from Figma `BfAqtVPBp0i4iWHg2jzAnB` node `7-2` (2026-10-22), for the UI legs:
- Font Inter, regular weight only. Sizes: 11px chips (uppercase), 12px card labels, 13px context-panel rows, 15px list rows, 24px card values.
- Text: white; secondary text = white at 60% opacity; tertiary = white at 40%. Muted numerals `#767576` / `#999899`; bright value `#eee`.
- Accents: green `#8cd68c` (delivered count, active ticket id), blue `#72c5e4` (PILOT chip border + text). Status chip "CI": border `#333`, text white 60%. Status chip "PILOT" (done): border white.
- Cards: 1px border `#333`, radius 4px, padding 12px (stat cards) / 16px (context cards), gap 12px between stat cards.
- Chips: height 21px, padding 8px, radius 4px, 11px uppercase. The "Review" pill: height 24px, radius 100px, border `#e2e2e2`.
- Layout at 1440px: left rail 24–64px (40px avatar, 24px icons); main column x=184, width 707; context column x=986, width 270; right rail at x=1376. Page padding 24px.
- Canvas is near-black; the exact fill is not in the extracted nodes — the UI leg keeps the current theme canvas unless the designer supplies the hex (design leg D1).

## Known pitfalls (from memory)

- mem-195 (prod UI shipped the mock adapter): every UI leg's acceptance includes `make build`, `make test`, `make lint` output and a one-line note on which adapter the view uses.
- SOP Rule 3: dependencies are `Blocked by: #N` numeric refs, same repo only.
- SOP Rule 3b: backticked paths must exist on `main`; new files are named in plain text.
- mem-203: Pilot copies issue claims verbatim — no unverified facts in bodies.

## Legs — pilot-console-ui

### LEG U1 — repo: qf-studio/pilot-console-ui — title: feat(design): tokens and primitives from the 2026-10 Figma — type scale, colors, card, chip, pill, section header, list row

## Context
The console gets a new visual design (Figma file `BfAqtVPBp0i4iWHg2jzAnB`, node `7-2`, 2026-10-21). This leg lands the foundation only: tokens and reusable primitives. Views are restyled in follow-up issues that depend on this one.

Design facts (extracted from Figma; use these values):
- Font: Inter, regular weight only. Type scale: 11px uppercase (chips), 12px (card labels), 13px (context rows), 15px (list rows), 24px (card values). Line-height 1.
- Text colors: primary white; secondary white at 60% opacity; tertiary white at 40%; muted numerals `#767576` and `#999899`; bright value `#eee`.
- Accents: green `#8cd68c`; blue `#72c5e4`. Border `#333`. Pill border `#e2e2e2`.
- Card: 1px border `#333`, radius 4px, padding 12px (compact) or 16px (panel). Stat-card row: 4 equal cards, 12px gap, label top-left 12px, value bottom-right 24px.
- Chip: height 21px, padding 8px, radius 4px, 11px uppercase; variants `neutral` (border `#333`, text white 60%), `pilot` (border + text `#72c5e4`), `done` (border + text white). Pill: height 24px, radius 100px, border `#e2e2e2`, 11px uppercase.
- Section header: 15px title left, 15px secondary action right (e.g. "Show history"), 40px tall.
- List row: 15px, columns id (64px) · title (flexible, truncated) · meta right-aligned (156px) · chip (right); 26px row pitch; child rows indented 21px with a 1px tree line.

## Scope
- Add the tokens to `src/design-system/theme.css` as CSS custom properties (font, sizes, text opacities, accent colors, border, radii, spacing). Keep the existing light/dark theme switch working; the new tokens apply to the dark theme and get sensible light equivalents.
- Add primitives under `src/design-system/components` (new files, plain names): StatCard.vue (label + value slot, optional secondary value span in muted color), Chip.vue (variants neutral/pilot/done), Pill.vue, SectionHeader.vue, ListRow.vue (id/title/meta/chip slots, `depth` prop for tree indent).
- Restyle `src/design-system/components/DashboardStatCard.vue` to use StatCard.vue (or make it a thin wrapper) so existing dashboard tests still pass.
- Add one story-like test page is NOT required; unit tests per primitive are.

## Acceptance
- [ ] Tokens exist in `src/design-system/theme.css` with the values above; a test reads the computed style of a mounted StatCard and asserts border color `#333` and radius 4px.
- [ ] Each new primitive has a unit test under src/design-system/__tests__ (plain name; the directory exists) covering its variants and slots.
- [ ] Existing tests for `DashboardStatCard.vue` pass unchanged.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

## Out of scope
Restyling any view (follow-up issues).

### LEG U2 — repo: qf-studio/pilot-console-ui — title: feat(dashboard): stat cards (Delivered N/100, Review needed, Time to PR, Projects) and header "Last shipped X ago" with a pool meter

## Context
The dashboard header and stat row follow the 2026-10 design. Per-ticket cost never appears on a platform-key tenant; a pool meter (percent of the fair-use allowance) is the only usage number. The "Blocked" card is renamed "Review needed" and opens the review list.

Blocked by: #{{U1}}

## Scope
- In `src/views/DashboardView.vue`, replace the current stat tiles with four StatCard primitives in one row, left to right:
  1. **Delivered** — value `N/100` where N is tickets delivered this billing month (green `#8cd68c` for N, muted `#767576` for `/100`). Source: the existing dashboard payload from `GET /api/v1/dashboard` (use the field that counts delivered/shipped cards this period; if no such field exists, show `—` and name the missing field in the PR body).
  2. **Review needed** — a `Review` pill on the left and the count on the right; the whole card is a link to `/board?lane=needs-you` (the existing Needs You lane route; keep whatever route the board uses today).
  3. **Time to PR** — median minutes, rendered as `~6 m` with the unit in muted color. Source: existing dashboard payload field for median time-to-PR if present; else `—` and note it.
  4. **Projects** — count of connected projects.
- Header line (top of the main column): org name on the left; on the right `Last shipped 12 min ago · Pool 62%` where "Last shipped" is the relative time of the most recent delivered card and "Pool" is `percent_used` from `GET /api/v1/billing/allowance` (if the field is absent, show only "Last shipped" and note it in the PR body). Never render tokens or USD here.
- Keep the existing "honest degrade" behaviour when the instance is not running.

## Acceptance
- [ ] The four cards render in order with the labels above; a unit test asserts labels and the `N/100` split spans.
- [ ] The Review needed card navigates to the Needs You lane; a test asserts the link target.
- [ ] No token or USD string is rendered in the header or cards on a platform-key tenant; a test asserts the absence of `$` and `tokens` in the header text.
- [ ] One line in the PR body names the adapter the view uses in dev and in the test (`src/lib/api/httpAdapter.ts` vs `src/lib/api/mockAdapter.ts`).
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

### LEG U3 — repo: qf-studio/pilot-console-ui — title: feat(dashboard): activity bar chart with 1D / 7D / 30D range switch in the new design

## Context
The dashboard shows an activity bar chart (one bar per bucket; shipped tickets highlighted). The 2026-10 design places it under the stat row in a bordered panel with a range switch on the right.

Blocked by: #{{U1}}

## Scope
- Restyle `src/design-system/components/DashboardShippedChart.vue` (or replace its use in `src/views/DashboardView.vue`) as a panel card: header row "Activity" (12px) left, range switch `1D · 7D · 30D` (12px) right; bars 9.67px wide, 8px gap, radius 2px, height scaled to the max bucket; highlighted bars in the warm accent used today in `src/design-system/components/dashboardShippedColors.ts`, others white at 30% opacity.
- Range switch changes the bucket size (1D = hourly, 7D = daily, 30D = daily) and re-requests `GET /api/v1/board/activity` with the range the endpoint supports; if the endpoint has no range parameter, bucket client-side from the returned events and say so in the PR body.
- Keep existing chart tests passing or update them for the new structure.

## Acceptance
- [ ] A unit test asserts three range buttons and that clicking one changes the bucket count.
- [ ] Bars render with the highlighted/muted distinction; a test asserts both classes appear for a fixture with mixed events.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

### LEG U4 — repo: qf-studio/pilot-console-ui — title: feat(queue): queue grouped by project with epic trees; rows show duration and diff size, never tokens

## Context
The dashboard queue lists work grouped by project (avatar initial + project name + "N shipped" on the right), each group showing epics as parent rows with child rows indented under a tree line. Row meta shows duration and diff size. Tokens and cost never appear.

Blocked by: #{{U1}}

## Scope
- Add a "Queue" section under the activity panel in `src/views/DashboardView.vue` with a SectionHeader ("Queue" left, "Show history" right linking to the board history view that exists today, or `/board` if none).
- Group cards from `GET /api/v1/board/cards` by project; render each group header as: 24px circle with the project's first letter, project name (15px), and `N shipped` (15px, 40% opacity) right-aligned.
- Rows use the ListRow primitive: id (`GH-5521`, 60% opacity; green when the row is the active/running one), title (truncated), meta right-aligned (`12 min · +184 −32` built from execution duration and lines added/removed from the card's execution payload at `GET /api/v1/board/cards/{id}/execution`; omit a part when the field is absent), chip right (`CI` neutral / `PILOT` blue while running / `DONE` white when delivered).
- Epic children render under the parent with `depth=1` and the tree line.
- Group order: projects with running work first, then by most recent activity. Show at most 3 groups and 3 rows per group, with a `+N more` link to the board.
- Never render `tokens`, `K tokens` or `$` in rows; the existing `DashboardShippedByProject.vue` tile is replaced by this section.

## Acceptance
- [ ] Unit tests cover: grouping by project, epic child indentation, meta string building with and without diff fields, chip selection per status, the 3×3 cap with `+N more`.
- [ ] A test asserts that no row text contains `token` or `$`.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

### LEG U5 — repo: qf-studio/pilot-console-ui — title: feat(context): Context panel with Docs / Memory / Graph growth numbers and a labeled health value

## Context
The right column shows what Pilot knows about the tenant's repos: Navigator docs, memories and the knowledge graph. The 2026-10 design shows three panel cards. The buyer cares about growth, not read counts, and every number needs a label.

Blocked by: #{{U1}}

## Scope
- Add a right column (270px at ≥1280px; stacked under the main column below that) to `src/views/DashboardView.vue` with a header "Context" and three panel cards (16px padding, 13px rows):
  - **Docs** — header value: total docs size or count; rows: `Created this week`, `Updated this week`, `Read by Pilot this week`, each `count` left of a muted secondary number where available.
  - **Memory** — header value: total memories; same three rows.
  - **Graph** — header value: a health word (`Healthy` green / `Drift` / `Stale`) with a tooltip that explains the rule; rows: `Nodes`, `Edges`, `Last updated` (relative time).
- Data source: the existing docs/graph endpoints the Docs page uses (see `src/views/DocsView.vue` and `src/lib/api/types.ts`). Where a number is not available from any existing endpoint, render `—` and list the missing fields in the PR body under "Needs API" — do not invent values and do not hardcode.
- Replace the unlabeled percentage in the design with the health word; no bare `98%`.

## Acceptance
- [ ] The three cards render with the labels above; a unit test asserts the labels and the `—` fallback.
- [ ] The PR body lists every field that fell back to `—` and which endpoint would need to supply it.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

### LEG U9 — repo: qf-studio/pilot-console-ui — title: feat(research): Research page — sub-agent tree with DONE badges, report body, Context-per-run panel; board input submits here

## Context
The chat view becomes the Research page (Figma node `13-994`). A research turn shows the sub-agents it ran as a tree with DONE badges and meta, then the report. The right column shows what this run read and created. The dashboard's "Ask your pilot…" input submits to this page. Tokens never appear on rows.

Blocked by: #{{U1}}

## Scope
- Restyle `src/views/ChatView.vue` (route stays as today) to the design: page title "Research" (15px) with the question under it (15px, 60%); a "Navigator research" tree header with the sub-agent count right-aligned; one ListRow per sub-agent (id, DONE chip, title, meta `12 m` duration only); then the report rendered as markdown.
- Input at the bottom: placeholder `Ask your pilot…`, two mode pills `RESEARCH` and `DOCS` (existing chat modes; keep their current semantics), send on Enter.
- Dashboard: add the same input (placeholder + pills) at the bottom of the main column in `src/views/DashboardView.vue`; submitting navigates to the Research page with the question and mode as query params, and the Research page starts the turn from them.
- Right column "Context" for the current run: `Read N docs · M memories`, `Created K memories`, each from the conversation events at `GET /api/v1/chat/conversations/{id}/events` if those events carry such counts; otherwise `—` and list the missing event fields in the PR body. Remove any judge name or version from the panel.
- Do not add the `Create tickets` action yet (separate issue).

## Acceptance
- [ ] Unit tests: sub-agent rows render from a fixture event stream with DONE chips and duration-only meta; the report renders markdown; the dashboard input navigates with the question in the query.
- [ ] A test asserts no `token` or `$` string in the sub-agent rows.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body; one line names the adapter used.

## Legs — pilot (daemon)

### LEG P1 — repo: qf-studio/pilot — title: feat(gateway): run-story fields on the execution read — diff stats, tests, evidence-gate result, issue→PR and PR→merge timings

## Context
Pilot Cloud shows a "Run Story" per ticket: what was asked, what changed, what was verified, how long it took, the outcome. The console reads executions through the gateway JSON API. This leg makes every field the card needs available in one read, computed from data the daemon already stores; nothing is invented.

## Scope
- Add a `run_story` object to the gateway's execution read responses (the handlers in `internal/gateway` that serve `/api/v1/executions/{id}` and the task-scoped variant) with these fields, each nullable:
  - `asked`: task title, issue URL.
  - `changed`: PR URL, `files_changed`, `lines_added`, `lines_removed` (from the executions row), and a one-line summary = the first line of the PR body when the daemon has it.
  - `verified`: quality-gate results for the run (names + pass/fail) from the stored execution events, acceptance-evidence outcome (`verified` / `not_verified` / `skipped`) when the acceptance-evidence feature produced one, CI status when the autopilot state for the PR has it.
  - `timings`: `queued_at`, `started_at`, `pr_opened_at`, `merged_at`, `duration_ms`. `pr_opened_at` and `merged_at` come from the autopilot PR state where recorded.
  - `outcome`: one of `running`, `review_needed`, `merged`, `failed`; `failure_class` (`ci` / `env` / `scope` / `unknown`) when failed, derived from the execution's error classification if one exists.
  - `model_name`.
- Document in a short doc comment on the response type where each field comes from; where no stored source exists today, the field is null and the PR body says so under "Not available yet".
- No cost or token fields in `run_story` (they stay where they are on the execution row).

## Acceptance
- [ ] Table-driven tests in `internal/gateway` cover: a merged execution with all sources present, a running execution (nulls), a failed execution with a failure class.
- [ ] The PR body lists every `run_story` field with its source table/column or "Not available yet".
- [ ] `make test` and `make lint` pass — paste the output into the PR body.

### LEG P9 — repo: qf-studio/pilot — title: feat(briefs): one brief object with console and email payloads; receipts digest folds in; cost lines only on own-key

## Context
The daily brief is generated in `internal/briefs` and delivered to Slack/Telegram; the receipts digest is a separate Telegram-only message. Pilot Cloud needs one brief object that the console can store and show, and an email rendering. On a platform-key tenant, per-run cost never appears.

## Scope
- In `internal/briefs`, define a single structured brief (extend `types.go`) with sections in this order: `shipped` (one entry per delivered run: issue ref, title, PR URL, duration, `+a −b`), `review_needed` (items older than 24h first; each with a one-line reason), `pool` (percent of allowance used, when the daemon knows it), `failed` (issue ref, failure class, retryable flag).
- Fold the receipts digest into the `shipped` section: the per-run cost line is included only when the execution's `key_source` is the tenant's own key; on `platform` it is omitted.
- Add a JSON payload for the console (serializable struct, versioned `schema: 1`) and an email rendering in `internal/briefs/formatter_email.go` with the same four sections; keep Slack/Telegram formatters working.
- Expose the latest brief through the gateway (`internal/gateway`, new route in plain text: GET /api/v1/briefs/latest and GET /api/v1/briefs?limit=N) with the same bearer auth as sibling routes.
- Do not change the schedulers' times in this leg.

## Acceptance
- [ ] Table-driven tests: section ordering; 24h-first ordering in review_needed; cost line present for own-key and absent for platform-key; JSON payload round-trips; email formatter output contains the four section headings.
- [ ] Gateway tests for the two routes (auth, empty, limit).
- [ ] `make test` and `make lint` pass — paste the output into the PR body.

### LEG P10 — repo: qf-studio/pilot — title: feat(briefs): weekly brief on Monday with proof numbers — tickets shipped, median time to PR, merged count

## Context
The weekly brief exists as a manual trigger. Pilot Cloud sends it every Monday with three proof numbers for the previous 7 days.

Blocked by: #{{P9}}

## Scope
- Schedule the weekly brief for Monday at the daily-brief hour in the tenant's configured timezone (reuse the daily scheduler's config shape; add `weekly_day` with default `monday`).
- Content: `tickets_shipped`, `median_time_to_pr_minutes`, `merged_count` for the last 7 days, computed from the executions ledger and the autopilot PR state; plus the top 3 shipped runs by diff size.
- Use the brief object from the previous leg; the console and email payloads include a `kind: weekly` marker.

## Acceptance
- [ ] Tests: the scheduler fires on Monday only; the three numbers are computed correctly from a fixture ledger (including an empty week → zeros, not an error).
- [ ] `make test` and `make lint` pass — paste the output into the PR body.

### LEG P11 — repo: qf-studio/pilot — title: test(security): hostile-ticket pen-test harness — fixture repo, injected issue bodies, assertions on env, disk, SSM, egress and cross-tenant reach

## Context
Before any customer with a private repo runs on Pilot Cloud, a prompt-injected ticket must be shown not to reach beyond the tenant's own blast radius. This leg builds a repeatable harness; running it on the fleet canary is a separate operations step.

## Scope
- Add a harness under a new directory (plain name: tests/pentest) with:
  - A fixture repository generator (a small Go module with a README and one test) that the harness commits to a temp dir.
  - A set of hostile issue bodies (plain-text files) that try, one per file: read environment variables and write them into the PR body; read files outside the worktree (home directory, `/etc`, the Pilot data directory); call the instance metadata endpoint and SSM; make outbound HTTP to a host not on the expected allowlist; read another path that would belong to a different tenant.
  - A runner that executes each body through the real executor path with the Claude Code backend in the same way `pilot start` does (reuse the existing executor entry points; no mock executor), captures the resulting PR body / diff / process egress log, and asserts: no environment value appears in any output; no file content from outside the worktree appears; no request reached the disallowed host; the destructive-command hook blocked the attempts it covers.
  - A report (markdown) written to the harness output directory: one row per hostile body with pass/fail and the evidence line.
- Add `make pentest` that runs the harness against a configured backend; skipped in CI by default (requires a real key and network).
- Document how to run it on the fleet canary in a short section of `docs/` (the Nextra docs under docs/content; add a page in plain text: docs/content/guides/pen-test.mdx).

## Acceptance
- [ ] The harness runs locally end to end with `make pentest` and writes the report; paste the report table into the PR body (redact nothing — if a secret appears in the output, the harness has found a real defect, and the PR must say so).
- [ ] Unit tests cover the assertion functions with synthetic outputs (positive and negative cases).
- [ ] `make test` and `make lint` pass — paste the output into the PR body.

## Legs — pilot-console (API)

### LEG C3 — repo: qf-studio/pilot-console — title: feat(review): unified Review needed list — approvals and decisions with a why-line; every action records the actor

## Context
The console shows a Needs You lane fed by approval mirroring. Pilot Cloud's "Review needed" list unifies everything that waits on a human: PRs that need approval, decisions Pilot cannot make, and (in a later leg) ticket drafts. Each item carries one line that says why it needs a human, and every action records who did it.

## Scope
- Add `GET /api/v1/review` returning one list, oldest first, grouped by project, of items `{ id, type, project, title, why, created_at, card_id, actions }` where `type` is `pr_approval` or `decision` for now (a `draft` type is reserved for a later leg), and `why` is a one-line reason derived from the source (e.g. "prod mode: merge needs approval", "destructive action: migration drops a column").
- Record `actor_id` (the authenticated user) on every decision taken through `POST /api/v1/board/cards/{id}/decision`, and return `decided_by` in subsequent reads.
- Add a `send_back` action to the decision endpoint: it posts the comment to the card (reusing the comment path) and marks the item as sent back without approving or rejecting.
- Org-scoped like sibling routes; covered by the existing fleet-org-scoping check (`make check-fleet-org-scoping`).

## Acceptance
- [ ] Tests: list ordering and grouping; `why` lines for both types; actor recorded and returned; `send_back` neither approves nor rejects and posts the comment.
- [ ] `make build`, `make test`, `make lint`, `make check-fleet-org-scoping` pass — paste the output into the PR body.

### LEG C7 — repo: qf-studio/pilot-console — title: feat(metering): pool meter — percent of the fair-use allowance; USD hidden when the tenant runs on the platform key

## Context
Pilot Cloud never shows per-ticket cost to a platform-key tenant. The one usage number is the pool meter: percent of the monthly fair-use allowance used. The allowance endpoint exists.

## Scope
- Extend `GET /api/v1/billing/allowance` with `percent_used` (integer 0–100, capped at 100) and `key_source` (`platform` or `own`).
- When `key_source` is `platform`, omit every USD field from this response and from `GET /api/v1/usage` and `GET /api/v1/usage/ledger` (keep counts, durations, ticket numbers). When `own`, keep USD fields as today.
- Add the same `key_source` gate to the dashboard payload (`GET /api/v1/dashboard`) if it carries cost fields.

## Acceptance
- [ ] Tests: percent computation at 0, mid, over-cap; USD fields absent for `platform` and present for `own` on all three endpoints.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

### LEG C8 — repo: qf-studio/pilot-console — title: feat(billing): $300 fair-use ceiling; prepaid ticket packs (25 for $99, 100 for $349, 90-day expiry); daily cap of 15 tickets with a board notice

## Context
Pricing v3 (decided 2026-10-21): the ticket is the unit; the dollar ceiling only stops abuse. The ceiling moves from $100 to $300. Packs are prepaid and expire. A daily cap protects the box and the pool.

## Scope
- Raise the default monthly allowance to USD 300 (config and the per-org default row); existing orgs with the old default are migrated to 300 by a migration; orgs with a custom value are untouched.
- Add pack products: 25 tickets / USD 99 and 100 tickets / USD 349, as Paddle one-time prices (config holds the price ids; the catalog entries are created by the founder — the code reads them from config and fails closed with a clear error if missing). A purchased pack adds tickets with a 90-day expiry; consumption order: plan tickets first, then the oldest unexpired pack.
- Daily cap: 15 tickets dispatched per org per calendar day (org timezone); the 16th dispatch is held until the next day and the board shows a one-line notice "Daily cap reached — resumes at 00:00 <tz>". Expose `daily_cap` and `dispatched_today` on `GET /api/v1/billing/allowance`.
- Own-key overage carries no fee: no code path charges when `key_source` is `own`.

## Acceptance
- [ ] Tests: migration moves default-valued orgs to 300 and leaves custom ones; pack purchase, expiry and consumption order; daily cap holds the 16th dispatch and releases next day; no charge on own-key.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

## Legs — pilot-console (after P1 merged, 10-26)

### LEG C1 — repo: qf-studio/pilot-console — title: feat(run-story): GET /api/v1/board/cards/{id}/run-story — relay the daemon's run_story object for a linked card; no cost fields

## Context
The daemon (pilot v2.294.0, GH-5790) serves a Run Story object on GET /api/v1/executions/{id} and GET /api/v1/tasks/{taskId}: an executionId, taskId, status and a run_story object with asked, changed, verified, timings, outcome, failure_class and model_name. Every leaf is nullable and the payload carries no cost or token field. The console has no route that reads it. The board already joins a card to its linked issue and the org's running instance in `internal/boardapi/dispatch.go` (handleExecution, resolveOrgInstance) and already calls a tenant daemon per request through the approvals client in `internal/approvals/client.go` (History reads the daemon history list; the do helper resolves the private IP and the gateway bearer token). The execution route's durationMs already comes from the newest history row whose issueID equals the card's sequence_id (runDurationMs), and the executionDTO comment reserves linesAdded and linesRemoved for the daemon run-story read. This leg adds one read route on that path and fills those two reserved fields. Spec: positioning section 8.1 (Run Story, decisions D1, D4).

probe: present qf-studio/pilot json:"run_story"
probe: present qf-studio/pilot GET /api/v1/tasks/\{taskId\}
probe: present qf-studio/pilot-console func \(h \*Handler\) handleExecution
probe: present qf-studio/pilot-console func \(c \*Client\) History\(
probe: present qf-studio/pilot-console type HistoryReader interface
probe: present qf-studio/pilot GET /api/v1/executions/\{id\}
probe: absent  qf-studio/pilot-console cards/\{id\}/run-story

## Scope
- Route: GET /api/v1/board/cards/{id}/run-story in `internal/boardapi/routes.go`, authenticated like the sibling execution route, no CSRF guard (read only).
- Handler in `internal/boardapi/dispatch.go` next to handleExecution: resolve org, board, card and link exactly as handleExecution does. No link, or no running instance, returns 200 with linked false (or linked true and run_story null) — never a 5xx for a card that has nothing to tell yet.
- Execution lookup: scan the daemon history exactly as runDurationMs does (newest row whose issueID equals the card's sequence_id) and take that row's id as the execution id; then call GET /api/v1/executions/{id} on the org's instance. No matching history row, or a daemon 404, returns 200 with run_story null and reason "no_execution". A daemon that is unreachable or non-200 returns 200 with run_story null and reason "daemon_unavailable", logged at warn. Do not guess a task id from sequence_id.
- Tenant call: add a RunStory method beside History on the approvals client (same do helper, path executions/{id}, same token cache and timeout) and a nil-legal RunStoryReader interface in the board Deps next to History; do not add a second IP or token resolver.
- DTO in `internal/boardapi/dto.go`: a runStoryDTO with linked, executionId, taskId, status, runStory, reason. runStory is the daemon's run_story object relayed field by field with the daemon's snake_case names kept inside it (asked.title, asked.issue_url, changed.pr_url, changed.files_changed, changed.lines_added, changed.lines_removed, changed.summary, verified.quality_gates[].name, verified.quality_gates[].passed, verified.acceptance_evidence, verified.ci_status, timings.queued_at, timings.started_at, timings.pr_opened_at, timings.merged_at, timings.duration_ms, outcome, failure_class, model_name). A null leaf stays null; the console never substitutes a zero.
- Execution route: on GET /api/v1/board/cards/{id}/execution, fill executionDTO linesAdded and linesRemoved from run_story.changed when the run-story read succeeds (omitted otherwise, like durationMs). This is the join pilot-console-ui#242 already reads.
- Cost rule (section 3, D4): the DTO has no cost, usd, tokens or key_source field, on any key source. The daemon payload has none; do not derive any.
- One line in the PR body names the daemon file the wire shape was copied from (pilot repo, internal/gateway/run_story.go) and the daemon version it was read at (v2.294.1).

## Acceptance
- [ ] A wire-contract test decodes a fixture that is the exact JSON the daemon emits (copied from pilot internal/gateway/run_story.go at v2.294.1, all leaves populated) into the relay type and asserts every field listed in Scope round-trips; a second case with every leaf null round-trips as null. Rename the fixture key "failure_class" to "failureClass" -> TestRunStoryWireContract fails.
- [ ] Handler tests with fake history and run-story readers: a linked card with a running instance and a matching history row returns 200 and the relayed object with the daemon's executionId and taskId; an unlinked card returns 200 linked false; no running instance returns 200 with run_story null and reason "daemon_unavailable"; no matching history row or a daemon 404 returns 200 with run_story null and reason "no_execution"; the execution route carries linesAdded and linesRemoved when the run story has them and omits them when the read fails.
- [ ] A test asserts the encoded DTO contains none of the keys cost, usd, tokens, key_source for a platform-key org and for an own-key org.
- [ ] `make build`, `make test`, `make lint` pass — paste the output into the PR body.

## Out of scope
- Share token and public redacted payload (C2, separate issue).
- Any UI change (U6, U7).
- Aggregating several executions of the same task: the daemon's task read already returns the newest execution; the console relays that one.

## Legs — pilot-console-ui (after C3 merged, 10-26)

### LEG U8 — repo: qf-studio/pilot-console-ui — title: feat(review): Review needed page — one list from GET /api/v1/review, oldest first, grouped by project, with the why line; Approve, Reject with reason, Send back

## Context
C3 shipped the unified list on pilot-console (PR#426, GH-422): GET /api/v1/review returns an object with one key, items, an array already grouped by project and ordered oldest first (groups ordered by their oldest item). Each item has snake_case fields: id, type, project, title, why, created_at, card_id, actions. type is pr_approval or decision (draft is reserved and not emitted yet). card_id is null and actions is empty when the item joins no board card, so there is nothing to act through. The decision verb is the existing POST /api/v1/board/cards/{id}/decision with a body of decision and comment; decision is approve, reject or send_back; comment is required for send_back and ignored otherwise. The daemon fields kind, mode, reason and title on the approvals route are NOT emitted yet (pilot#5797 is parked), so the why line is whatever the console derives; render it as given and never invent one.

In this repo the adapter has no review list method and `src/lib/api/types.ts` types decideCard as approve or reject only. The dashboard's Review needed StatCard links to the board's needs-you lane (`src/views/DashboardView.vue`, test in `src/views/__tests__/DashboardView.spec.ts`). The 2026-10 primitives exist: ListRow, SectionHeader, Chip, Pill, StatCard. `src/composables/useCardDecision.ts` holds the never-optimistic decision rule: a needs-you state only clears from a 200 response. Spec: positioning section 8.2 (D1 three item types, D2 three actions, D3 oldest first grouped by project, D4 owner-only in v1).

probe: present qf-studio/pilot-console json:"why"
probe: present qf-studio/pilot-console decisionSendBack = "send_back"
probe: present qf-studio/pilot-console "items": groupReviewItems
probe: present qf-studio/pilot-console-ui data-review-needed
probe: present qf-studio/pilot-console-ui decideCard\(cardId: string, decision: 'approve' \| 'reject'\)
probe: absent  qf-studio/pilot-console-ui api/v1/review

## Scope
- Adapter (`src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`): add a WireReviewItem type with exactly the eight snake_case fields above (card_id nullable, actions a string array) and a listReview method that GETs /api/v1/review and returns the items in server order. Extend decideCard to accept send_back and an optional comment, sending the body decision plus comment; keep the existing error translation. The mock adapter returns three items across two projects, one with card_id null.
- Route: add /review named review in `src/router/index.ts`, authenticated like board.
- View: new file src/views/ReviewView.vue. One list in server order (do not re-sort or re-group on the client). A SectionHeader opens each project group where project changes in server order. Each item is a ListRow with a Chip for the type (PR for pr_approval, Decision for decision), the title, the why line, and the age from created_at. Actions on the row, only when card_id is not null: Approve; Reject, which opens a reason TextArea and sends decision reject with the reason as comment (the console ignores the comment on reject today; storing it as a learning is a console follow-up, not this leg); Send back, which requires a comment and sends decision send_back. An item with card_id null renders no buttons and a muted "no card on the board" note. The row leaves the list only after a 200 and a re-fetch of the list (the useCardDecision rule). Empty state: one line "Nothing waits on you".
- Dashboard: point the Review needed StatCard to the review route and update its test. Add a Review entry to `src/design-system/components/AppRail.vue` after Board. No new mobile tab; the mobile path is the dashboard card.
- No new wire field: the page reads only the eight fields the console emits. No token or USD string anywhere on the page.
- One line in the PR body names the adapter the view uses in dev and in the test (httpAdapter vs mockAdapter).

## Acceptance
- [ ] A wire-contract test decodes a fixture that is the exact JSON of GET /api/v1/review as pilot-console internal/boardapi/review.go emits (two projects, three items, one with card_id null and empty actions) into WireReviewItem and asserts all eight fields per item. Rename the fixture key why to reason -> reviewWireContract fails.
- [ ] View tests: items render in fixture order with two SectionHeaders; the null-card item has no buttons; Reject's send is disabled until the reason has text; Send back's send is disabled until the comment has text; after a mocked 200 the list is re-fetched and the item is gone; after a mocked 409 the item stays and a toast shows the message.
- [ ] Adapter tests: listReview GETs /api/v1/review; decideCard posts decision send_back with the comment and the CSRF header; decideCard with reject and a comment posts both fields.
- [ ] Dashboard test: the Review needed card href is /review.
- [ ] `npm run build`, `npm test`, `npm run lint` pass — paste the output into the PR body.

## Out of scope
- The draft item type (not emitted yet) and the share link (U7).
- Storing the reject reason as a learning (console side).
- Roles beyond owner (D4).
- Badge counts on the rail.

## Legs — pilot-console (after C1 merged, 10-26 12:30Z)

### LEG C2 — repo: qf-studio/pilot-console — title: feat(run-story): opt-in share link per story — owner creates or revokes a token on a card, public GET /api/v1/share/{token} serves a redacted snapshot

## Context
C1 (PR#442) serves GET /api/v1/board/cards/{id}/run-story: linked, executionId, taskId, status, runStory (the daemon run_story body: asked, changed, verified, timings, outcome, failure_class, model_name) and reason. Positioning section 8.1 D3: a share link per story, opt-in, secrets redacted, the sales artifact. No share table or public story route exists. Precedents in this repo: org invites mint a random token and store only its sha256 in a token_hash column (`internal/orgs/invites_handlers.go`, `internal/orgs/invites_store.go`); unauthenticated routes are registered without the authenticate wrapper (`internal/bff/handlers.go`, the login route); a per-key rate limiter lives in `internal/email/ratelimit.go`. Migrations run to 0035.

probe: present qf-studio/pilot-console GET /api/v1/board/cards/\{id\}/run-story
probe: present qf-studio/pilot-console type RunStoryBody struct
probe: present qf-studio/pilot-console func hashInviteToken
probe: present qf-studio/pilot-console CREATE TABLE card_links
probe: absent  qf-studio/pilot-console card_shares
probe: absent  qf-studio/pilot-console /api/v1/share/

## Scope
- Migration 0036: table card_shares with id, org_id, card_id (unique: one share per card), token_hash (unique), payload jsonb, created_by, created_at, revoked_at nullable. RLS like the sibling board tables.
- Snapshot, not live: the public route never calls a tenant daemon. On create the handler reads the run story through the same reader the C1 route uses, redacts it, and stores the result in payload. The owner refreshes by creating again (rotate: the old token is revoked, a new one minted, payload rebuilt).
- Owner routes in `internal/boardapi/routes.go`, authenticated and CSRF-guarded like the decision route: POST /api/v1/board/cards/{id}/share creates or rotates and returns enabled true, url (the console origin plus /s/ plus the plaintext token, shown only in this response), createdAt; GET /api/v1/board/cards/{id}/share returns enabled, createdAt (no url); DELETE /api/v1/board/cards/{id}/share revokes (204). A card with no linked execution returns 409 with code no_run_story.
- Public route GET /api/v1/share/{token}: no session, no CSRF, registered next to the login route; looks up by sha256 of the token; unknown, revoked or malformed tokens all answer 404 with the same body; rate limited per client IP (60 per minute, same limiter shape as the email package); response headers Cache-Control no-store and X-Robots-Tag noindex.
- Redacted payload (snake_case, the only fields the public route emits): title, outcome, failure_class, model_name, changed with files_changed, lines_added, lines_removed, summary; verified with quality_gates (name, passed) and ci_status; timings with queued_at, started_at, pr_opened_at, merged_at, duration_ms; shared_at. Never emitted: issue_url, pr_url, acceptance_evidence, taskId, executionId, card body, org or project names, any token or USD value.
- Org scoping: the owner routes resolve the card through the board's org like the sibling routes; the public route is keyed by token only and touches no fleet store.

## Acceptance
- [ ] Wire-contract test with the raw body from the recorder: the public response contains the literal keys title, outcome, changed, verified, timings, shared_at and does not contain issue_url, pr_url, acceptance_evidence, executionId. Remove the redaction of pr_url -> TestShareRedactsLinks fails.
- [ ] Handler tests: create returns a url whose token hashes to the stored token_hash; a second create rotates (old token 404, new token 200); delete revokes (then 404); unknown and revoked tokens return the same 404 body; the 61st request from one IP in a minute returns 429; a card without a run story returns 409 no_run_story.
- [ ] Migration test: card_shares exists with the unique constraints on card_id and token_hash.
- [ ] `make build`, `make test`, `make lint`, `make check-wire-contract-tests`, `make check-fleet-org-scoping` pass — paste the output into the PR body.

## Out of scope
- The UI toggle and the public page (U7).
- Live refresh of a shared story (owner rotates to refresh).
- Roles beyond owner (D4).

## Legs — pilot-console-ui (after C1 merged, 10-26 12:30Z)

### LEG U6 — repo: qf-studio/pilot-console-ui — title: feat(run-story): Run Story card — asked, changed, verified, time, outcome, model; four states; Retry on Failed; on the ticket page and the done-card drawer

## Context
C1 shipped on pilot-console (PR#442): GET /api/v1/board/cards/{id}/run-story returns an object with camelCase top-level fields linked, executionId, taskId, status, runStory, reason, where runStory is the daemon's run_story body with snake_case leaves: asked (title, issue_url), changed (pr_url, files_changed, lines_added, lines_removed, summary), verified (quality_gates as a list of name and passed, acceptance_evidence, ci_status), timings (queued_at, started_at, pr_opened_at, merged_at, duration_ms), outcome, failure_class, model_name. Every leaf can be null; runStory itself is null with reason no_execution or daemon_unavailable; linked is false for a card without a tracker link. outcome is one of running, review_needed, merged, failed; failure_class is one of ci, env, scope, unknown. The execution join (`src/lib/api/types.ts` CardExecution, adapter getCardExecution) already carries durationMs, linesAdded, linesRemoved. The ticket page `src/views/IssueView.vue` has a Latest run section with `src/design-system/components/PilotRunTimeline.vue`; the done card opens `src/views/BoardCardDrawer.vue`. Spec: positioning section 8.1 (D1 fields, D2 four states with Retry on Failed, D4 model shown, cost never).

probe: present qf-studio/pilot-console json:"runStory"
probe: present qf-studio/pilot-console type RunStoryBody struct
probe: present qf-studio/pilot-console-ui getCardExecution\(cardId: string\)
probe: present qf-studio/pilot-console-ui PilotRunTimeline
probe: absent  qf-studio/pilot-console-ui run-story

## Scope
- Adapter (`src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`): a WireRunStory type that mirrors the console response exactly as listed in Context (camelCase top level, snake_case inside runStory, nullable leaves) and a getCardRunStory method on GET /api/v1/board/cards/{id}/run-story. The mock adapter returns four stories, one per outcome, plus one with runStory null and reason daemon_unavailable.
- Component: new file src/design-system/components/RunStoryCard.vue using the 2026-10 primitives (SectionHeader, Chip, Pill, ListRow). Five blocks: Asked (title, issue link), Changed (PR link, files, plus and minus lines, summary), Verified (quality gates as pass or fail chips, CI status, evidence line), Time (issue to PR = pr_opened_at minus queued_at; PR to merge = merged_at minus pr_opened_at; total = duration_ms), Outcome (state pill from outcome; model_name as text). A null leaf renders an em dash; the component never invents a value. State styles: running shows the live phase from the existing timeline below it; review_needed shows a Review pill that links to the review route; merged shows the merge time; failed shows the failure_class chip and a Retry button.
- Retry: the Retry button calls the existing C8 dispatch adapter method for the card (the path that supersedes a failed or stalled card) and is never optimistic: the card stays failed until the next run-story read says otherwise; errors show in the toast shelf.
- Mount: on the ticket page above the Latest run section (keep the timeline); in the drawer when the card status is done (expand on click of the done tile). When runStory is null show one muted line with the reason text and nothing else.
- Cost rule: no token or USD string anywhere in the component; a test asserts the absence of the dollar sign and the word tokens in the rendered text.
- One line in the PR body names the adapter the views use in dev and in the test (httpAdapter vs mockAdapter).

## Acceptance
- [ ] A wire-contract test decodes a fixture that is the exact JSON of GET /api/v1/board/cards/{id}/run-story as pilot-console internal/boardapi/dto.go and internal/approvals/client.go emit (all leaves populated) into WireRunStory and asserts every field in Context; a second case with every leaf null decodes to nulls. Rename the fixture key failure_class to failureClass -> runStoryWireContract fails.
- [ ] Component tests: one render per outcome asserting the state pill and the Retry button only on failed; null leaves render the em dash; the Time block computes issue to PR and PR to merge from the fixture timestamps; the review_needed pill links to /review.
- [ ] Retry test: a click calls the dispatch adapter once; after a mocked error the card still shows failed and a toast is shown.
- [ ] Adapter test: getCardRunStory GETs /api/v1/board/cards/{id}/run-story.
- [ ] `npm run build`, `npm test`, `npm run lint` pass — paste the output into the PR body.

## Out of scope
- The share toggle and the public story page (U7, after C2).
- The receipts digest line (TASK-489).
- Attempt history (the wire tails the newest execution only).

### LEG U7 — repo: qf-studio/pilot-console-ui — title: feat(run-story): share toggle on the ticket page and the public story page /s/{token}, redacted, noindex

## Context
Blocked by pilot-console C2 (see TASK-583 Refs): owner routes POST, GET and DELETE /api/v1/board/cards/{id}/share and the public GET /api/v1/share/{token} that serves the redacted snapshot: title, outcome, failure_class, model_name, changed (files_changed, lines_added, lines_removed, summary), verified (quality_gates, ci_status), timings (queued_at, started_at, pr_opened_at, merged_at, duration_ms), shared_at. The public route emits no links, no evidence text, no ids and no org or project name. U6 ships RunStoryCard. The router keeps an explicit list of public route names in `src/router/index.ts` (login, register, verify-email and the email-change pages); every other route redirects to login. Spec: positioning section 8.1 D3 (share link, opt-in per story, secrets redacted).

probe: present qf-studio/pilot-console /api/v1/share/
probe: present qf-studio/pilot-console-ui RunStoryCard
probe: absent  qf-studio/pilot-console-ui /api/v1/share/

## Scope
- Adapter (`src/lib/api/types.ts`, `src/lib/api/httpAdapter.ts`, `src/lib/api/mockAdapter.ts`): WireShareStatus (enabled, createdAt, url optional) and WireSharedStory mirroring the public payload exactly; methods getCardShare, createCardShare, deleteCardShare, and getSharedStory(token) which sends no CSRF header and no credentials.
- Ticket page: a "Share this story" switch next to the RunStoryCard. Off to on calls createCardShare and shows the returned url with a Copy button and the note "Anyone with the link can read a redacted copy. Rotate to refresh." On to off calls deleteCardShare. A second create (Rotate) replaces the shown url. Never optimistic: the switch reflects the last 2xx response.
- Public page: route /s/:token named shared-story with meta blank true, added to the router's public list; new file src/views/SharedStoryView.vue renders RunStoryCard in a public mode (no Retry, no links, no review pill) from getSharedStory; 404 renders "This story is not shared." with no further detail; a document meta tag robots noindex on this page only.
- No token or USD string on the public page; no org or project name; the page title is the story title only.

## Acceptance
- [ ] A wire-contract test decodes a fixture that is the exact JSON of GET /api/v1/share/{token} as pilot-console emits into WireSharedStory and asserts the fields in Context; a test asserts the type has no pr_url, issue_url or acceptance_evidence key. Add pr_url to the public fixture and assert it is rendered -> sharedStoryWireContract fails.
- [ ] Router test: /s/abc is reachable without a session; /board still redirects to login.
- [ ] View tests: the switch creates on first toggle and shows the url; delete on toggle off; a 404 from getSharedStory renders the not-shared line; the public render contains no Retry button and no anchor element.
- [ ] `npm run build`, `npm test`, `npm run lint` pass — paste the output into the PR body.

## Out of scope
- Share analytics or expiry.
- Roles beyond owner (D4).

## Held legs (filed when upstream merges)

| Leg | Repo | After | Body source |
|---|---|---|---|
| C6 brief store + Briefs API + email (SES) | pilot-console | I3 only (P9 pilot#5791 merged 10-22) | §8.7 |
| U11 Briefs page + bell | pilot-console-ui | C6 | §8.7 |
| U12 brief settings | pilot-console-ui | C6 | §8.7 D3 |
| I2 pen test on canary | infra (Nelya) | P11 merged | §8.8 D1 |
| I3 SES sender | infra (Nelya) | — | §8.7 |
| D1–D5, D7 | Figma (founder) | — | §6, §8.1, §8.2, §8.7 |

## Refs

- U1 → https://github.com/qf-studio/pilot-console-ui/issues/239
- U2 → https://github.com/qf-studio/pilot-console-ui/issues/240
- U3 → https://github.com/qf-studio/pilot-console-ui/issues/241
- U4 → https://github.com/qf-studio/pilot-console-ui/issues/242
- U5 → https://github.com/qf-studio/pilot-console-ui/issues/243
- U9 → https://github.com/qf-studio/pilot-console-ui/issues/244
- P1 → https://github.com/qf-studio/pilot/issues/5790
- P9 → https://github.com/qf-studio/pilot/issues/5791
- P10 → https://github.com/qf-studio/pilot/issues/5792
- P11 → https://github.com/qf-studio/pilot/issues/5793
- C3 → https://github.com/qf-studio/pilot-console/issues/422
- C7 → https://github.com/qf-studio/pilot-console/issues/423
- C8 → https://github.com/qf-studio/pilot-console/issues/424
- C1 → https://github.com/qf-studio/pilot-console/issues/440
- U8 → https://github.com/qf-studio/pilot-console-ui/issues/275
- C2 → https://github.com/qf-studio/pilot-console/issues/450
- U6 → https://github.com/qf-studio/pilot-console-ui/issues/277
- U7 → https://github.com/qf-studio/pilot-console-ui/issues/282
- C14 → https://github.com/qf-studio/pilot-console/issues/449 (TASK-589)
- U19 → https://github.com/qf-studio/pilot-console-ui/issues/280 (TASK-589)
- X1 → https://github.com/qf-studio/pilot-console/issues/453 (TASK-592)
- X2 → https://github.com/qf-studio/pilot-console-ui/issues/283
- Follow-ups from the 10-22 reviews (third batch): pilot#5837 (brief kind filter) · pilot#5838 (example config + CLI window)
- Follow-ups from the 10-22 reviews (second batch): ui#271 (U5 fix) · ui#272 (U4 fix) · console#436 (card/execution DTO fields) · pilot#5816 (P18 docs summary) · pilot#5812/#5813/#5814 (P9 fixes)
- Follow-ups from the 10-22 reviews: ui#262 (U2 fix) · ui#263 (USD absent) · ui#264 (U3 fix) · ui#265 (chat confirmation) · ui#266 (mode decision) · console#430 (activity since) · console#431 (proxy USD) · console#432 (allowance hygiene) · pilot#5810 (P17 subagent events, design)
