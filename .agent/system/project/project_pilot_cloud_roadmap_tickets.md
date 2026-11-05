---
name: project_pilot_cloud_roadmap_tickets
description: Pilot Cloud roadmap as tickets per repo (site, console-ui, console API, daemon, infra, design, founder) — wave, spec ref, size. Companion to project_pilot_cloud_positioning.md §10.
type: project
---

# Pilot Cloud roadmap — tickets per project (2026-10-21)

Source of truth for scope: `project_pilot_cloud_positioning.md` (§ refs below). Wave = §10.
Size: S ≤ 1 Pilot run · M = 2–3 runs or an epic · L = epic with ≥4 sub-issues.
Status: 📋 not filed · 🎫 filed (issue #) · 🚧 in flight · ✅ merged. 18 filed 2026-10-22; P16/C14/U19 added 10-22 (TASK-589, daily cap owner) (TASK-582 wave 0, TASK-583 wave 1); P12 = #5788. Execution plan for everything else: TASK-584.

## pilotcloud-site (`qf-studio/pilotcloud-site`) — 9

| # | Wave | Ticket | § | Size | Status |
|---|---|---|---|---|---|
| S1 | 0 | feat(legal): terms, privacy, refunds with the legal entity, address, contact email | 8.6 | S | 📋 blocked: founder facts |
| S2 | 0 | ci(deploy): land Nelya's `deploy-quantflow-aws.yml` (review her PR, first deploy, edge probe) | 8.6 | S | 📋 blocked: Nelya PR |
| S3 | 0 | feat(landing): hero = identity sentence; drop the "orchestration layer" line | 8.6 D1 | S | ✅ site#12 → PR#17 (10-22) |
| S4 | 0 | feat(proof): build-time "Pilot built Pilot: N of M merged PRs" counter (script + section, dated, weekly rebuild) | 8.6 D2 | S | ✅ site#13 → PR#18 (10-22) |
| S5 | 0 | feat(pricing): `$499 a month · 100 shipped tickets · No seats · Your own box`; packs; "fair use" only; price id pinned | 8.6 D3 | S | ✅ site#14 → PR#19 (10-22) |
| S6 | 0 | feat(self-hosted): OSS keeps "secrets never leave"; Cloud column says "a box only you use" | 8.6 D4 | S | ✅ site#15 → PR#20 merged 13:35Z, reviewed APPROVE-w-notes (10-22) |
| S7 | 0 | chore(copy): product name Pilot Cloud site-wide; "Console" only as the app URL | 8.6 D5 | S | ✅ site#16 → PR#21 closed (test red) → PR#23 merged 13:59Z, unreviewed; price constants in `lib/site.ts` still duplicated (10-22) |
| S8 | 4 | feat(security): Security page — blast-radius statement verbatim + last pen-test summary with date | 8.8 D4 | S | 📋 after P11 |
| S9 | 0 | docs(cloud): docs site copy — why-pilot research line, dedicated-box wording next to the self-hosted claim, Pilot Cloud name, §3 proof line (repo: pilot `docs/content/`) | 8.6, 3 | S | ✅ pilot#5820 (merged 10-22) |

## pilot-console-ui (`qf-studio/pilot-console-ui`) — 19

| # | Wave | Ticket | § | Size | Status |
|---|---|---|---|---|---|
| U1 | 1 | feat(design): tokens, type scale, grid and card primitives from Figma `7-2` (foundation for U2–U11) | 6 | M | ✅ ui#239 → PR#245 (10-22) |
| U2 | 1 | feat(dashboard): stat cards — Delivered N/100 · Review needed (count + open button) · Time to PR · Projects; header `Last shipped X min ago` + pool meter | 6.2, 6.5 | M | ✅ ui#240 → epic ui#247–249 / PR#250–252; **reviewed REQUEST-CHANGES** (header bound to 4 dashboard fields no backend sends; StatCard variants unused) → fix ui#262; USD-absent handling ui#263 (10-22); **Time to PR still "—" → C15 console#481 (median from the ledger) + held U22 (11-04)** |
| U3 | 1 | feat(dashboard): activity chart 1D/7D/30D | 6 | S | ✅ ui#241 → PR#254, reviewed APPROVE-w-defects (two charts, 50-event cap, rolling buckets) → ui#264 + console#430 (10-22) |
| U4 | 1 | feat(queue): queue grouped by project with epic trees; rows show `duration · +a −b`, never tokens | 6.1 | M | ✅ ui#242 → epic ui#256–258; PR#261 (stacked superset) merged 14:34Z carrying the component; PR#260 closed as duplicate, ui#257 closed pilot-done 16:20Z; **reviewed REQUEST-CHANGES** (groups/trees/meta bind to `project`/`parentId`/`durationMs` the console never sends; N+1 fetch) → ui#272 + console#436 (10-22); **11-04 audit: console serves the fields since PR#439 but the UI still gates meta off → ui#299 (U21); `GH-GH-N` double prefix on rows + ticket page → ui#298 (U20)** |
| U5 | 1 | feat(context): Context panel shows growth (`Memories 5.0K · +120 this week`), labeled health | 6.4 | S | ✅ ui#243 → PR#255 merged 13:41Z; **reviewed REQUEST-CHANGES** (no Needs-API list; Memory card ignores served `counts`; Graph card = dead prop; no `ds-*` tokens) → ui#271 + pilot#5857 (refiled from #5816) (P18) (10-22) |
| U6 | 1 | feat(run-story): Run Story card on Done (expand) + ticket page: asked / changed / verified / time / outcome / model; 4 states; `Retry` on Failed | 8.1 | L | ✅ ui#277 merged 10-26 |
| U7 | 1 | feat(run-story): share-link view, public, redacted, opt-in toggle | 8.1 D3 | S | ✅ ui#282 → PR#285 (10-26) |
| U8 | 1 | feat(review): Review needed page — one list, oldest first, grouped by project; 3 item types with the "why" line; Approve / Reject with reason / Send back | 8.2 | L | ✅ ui#275 → PR#276 merged 10-26 10:59Z, deployed 11:02Z, unreviewed |
| U9 | 1 | feat(research): Research page per Figma `13-994` — sub-agent tree with DONE badges, report, Context-per-run panel (`Read 12 docs · 4 memories · Created 2`) | 6 research | M | ✅ ui#244 → PR#246, reviewed APPROVE-w-defects (mobile chat lost confirmation cards → ui#265; tree/Context bind to events the daemon never emits → P17; mode pills inert → ui#266 decision) (10-22) |
| U10 | 2 | feat(research): `Create tickets` under the report → drafts → lane; edit before approve; board input submits to this page | 8.3, 6.3 | M | 🎫 ui#287 (11-02, TASK-585) |
| U11 | 1 | feat(briefs): Briefs page newest first; bell shows the unread brief | 8.7 | M | 📋 after C6 |
| U12 | 1 | feat(settings): brief channels (console / email / Slack / Telegram), time + timezone | 8.7 D3 | S | 📋 after C6 |
| U13 | 4 | feat(run-story): live log tail on Running — stage timeline + tool-call lines, no transcript | 8.5 | M | 📋 after C9 |
| U14 | 3 | feat(onboarding): new-project interview (≤5 questions with defaults) on the Research page → `Create tickets` | 7 | M | 📋 after P4 |
| U15 | 3 | feat(onboarding): legacy path — scan progress + "add up to 3 external docs" (URL / upload) | 7 | M | 📋 after P5, P6 |
| U16 | 5 | feat(members): Viewer role invite with project scope; "Approved by …" on lane items | 9.1 | M | 📋 after C11 |
| U17 | 5 | feat(mobile): mobile layer on the new design (mobile v1 landed 08-15 as PR#84 — the re-land wording was stale); Playwright 390×844 check | 9.6 | L | 🚀 [TASK-637](../../tasks/TASK-637-mobile-on-the-new-design.md): M1 ui#345 → PR#350 · M2 ui#346 → PR#349 · M3 ui#347 → PR#352 · M4 ui#348 → PR#351 all merged 11-05, deployed 13:03Z; M5 ui#357 (review defects) re-armed |
| U18 | 5 | feat(docs): edit + create `.agent/` docs from the Docs page, PR per change | 9.5 | M | 📋 |
| U19 | 1 | feat(board): daily cap banner from the cards response `notice`; `daily_cap_reached` toast names the resume time | TASK-589 | S | ✅ ui#280 → PR#284 (10-26, run 3; runs 1–2 → pilot#5864) |
| U30 | 1 | feat(board): desktop Board per Figma `7-482` — project-grouped list replaces the lanes, status chips incl. FAILED / BLOCKED, moves in the drawer, right column `Performance · N PRs` | 6.1 | L | 📋 [TASK-639](../../tasks/TASK-639-desktop-board-page-grouped-list.md) B1 → B2, holding on two founder calls |

Alignment legs U20–U29 (11-04/05, TASK-583 § 2026-11-04 audit + founder design walk-through): all landed — U20 ui#298 · U21 ui#302 · U22 → U28 ui#334 · U23 ui#307 · U24 ui#308 · U25 ui#309 · U26 ui#310 · U27 ui#311 · U29 ui#335. Live: Home and Research match Figma 7-2 / 13-994.

## pilot-console (API, `qf-studio/pilot-console`) — 14

| # | Wave | Ticket | § | Size | Status |
|---|---|---|---|---|---|
| C1 | 1 | feat(run-story): `GET /orgs/{org}/cards/{id}/run-story` — aggregate executions + events + PR data; cost fields omitted on platform key | 8.1 | M | ✅ console#440 → PR#442 merged 10-26 11:40Z (revision #448 added raw-body wire tests), unreviewed |
| C2 | 1 | feat(run-story): share token (opt-in), redacted public payload | 8.1 D3 | S | ✅ console#450 → PR#452 merged 10-26 |
| C3 | 1 | feat(review): unified Review needed list — approvals + spec drafts + decisions; actions carry `actor_id` | 8.2 | M | ✅ console#422 → PR#426 hand-merged, D1 → pilot#5858 (refiled from #5797) (10-22) |
| C4 | 2 | feat(review): reject reason → learning write-through to the tenant daemon | 8.2 D2 | S | 🎫 console#469 (11-02, TASK-585) |
| C5 | 2 | feat(drafts): ticket-draft store; approve → GitHub issue with the `pilot` label → Queued; edit before approve | 8.3 | M | ✅ console#459 → PR#463 (11-02, via revision #465) |
| C6 | 1 | feat(briefs): brief store + Briefs API; email channel (SES); tenant time + timezone | 8.7 | M | 📋 after P9, I3 |
| C7 | 1 | feat(metering): pool meter endpoint (% of the $300 ceiling); USD hidden on platform key | 4 | S | ✅ console#423 → PR#427 recovery, reviewed APPROVE-w-notes → console#431 (proxy /metrics USD gap), console#432 (hygiene), ui#263 (10-22) |
| C8 | 1 | feat(billing): $300 ceiling config; Paddle products for packs 25/$99 and 100/$349 (90-day expiry); daily cap 15 notice | 4 (v3) | M | ✅ console#424 → PR#425, D1 → TASK-589 (10-22) |
| C9 | 4 | feat(ws): `/ws/dashboard` proxy to the tenant gateway for the live log | 8.5 D3 | M | 📋 after P7, P8 |
| C10 | 2 | chore(research): verify the daily research-turn cap (50) against the shipped chat-turn metering; adjust | 8.3 D4 | S | ✅ console#460 → PR#461 (11-02) |
| C11 | 5 | feat(viewer): `viewer` role with `project_ids` scope enforced on every read | 9.1 | M | 📋 |
| C12 | 4 | feat(db): Postgres RLS — `FORCE ROW LEVEL SECURITY` + `app.current_tenant_id` | 8.8 D3 | M | 📋 |
| C13 | 3 | feat(onboarding): bootstrap trigger per project, cost cap from TASK-581, status + result surface | 7 | M | 📋 after P5 |
| C14 | 1 | feat(fleet): daily ticket cap owned by the daemon — render cap + timezone into tenant config, standing → board notice, delete both console gates, pack draw at ledger ingest | TASK-589 (PR#425 D1) | M | ✅ console#449 → PR#451 merged 10-26, reviewed APPROVE-w-notes |

## pilot (daemon, `qf-studio/pilot`) — 18

| # | Wave | Ticket | § | Size | Status |
|---|---|---|---|---|---|
| P1 | 1 | feat(gateway): run-story fields per execution — diff stats, tests passed/failed, evidence-gate result, issue→PR and PR→merge timings | 8.1 | M | ✅ pilot#5790 → PR#5828 merged 10-22 21:29Z (rebuild after PR#5805 manual-rebase hold), unreviewed; in v2.294.0 |
| P2 | 2 | feat(research): `Create tickets` — draft N tickets from a research report in SOP format (H2, Acceptance, `file:line`, `#N` deps); shippable check; epic split on failure | 8.3 | L | ✅ pilot#6010 (merged 11-02) |
| P3 | 2 | feat(memory): reject reason from the lane → learning memory + graph index | 8.2 D2 | S | ✅ pilot#6011 → PR#6013 (11-02) |
| P4 | 3 | feat(bootstrap): new-project interview (≤5 questions, defaults) → scaffold ticket + 5–10 first tickets per the onboarding SOP | 7 | L | 📋 |
| P5 | 3 | feat(bootstrap): legacy scan — repo, README, `docs/`, open issues, last 50 merged PRs → proposed tickets; cost cap + degrade path | 7, TASK-549/581 | L | 📋 after P12 |
| P6 | 3 | feat(bootstrap): external docs ingestion v1 — URL / upload → `.agent/system/`, indexed by Navigator | 7 | M | 📋 |
| P7 | 4 | feat(gateway): redaction scrubber at the gateway edge — `secretpatterns` + env-name rule — on event `detail` and the stream | 8.5 D2 | M | 📋 |
| P8 | 4 | feat(gateway): tool-call line events (`read` / `edit +a −b` / `run … ✓`), 7-day retention | 8.5 D1/D4 | M | 📋 |
| P9 | 1 | feat(briefs): one brief object with a console payload + email payload; receipts digest folds in; cost lines only on own-key | 8.7 | M | ✅ pilot#5791 → PR#5799, in v2.293.0 (14:27Z); reviewed APPROVE-w-defects (receipts digest fail-open on `unknown` → #5812; fixed 09:00 window → #5813; no `timezone` field → #5814; email formatter unused, C6 decides transport; wire unconsumed). All three fixes merged same day: PR#5823/#5830/#5834, unreviewed (10-22) |
| P10 | 1 | feat(briefs): weekly Monday brief with proof numbers (tickets shipped, median time to PR, merged) | 8.7 D4 | S | ✅ pilot#5792 → PR#5822 merged 19:25Z by autopilot; reviewed APPROVE-w-defects (Monday daily+weekly race on `/latest`, no `kind` column → #5837; example config + CLI window → #5838; `merged_count` 0 where autopilot never tracks) (10-22) |
| P11 | 1 | test(security): hostile-ticket pen-test harness — fixture repo, injected issue bodies, assertions (env, disk, SSM, egress, cross-tenant) | 8.8 D1 | L | 🚧 pilot#5793 → PR#5795 merged but REQUEST-CHANGES: harness scored 8 backend failures as PASS → fix #5798; hard gate NOT met (10-22) |
| P12 | 3 | feat(bench): bootstrap cost harness + the 4-repo × 3-stage × 2-run matrix | TASK-581 | M | ✅ #5788 → PR#5789 (10-22) |
| P13 | 5 | feat(templates): deploy-template ticket sets — VPS (Compose + Actions over SSH), AWS | 9.2 | L | 📋 |
| P14 | 5 | feat(pm-mode): preview URL on the Run Story + merge-on-green per project behind the evidence gates | 9.4 | L | 📋 |
| P15 | 1 | fix(executor): `modelPricing` — Opus 5.5 $4/$20, Sonnet 5.5 + Sonnet 5 $2/$10, Haiku 5.5 $0.10/$0.50, cache-read rate per model (TASK-581 § Found) | 4 | S | ✅ pilot#5821 (closed) |
| P16 | 1 | feat(executor): daily ticket cap held at `ExecutionLifecycle.Begin` — `executor.daily_ticket_cap` + timezone keys, org-local day, `GET /api/v1/executions/daily-cap` standing | TASK-589 | M | ✅ pilot#5794 epic → #5807/#5809 (10-22) + #5808 → PR#5835 merged 10-23 12:38Z, unreviewed; in v2.294.0 |
| P17 | 1 | feat(web): `subagent` + `context` chat events (`durationMs`, `docsRead`, `memoriesRead`, `memoriesCreated`) for the Research page — U9 renders `—` until then | 6 research | M | 🎫 pilot#5810, needs design (10-22) |
| P18 | 1 | feat(gateway): docs + graph summary read (`created/updated this week`, memory totals, graph nodes/edges/updatedAt) for the Context panel — U5 renders `—` until then | 6.4 | M | ✅ pilot#5857 → PR#5859 (10-26) |

## infra (fleet, Nelya / `aws-infrastructure-pilot`) — 4

| # | Wave | Ticket | § | Size | Status |
|---|---|---|---|---|---|
| I1 | 4 | egress domain-allowlist proxy on the tenant AMI (Anthropic, GitHub, trackers, npm / Go proxy / PyPI); egress logged | 8.8 D2 | L | 📋 |
| I2 | 1 | pen-test run on the canary tenant before every AMI roll (uses P11) | 8.8 D1 | S | 📋 after P11 |
| I3 | 1 | SES sender for brief email (per `console-email-transport-ses…` memory) | 8.7 | S | 📋 |
| I4 | 0 | site deploy workflow PR to `pilotcloud-site` (Nelya) | 8.6 | S | 🚧 waiting on Nelya |

## design (Figma `BfAqtVPBp0i4iWHg2jzAnB`, founder) — 7

| # | Wave | Ticket | § | Status |
|---|---|---|---|---|
| D1 | 1 | Dashboard `7-2`: apply §6 — no-cost rows, pool meter, "Review needed" card + button, `Last shipped`, Context growth | 6 | 📋 |
| D2 | 1 | Research page `13-994`: `Create tickets` exit, duration-only rows, Context-per-run panel | 6 | 📋 |
| D3 | 1 | Review needed page: list, 3 item types, 3 actions | 8.2 | 📋 |
| D4 | 1 | Ticket page with the Run Story (4 states, `Retry`, share toggle) | 8.1 | 📋 |
| D5 | 1 | Briefs page + bell unread state | 8.7 | 📋 |
| D6 | 3 | Onboarding: new-project interview + legacy scan + external docs step | 7 | 📋 |
| D7 | 1 | Settings: brief channels, time/timezone, members with Viewer scope | 8.7, 9.1 | 📋 |

## founder / ops — 4

| # | Wave | Item | Status |
|---|---|---|---|
| F1 | 0 | Legal entity, address, contact email → unblocks S1 | ⏳ |
| F2 | 0 | Paddle website resubmit after the first deploy (TASK-497 L7) | ⏳ after S1, S2 |
| F3 | 3 | Run the TASK-581 matrix on the box; pick the bootstrap cap | ⏳ after P12 |
| F4 | 1 | Review Nelya's deploy PR (I4) | ⏳ |

## go-to-market (§11) — 3

| # | Wave | Item | § | Size | Status |
|---|---|---|---|---|---|
| O1 | 1 | GitHub Marketplace listing for the Pilot GitHub App — listing copy from §1/§4, screenshots from the new design, install → console signup link; verified-creator requirements. **Research 10-22: a paid listing needs 100 installs first — ship the free listing first.** | 11 D1(b) | M (ops + site) | 📋 after S1–S7, U1–U2 |
| O2 | 0 | Web deep-research: dev-agency market size (3–15 eng) and the channels agencies buy tools through; sourced report → §11 | 11 D3 | M (nav-deep-research) | ✅ `.agent/research/many-software-development-agencies-3-15/report.md` (2026-10-22) |
| O3 | 1 | Design-partner outreach: list of 20 agencies, one-page offer (proof counter, free bootstrap, $499 / 100 tickets), 5 signed | 11 D2 | founder | ⏳ after W0 |

**Totals:** site 8 · console-ui 18 · console API 13 · daemon 14 · infra 4 · design 7 · founder 4 · go-to-market 3 = **71**.
Launch-blocking (waves 0–1): S1–S7, U1–U9, U11–U12, C1–C3, C6–C8, P1, P9–P11, I2–I4, D1–D5, D7, F1, F2, F4 = **40**.

Dispatch order inside wave 1: U1 → (U2–U5, U9) ‖ P1 → C1 → U6 → C2 → U7 ‖ C3 → U8 ‖ P9 → I3 → C6 → U11, U12 ‖ P11 → I2 ‖ C7, C8.
