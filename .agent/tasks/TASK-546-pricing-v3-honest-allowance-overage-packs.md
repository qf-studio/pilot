# TASK-546: Pricing v3 — honest ticket allowance + prepaid overage packs at a value price

**Status**: ✅ **11-02 audit — ALL DISPATCHED LEGS MERGED; price unchanged at $499.** Pilot [#5676](https://github.com/qf-studio/pilot/issues/5676)/[#5677](https://github.com/qf-studio/pilot/issues/5677)/[#5678](https://github.com/qf-studio/pilot/issues/5678) (key_source + reported cost · chat-turn rows · daily chat cap) · console [#401](https://github.com/qf-studio/pilot-console/issues/401) ledger · [#402](https://github.com/qf-studio/pilot-console/issues/402) tickets-or-USD · [#403](https://github.com/qf-studio/pilot-console/issues/403) daily caps as plan data · [#404](https://github.com/qf-studio/pilot-console/issues/404) invites · [ui#231](https://github.com/qf-studio/pilot-console-ui/issues/231) Members page · **[console#424](https://github.com/qf-studio/pilot-console/issues/424) (10-22, C8 of TASK-583): $300 fair-use ceiling + prepaid packs 25/$99 and 100/$349, 90-day expiry, pack prices from config, ledger draws** · daily ticket cap owner moved to the daemon (TASK-589: console#449 → PR#451, ui#280 → PR#284). **Unfiled (Phase 3 remainder)**: `POST /api/v1/billing/packs/checkout` + `transaction.completed` credit via `billing_events`, Buy 25 / Buy 100 UI + balance card. **Unfiled (Phase 5)**: site/console/Paddle copy, `PRICING-MODEL.md` v3 banner, `UNIT-ECONOMICS.md` pack rows. Founder decisions D1–D11 for the price-bearing parts still open (D2 value, D3–D5 packs, D11). Boxes below are ticked only where a merged issue proves them.
**Created**: 2026-10-13
**Assignee**: Navigator plans · Pilot executes per repo · founder decides prices
**Parent**: [TASK-405](TASK-405-pilot-saas-platform.md) S5 billing · **Builds on**: [TASK-502](TASK-502-included-tokens-platform-key.md) (allowance + platform key), [TASK-497](TASK-497-paddle-billing-integration.md) (Paddle)
**Research**: [`research/ai-coding-agent-products-price-extra/report.md`](../research/ai-coding-agent-products-price-extra/report.md) (47 sources, 2026-10-13) · mem-233..mem-243

---

## Context

**Problem** (three parts, all measured, none modelled):

1. **The promise and the meter disagree.** The site sells "100 tickets a month included on Sonnet 5.5". The console enforces `included_usd_cents=10000`: $100 of model usage at Anthropic list price. At the Sonnet 5.5 planning cost ($1.00/ticket) those are the same thing. At the measured Sonnet 5 cost ($3.15/ticket) the cap fires near ticket 32. At the 375-task mixed window ($3.51/ticket) it fires near ticket 28. A medium org that picks a heavier model hits the wall in days and we are the ones who look dishonest.
2. **Heavy use earns us nothing.** After the cap the box switches to the customer's own Anthropic key. Every ticket past 100 (or past 32) is revenue for Anthropic and zero for us. The founder's instinct — "they start using the API and we have nothing from this" — is correct.
3. **Cost-plus does not fix it.** The founder asked why not charge per ticket at measured cost + 1-4%. The research answer: it loses money on every invoice. Paddle takes 5% + $0.50 per charge, so a 4% markup is under the payment fee before the $52/month box share or the control plane is paid. At 100 tickets, cost +1-4% nets **-$56.55 to -$2.32** per tenant/month against **+$321.55 (64%)** on today's flat plan. Cost-plus also hands every model-price cut to the customer (Sonnet 5 → 5.5 cut cost/ticket ~80%; a flat plan keeps that as margin).

**Goal**: keep the flat $499/month, no-seats plan as the headline, and make two changes:

- **A. Honest allowance.** The enforced unit becomes *tickets*, with a fair-use USD ceiling instead of a $100 cap that is the real limit.
- **B. Prepaid overage packs.** Extra tickets are sold in prepaid packs per shipped ticket at a *value* price (research range **$3.38–$4.90** for 50–64% margin), run on the platform key, and are consumed after the included allowance. When packs run out the box falls back to the customer's own key exactly as today. BYO-key overage stays free of any platform fee.

What the market does (report § At a glance): every vendor sells a base fee + included allowance, bills overage at or near API rates, and protects the pool with daily/weekly windows and prepaid credits that roll over (Devin, Factory, Lovable, Windsurf). Nobody charges cost + a few percent. The only per-outcome pricing is in support agents (Intercom $0.99/resolution, Agentforce $2) and it hinges on a strict definition of "resolved".

---

## Known Pitfalls & Patterns

<!-- memory_recall on billing/pricing/paddle returned nothing; these are the TASK-497/502 learnings that bind here. -->

- **PITFALL** Paddle price objects are immutable once used (TASK-497 § L7) → packs need **new** one-time price objects; `PILOT_CONSOLE_BILLING_PRICE_ID` for the subscription is untouched. Reflected: Phase 3 creates prices in sandbox then live, never edits.
- **PITFALL** `usage_rollup` rows are cumulative daemon counters; they reset on daemon restart and undercount (TASK-502 research) → pack consumption cannot be metered from snapshot deltas. Reflected: Phase 1 adds a per-ticket ledger before any pack is debited.
- **PITFALL** The daemon does not record which key served an execution (TASK-502 leg 2 narrowed to `key_source: own`) → a pack-funded ticket is indistinguishable from an own-key ticket today. Reflected: Phase 1 pilot-repo leg tags `key_source` per execution.
- **PITFALL** Issue bodies must say "a new migration", never a number (`issue-body-literal-migration-version-collides-with-sibling-leg`). Reflected in every console leg.
- **LEARNING** Mutation-check the wiring; three console PRs passed unit tests with unpinned wiring (TASK-497) → every leg's acceptance names the mutation that must fail.
- **DECISION** `plan_allowances` is data, not a constant, editable via `consolectl billing set-allowance` (TASK-502) → fair-use ceiling and pack prices live in DB rows, never Go constants.

---

## Decisions for the founder (gate for dispatch)

| # | Question | Recommendation | Why |
|---|---|---|---|
| D1 | Enforced unit for the included allowance | **Tickets (100), with a fair-use USD ceiling** read from `included_usd_cents` raised to a ceiling value, whichever exhausts first | Honours the promise for Sonnet 5.5 and Sonnet 5 workloads; keeps a hard cap on our token exposure |
| D2 | Fair-use ceiling value | **$300/month** (3× planning cost) | Stress case $3.15 × 100 = $315 → margin floor 24%; planning case unchanged at 64%. Below $300 the ceiling, not the ticket count, is the real limit again |
| D3 | Pack sizes and prices | **25 tickets / $99** ($3.96/ticket) and **100 tickets / $349** ($3.49/ticket) | Inside the research band $3.38–$4.90; the 100-pack is cheaper per ticket than the included 100 ($4.99) so volume is rewarded |
| D4 | Pack validity | **12 months, roll over, no monthly expiry** | Market norm for top-ups (Lovable, Devin, Factory); prepaid = no bill shock, no dunning |
| D5 | Per-ticket cost ceiling inside a pack | **$3.50** (pack price ÷ 1) → a ticket whose platform-key spend exceeds it still counts as one ticket; we eat the delta | Keeps the unit simple for the customer; the delta is bounded by the daily cap (D6) |
| D6 | Daily burst cap on platform-key tickets | **15 shipped tickets/day default**, org-overridable | Market standard is daily/weekly windows; stops a runaway loop from draining a pack in one afternoon; also keeps the box duty cycle inside the $52 infra assumption |
| D7 | Fee on BYO-key overage | **None, for now** | "Never marked up on your own key" stays a verifiable claim; Cursor's $0.25/M on Teams shows a fee is accepted later if heavy BYO users raise box cost |
| D8 | Billable ticket definition | **A distinct task whose execution opened a PR** (retries of the same task are one ticket; failed tasks are free) | Matches `succeeded_tasks` semantics, aligns with outcome pricing, avoids the "paid for failed output" complaint Devin gets |

| D9 | Team access behind "No seats" | **Add org invites (owner invites by email, role `member`) as Phase 6** | Verified 2026-10-13: `organizations` + `org_members(role owner|member)` exist, but the only write is the owner row at org creation; no invite endpoint, UI types say "invites/member mutation are a later" leg; `UNIQUE (user_subject)` = one org per user. Today an org is one person, so "move the whole team to Console" is a promise the product cannot keep |
| D10 | Side chat usage | **Meter chat turns into the ledger as their own unit and count their dollars against the fair-use ceiling (D2), not against tickets; org-level daily cap 200 turns** | Verified 2026-10-13: a chat turn is a full agentic Claude Code session (`ExecuteChatTurn`, up to 40 turns, 5 min, read-only tools) on the box's key — the platform key while under allowance. `BackendResult` carries `TokensInput/Output` but `ChatTurnResult` drops them; no `executions` row, no `RecordTaskUsage`, no Prometheus counter, so chat spend is invisible to `usage_rollup` and **uncapped by the allowance**. Only limits today: per-conversation 20 msgs/min, burst 5, one turn at a time, 40 max turns, 5 min timeout — all per conversation, none per org |
| D11 | Legacy-repo bootstrap cost | **Count one bootstrap as 10 included tickets; dollars go to the fair-use ceiling** (detail and build in [TASK-549](TASK-549-legacy-repo-navigator-bootstrap.md)) | On a legacy repo the scaffold's context sections are placeholders, nothing fills them, and worktree learnings never copy back — every ticket and chat turn is a cold start. That is the delivery gap and the token gap at once |

Founder answers "D1–D11 ok" or sends changes. No dispatch before that.

---

## Acceptance Criteria

- [x] (console#402) The plan-allowance row carries both `included_tickets` and a fair-use `included_usd_cents`; enforcement exhausts on **whichever first**, and the 80%/100% alerts name which one fired.
- [x] (console#401) A per-ticket ledger exists in the console: one row per billable ticket with task id, instance, period, model, tokens, list-price USD, `key_source ∈ {platform, pack, own}`, and the pack id when funded by a pack.
- [x] (pilot#5676) The daemon tags every execution with `key_source` and stores the CLI-reported `total_cost_usd` beside `estimated_cost_usd`.
- [ ] A customer can buy a pack from Plan & Billing; the Paddle one-time transaction credits a `ticket_packs` row via the existing `billing_events` idempotency ledger; balance and expiry are visible in the UI and via `GET /api/v1/billing/allowance`.
- [ ] Consumption order is included → packs (oldest expiry first) → own key; the own-key fallback behaves exactly as TASK-502 leg 3 today.
- [x] (console#403 → daemon-owned, TASK-589) The daily burst cap pauses platform-key dispatch at the limit, resumes at the next UTC day, and never blocks own-key dispatch.
- [ ] Site, console copy and the Paddle product description agree: "100 tickets a month included on Sonnet 5.5 (fair use: $300 of model usage)", "Extra tickets from $3.49", "Overage on your own key is never marked up".
- [x] (console#404 + ui#231) An org owner can invite a second member by email and that member sees the same board, usage and billing pages (no per-seat charge).
- [ ] `PRICING-MODEL.md` carries a v3 decision banner; `UNIT-ECONOMICS.md` gains the pack margin rows and the cost-plus rejection with the arithmetic.

---

## Implementation

### Phase 0: Founder decisions
**Goal**: D1–D8 closed, pack price objects created in Paddle sandbox.
- [ ] Founder answers D1–D8.
- [ ] Create two one-time price objects in Paddle sandbox (`paddle-sandbox` MCP) and record ids in `system/references/reference_paddle_account.md`.

### Phase 1: Billable-ticket ledger (pilot repo + console)
**Goal**: every shipped ticket is one attributable row before anything is debited.
- [x] (pilot#5676) **pilot** — `executions` gains `key_source TEXT` and `reported_cost_usd REAL` (CLI `total_cost_usd`); the executor sets `key_source` from `PILOT_ANTHROPIC_KEY_SOURCE` at run start.
- [x] (pilot#5677) **pilot** — chat turns are recorded too: `ExecuteChatTurn` copies `TokensInput/Output/Cache` and cost from `BackendResult` into `ChatTurnResult`; the comms handler writes a `chat_turns` row (conversation, project, model, tokens, cost, key_source, duration, partial) and bumps `pilot_chat_turns_total` / `pilot_chat_cost_usd_total`; `/api/v1/metrics` exposes the chat totals beside execution cost. Mutation that must fail: drop the chat row write → allowance test with chat-only spend must still exhaust. `/api/v1/metrics` (or a new `/api/v1/executions?since=`) exposes per-task rows with these fields.
- [x] (console#401) **console** — a new migration adds `ticket_ledger` (org, instance, period, task_id, model, tokens, list_usd_cents, reported_usd_cents, key_source, pack_id NULL, shipped_at); the `UsagePoller` ingests per-task rows idempotently on `(instance_id, task_id)` instead of relying only on cumulative counters.
- [ ] Mutation that must fail: drop the `key_source` write → ledger test asserts `platform` rows exist for a platform-key run.
**Files**: `internal/memory/store.go`, `internal/executor/runner.go`, `internal/gateway/server.go` (pilot) · `internal/fleet/usagepoller.go`, `internal/fleet/usage_meter.go`, `internal/db/migrations/` (console).

### Phase 2: Honest allowance (console + reconciler)
**Goal**: tickets are the unit; the USD figure is a fair-use ceiling.
- [x] (console#402) `allowance/status.go` returns both meters and `exhausted_by ∈ {tickets, usd}`; the USD meter includes chat-turn dollars (D10); `allowanceenforce.go` switches to own key on either.
- [ ] `consolectl billing set-allowance --tickets 100 --usd 300` run in prod after deploy (operator step, recorded in the SOP).
- [ ] UI "included: 37 of 100 tickets · $41 of $300 fair use".
- [ ] Mutation that must fail: enforcement that reads only USD → test with 100 tickets at $0.50 each must exhaust on tickets.

### Phase 3: Prepaid packs (console + console-ui)
**Goal**: a customer buys tickets; the box keeps running on the platform key.
- [x] (console#424) A new migration adds `ticket_packs` (org, paddle_transaction_id, tickets_total, tickets_used, price_cents, purchased_at, expires_at) and `pack_prices` (code, tickets, price_id_sandbox, price_id_live, active).
- [ ] `POST /api/v1/billing/packs/checkout` creates a Paddle one-time transaction with `custom_data.org_id` and `pack_code`; the webhook handler on `transaction.completed` inserts the pack through `billing_events` dedupe.
- [x] (console#424 ledger draws) Consumption: `allowanceenforce.go` debits `ticket_ledger` rows to the oldest unexpired pack when included is exhausted; own-key fallback only when no pack has balance.
- [ ] UI: pack balance card, "Buy 25 / Buy 100" buttons, expiry, history.
- [ ] Mutation that must fail: remove the dedupe check → replayed `transaction.completed` must not credit twice.

### Phase 4: Daily burst cap (console + reconciler)
**Goal**: no pack drains in an afternoon; the box duty cycle stays bounded.
- [x] (console#403; owner now the daemon, TASK-589) `plan_allowances.daily_ticket_cap` (default 15) + org override; reconciler pauses platform-key dispatch at the cap with a 429-style event; resumes next UTC day.
- [x] (console#403) Own-key dispatch is never paused by this cap.
- [x] (pilot#5678) Org-level daily chat cap (`plan_allowances.daily_chat_turn_cap`, default 200): the daemon's web adapter gets an org-wide limiter beside the per-conversation one; over the cap the chat replies "Daily chat limit reached" instead of running a session.

### Phase 5: Copy, catalog, docs
**Goal**: site, console, Paddle description and Navigator docs say the same thing.
- [ ] `pilotcloud-site`: `pricing-strip.tsx`, `site.ts`, `compare.ts`, `inside-scenes.tsx` lines per § Acceptance.
- [ ] Paddle product description updated by hand (operator step; reviewers compare site vs catalog).
- [ ] `product/PRICING-MODEL.md` v3 banner; `product/UNIT-ECONOMICS.md` pack rows + cost-plus rejection table; `product/PRICING.md` marked historical.

### Phase 6: Org invites (console + console-ui) — makes "No seats" deliverable
**Goal**: an owner can add teammates; usage stays per org.
- [x] (console#404) `POST /api/v1/org/members` (owner-only, CSRF-guarded) creates an invite by email; acceptance on first auth-service login binds `user_subject` → `org_members` row with `role='member'`; `DELETE /api/v1/org/members/{subject}` removes.
- [ ] Decide whether `UNIQUE (user_subject)` stays (one org per user) — keep for v3; multi-org membership is out of scope.
- [x] (ui#231) UI: Members page with invite form and role chip; the existing `members: OrgMember[]` wire type gains `status ∈ {active, invited}`.
- [ ] Mutation that must fail: a `member` calling the invite endpoint must get 403.
**Files**: `internal/orgs/handlers.go` (routes at :129-137), `internal/orgs/store.go` (`ListMembers` :313, owner insert :233), migration `0006_orgs_connections` schema; UI `src/lib/api/types.ts`, `src/stores/orgDetail.ts`.

**Dispatch order**: 0 → 1 (pilot leg and console leg parallel) → 2 → 3 → 4 → 5; **6 is independent and can start with 1**. Phases 2 and 4 are small and can ride together.

---

## Out of Scope

- Seats. "No seats" stays the headline; the research shows seats fit a people-metered product, not a ticket-metered one.
- Cost-plus per-ticket billing (rejected; arithmetic in the research § Q4 and in `UNIT-ECONOMICS.md` after Phase 5).
- A platform fee on BYO-key usage (D7 = none; revisit if heavy BYO users raise box cost).
- Tiered plans (100 / 400 / custom). Packs cover the same need without a second subscription price object.
- Model-choice UI (PRICING-MODEL.md § Model policy) — separate task; packs assume the Sonnet 5.5 default.
- Replacing the dormant `internal/memory/metering.go` scaffold in the pilot repo — leave in place, do not extend.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Overage unit | per token at cost+%, per execution, per shipped ticket, seats | Per shipped ticket (D8) | Tickets are what we sell and what the customer can count; cost+% is below the payment fee; executions punish our own retries |
| Overage billing mode | post-paid metered invoice, prepaid packs, auto top-up | Prepaid packs, optional auto-reload later | No bill shock, no dunning, no credit exposure; matches Devin/Factory/Lovable; one Paddle one-time transaction per pack |
| Where prices live | Go constants, env, DB rows | DB rows (`pack_prices`, `plan_allowances`) | Founder requirement "easy to configure"; Paddle price ids differ sandbox/live |
| Metering source | cumulative `usage_rollup` deltas, per-ticket ledger | Per-ticket ledger | Deltas undercount on restart and cannot attribute key source or pack |
| Fair-use ceiling vs pure tickets | pure ticket count, pure USD cap, both | Both, whichever first | Pure tickets exposes us to Opus-routed tenants; pure USD breaks the promise |

---

## Verify

```bash
# console
cd ../pilot-console && go test ./internal/allowance/... ./internal/fleet/... ./internal/billing/... -count=1
# pilot
make test && go test ./internal/memory/... ./internal/executor/... -run 'KeySource|ReportedCost' -count=1
# live sandbox (after Phase 3): simulate transaction.completed via paddle-sandbox MCP, then
consolectl billing show-allowance --org <id>   # expect pack balance credited once after replay
```

---

## Done

- [ ] `ticket_ledger` rows exist for a platform-key run on the founder box with `key_source=platform`.
- [ ] A sandbox pack purchase credits 25 tickets once, survives webhook replay, shows in the UI.
- [ ] An org at 100 tickets with pack balance keeps running on the platform key; at zero balance it falls back to its own key.
- [ ] Site copy, console copy and Paddle description match verbatim.
- [ ] `PRICING-MODEL.md` and `UNIT-ECONOMICS.md` updated; the research report is linked from both.

---

## Refs

- Research: `research/ai-coding-agent-products-price-extra/report.md` (§ Q4 arithmetic, § Q3 burst protection, § At a glance)
- `product/PRICING-MODEL.md`, `product/UNIT-ECONOMICS.md`
- TASK-502 (allowance row, platform key pool, enforcement), TASK-497 (Paddle, `billing_events`, price immutability § L7)
- Console: `internal/allowance/status.go`, `internal/fleet/allowanceenforce.go`, `internal/fleet/usage_meter.go`, `internal/billing/{service,webhook,handlers}.go`, migrations 0019/0020/0021/0025
- Pilot: `internal/memory/store.go` (`executions`), `internal/executor/runner.go` (`estimateCostWithCache`), `internal/gateway/prometheus.go` (`pilot_window_cost_per_delivered_usd`)

---

**Last Updated**: 2026-10-14
