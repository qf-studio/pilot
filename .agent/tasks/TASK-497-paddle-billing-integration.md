# TASK-497: Paddle Billing integration for pilot-console — vendor swap behind the `billing_status` contract

**Status**: 🚀 In flight — L1, L2, L3 and follow-ups merged + reviewed 2026-09-12; #303 PR#304 reviewed, awaiting merge; L4/L5/L6 next (no Paddle credentials needed for unit-tested legs); sandbox smoke and flag-on gated on founder inputs (§ Founder inputs)
**Created**: 2026-09-12
**Assignee**: Navigator (design) → Pilot (legs) · founder (Paddle account)
**Parent**: [TASK-405](TASK-405-pilot-saas-platform.md) S5 billing lifecycle · [TASK-496](TASK-496-console-resume-s5.md) § billing

---

## Context

**Problem**: Pilot Cloud has no working payment path. The console carries flag-gated Stripe Checkout scaffolding (console #60/#71/#72/#73) that can never be turned on: Stripe is unavailable for the founder's jurisdiction (memory `no-stripe-local-first-s3-testing`). Founder decision 2026-09-10: **Paddle Billing, merchant of record** (memory `payment-processor-is-paddle-not-stripe`).

**Goal**: replace the vendor adapter behind the console's existing billing contract with Paddle: checkout → subscription → webhook-driven `billing_status` → fleet enforcement on `past_due` → customer portal for self-serve cancel/update card. Flat $500/mo design-partner plan. Keep the DB/API/UI contract; swap the provider.

---

## Research (2026-09-12)

Digests with verbatim quotes + URLs live in `.agent/research/paddle-billing-docs/` (01 API + Go SDK · 02 checkout · 03 webhooks · 04 lifecycle/portal/MoR/go-live/pricing · 05 console scaffolding map). Facts that shape the design:

1. **Separate sandbox and live accounts**, separate API keys (`pdl_sdbx_apikey_…` / `pdl_live_apikey_…`) and client-side tokens (`test_…` / `live_…`); catalog and notification settings must be recreated in live; sandbox base URL `https://sandbox-api.paddle.com`.
2. **API keys expire** (default 90 days, max 1 year); expired keys are terminal. Permission-scoped keys; Paddle scans public GitHub for leaks.
3. **Go SDK** `github.com/PaddleHQ/paddle-go-sdk/v5` v5.2.0 (latest tag per module proxy; no v6): `paddle.New` / `paddle.NewSandbox`, typed `pkg/paddlenotification` payloads, `NewWebhookVerifier` (multi-`h1` rotation support unverified → own verifier, § D2).
4. **A default payment link must exist before any transaction can be created** (error `transaction_default_checkout_url_not_set`); it must be a page on our domain that loads Paddle.js. Sandbox auto-approves domains and allows `localhost`; live requires domain verification (Terms, Refund Policy, Privacy Policy reachable, HTTPS).
5. **Server-side transaction** (`items[{price_id, quantity}]`, `customer_id`, `custom_data`) → `checkout.url` = default payment link + `_ptxn=txn_…`. Passing `transactionId` to `Paddle.Checkout.open` locks customer and items. **`custom_data` on the transaction is copied to the created subscription** → `org_id` travels to every `subscription.*` event.
6. **Paddle-Signature** `ts=<unix>;h1=<hex>[;h1=<hex>…]`, HMAC-SHA256 over `ts + ":" + rawBody`, per-destination secret `pdl_ntfset_…`, more than one `h1` during secret rotation, docs' default tolerance 5 s.
7. **Envelope** `event_id` / `notification_id` / `event_type` / `occurred_at` / `data`. Dedupe on `event_id` ("store it when you first process an event, and skip any event with an ID you've already seen"). **Ordering is not guaranteed**: "use occurred_at rather than arrival time".
8. **Respond 200 within 5 s**; live retries 60× over 3 days; replay API `POST /notifications/{id}/replay`; ≤ 10 active destinations.
9. **Statuses** trialing · active · past_due · paused · canceled. Failed renewal → `transaction.payment_failed` + `transaction.past_due` + `subscription.past_due`; recovery → `subscription.updated` with `status=active` (`subscription.activated` is trial→active only). Default dunning without Retain: 7 retries over 30 days, then cancel (or pause; configurable). Canceled subscriptions cannot be reinstated.
10. **Customer portal**: `POST /customers/{id}/portal-sessions` with `subscription_ids[]` → `urls.general.overview` + per-subscription `cancel_subscription` / `update_subscription_payment_method`; sessions are temporary, never cache, never iframe. Pause is not in the portal.
11. **Merchant of record**: Paddle is the seller; tax, invoices, refunds, chargebacks, PCI out of our scope. Fee **5% + $0.50 per transaction** ($25.50 on a $500 charge); payouts monthly (created 1st, paid by 15th), $100 minimum, USD/EUR/GBP/AUD/CAD. Developer tooling = allowed "B2B SaaS".
12. **Console today** (digest 05): `billing_status ∈ {none, active, inactive, past_due}` on `organizations` + `stripe_customer_id` / `stripe_subscription_id`; routes `POST /api/v1/billing/checkout-session` → `{url}`, `GET /api/v1/billing/status`, raw `POST /api/v1/billing/webhook`; UI only consumes `{url}` via a full-page redirect on a 402 from provision, and renders a 3-way chip. **No billing→fleet hook exists**; the B7 conditional-write idiom `SetDesiredStateIfRunning` is the seam. No CSP header; no third-party script in the UI yet. The UI is **Vue 3**, not React.
13. **Paddle's own Claude Code plugin** (`paddle@claude-community`, installed 2026-09-12, see § Tooling) corroborates the design: dedupe ledger keyed on `event_id`, handlers written UPSERT-shaped and convergent because "`subscription.updated` can arrive before the corresponding `subscription.created`", any non-2xx on a verification failure is retried on the same budget (only a 2xx loses events), portal URLs are "one-time use and time-limited", a 3-day outage loses events so "query the API for current state" on recovery, and `past_due` access is "Usually yes — a few days of grace". Sandbox only emails the registered domain; failure card `4000 0000 0000 0002`.

---

## Tooling — Paddle Claude Code plugin (laptop, user scope)

- Installed 2026-09-12 via `claude plugin marketplace add anthropics/claude-plugins-community` + `claude plugin install paddle@claude-community`. Reference: `system/references/reference_paddle_claude_code_plugin.md`.
- Ships 10 skills (Next.js/Node-centric: `webhooks`, `checkout-web`, `subscription-sync`, `sandbox-testing`, `customer-portal`, `catalog-setup`, …) and 3 MCP servers: `paddle-docs` (answers doc questions; replaces ad-hoc web digests from now on), `paddle-sandbox` (needs the sandbox API key via `/plugin configure paddle@claude-community`), `paddle-live` (OAuth in browser).
- **Use in Navigator sessions**: create the sandbox product/price and the notification destination through `paddle-sandbox` (`notificationSettings.create`), read notification logs (`notifications.logs.list`), run simulations. This removes the dashboard clicking from the founder checklist.
- **Paddle's onboarding page** (`vendors.paddle.com/onboarding/get-started`) ships agent prompts per step (catalog · migrate · verify · go live). Catalog was done by hand 09-12; the migrate prompt is adapted as L7; verify and go-live prompts get adapted when L7 starts (their go-live test uses a 100% discount for a real zero-cost checkout, which fits us).
- **Not available to Pilot** on the box: issue bodies stay self-contained (SDK facts from digest 01, Go, not the plugin's TypeScript samples).

---

## Known Pitfalls & Patterns

- **PITFALL** `issue-body-literal-migration-version-collides-with-sibling-leg` → issue bodies say "a new migration", never a number. Reflected: L1 owns the only schema change; L2 adds its table in L1's migration or its own, never a named version.
- **PITFALL** `terminal-status-without-rearm-probe-kills-operator-recovery` → `past_due` enforcement must have a symmetric recovery path and an operator override. Reflected: L5 resume-on-active + `consolectl` override + flag default off.
- **LEARNING** mutation-check the wiring (console PR#292 → #293 → PR#294 → #295: three PRs passed unit tests with unpinned wiring) → every leg's acceptance lists the mutation that must fail.
- **PITFALL** `unwired-config-field-validated-but-dead` (GH-4784) → `main_test.go` already pins `registerBilling`; L1 keeps and extends that pin for the new env vars.
- **DECISION** `no-stripe-local-first-s3-testing` → local-first: Paddle cannot reach the local stack, so tests use signed fixtures (own signer) and the sandbox smoke uses Paddle simulations against a tunnel (L6).
- **PATTERN** console review cadence: every merged PR gets a verdict the same hour; legs sized for one-PR review.

---

## Contract kept vs changed

| Surface | Today | After | Change |
|---|---|---|---|
| `organizations.billing_status` | none/active/inactive/past_due | same four values (`paused` → inactive) | none |
| `organizations.stripe_customer_id` / `stripe_subscription_id` | Stripe ids | `billing_customer_id` / `billing_subscription_id` (Paddle `ctm_…` / `sub_…`) | rename (new migration) |
| `GET /api/v1/me` → `billing_status` | yes | yes | none |
| `GET /api/v1/billing/status` → `{billing_status}` | yes | yes | none |
| `POST /api/v1/billing/checkout-session` → `{url}` | Stripe session URL | Paddle `checkout.url` (our `/billing/checkout?_ptxn=…`) | provider only |
| `POST /api/v1/billing/webhook` | `Stripe-Signature` | `Paddle-Signature` + idempotency + ordering | provider + hardening |
| `GET /api/v1/billing/config` | — | `{provider:"paddle", environment, client_token, past_due_grace_hours}` | new (public-safe values) |
| `POST /api/v1/billing/portal-session` | — | `{url, cancel_url, update_payment_method_url}` | new |
| UI 402 → redirect to `{url}` | yes | yes | none |
| UI `/billing/checkout` route | — | loads Paddle.js, opens overlay for `_ptxn` | new |
| UI settings-billing | chip only | chip (`past_due` label → "payment failed" + grace notice) + Subscribe / Manage / Update card + post-checkout polling | label + new actions |
| Env `PILOT_CONSOLE_BILLING_*` | STRIPE_API_KEY, STRIPE_WEBHOOK_SECRET, CANCEL_URL | PADDLE_ENVIRONMENT, PADDLE_API_KEY, PADDLE_WEBHOOK_SECRET, PADDLE_CLIENT_TOKEN, ENFORCE_ENABLED, WEBHOOK_TOLERANCE | renamed/added; CHECKOUT_ENABLED, PRICE_ID, SUCCESS_URL kept |
| `go.mod` | stripe-go v81 | paddle-go-sdk v5 | swap |

---

## Design

### D1 — Checkout
1. UI hits a 402 on provision (unchanged) or clicks **Subscribe** on settings-billing → `POST /api/v1/billing/checkout-session`.
2. Server: org from session → ensure a Paddle customer (`billing_customer_id` empty → `POST /customers` with the org owner's email, store id) → `POST /transactions` `{items:[{price_id: cfg.PriceID, quantity:1}], customer_id, custom_data:{org_id}}` → respond `{url: txn.checkout.url}`. 502 on Paddle errors, never leak the API error body.
3. UI full-page redirects to `url` = `<SUCCESS_URL origin>/billing/checkout?_ptxn=txn_…` (the default payment link configured in Paddle). That route calls `GET /api/v1/billing/config`, `initializePaddle({environment, token})`, then `Paddle.Checkout.open({transactionId, settings:{displayMode:"overlay", successUrl: "<origin>/settings/billing?checkout=success", allowLogout:false}})`.
4. After payment, Paddle redirects to `successUrl`; settings-billing polls `GET /api/v1/billing/status` every 2 s for up to 60 s until `active` (the webhook is the source of truth; the client `checkout.completed` event is never trusted for state).

### D2 — Webhook processing (`POST /api/v1/billing/webhook`)
0. **Source-IP allowlist** (flag `PILOT_CONSOLE_BILLING_WEBHOOK_IP_ALLOWLIST`, default off): Paddle publishes its webhook egress IPs at `GET https://api.paddle.com/ips` (live) and `GET https://sandbox-api.paddle.com/ips` (sandbox), unauthenticated, shape `{"data":{"ipv4_cidrs":["34.237.3.244/32", …]}}` (verified 2026-09-12; six /32s each). The console fetches the list for its configured environment at start and hourly, keeps the last good list on fetch failure, and when the flag is on rejects (403) requests whose client IP is outside it. Client IP comes from the deployment's trusted-proxy chain (same approach as auth-service's `TRUSTED_PROXY_CIDRS`); never from an unvalidated `X-Forwarded-For`. Never hard-code the list. Flag stays off until proven in staging behind the real load balancer.
1. Read body (1 MiB cap, existing). Parse `Paddle-Signature`: `ts` + every `h1`. Reject (400) if `|now − ts| > tolerance` (default 5 min, env-overridable) or no `h1` equals `hex(HMAC-SHA256(secret, ts + ":" + body))` under constant-time compare. Own ~40-line verifier with published vectors; the SDK verifier's multi-`h1` handling is unverified and a rotation must not break ingestion.
2. Decode envelope with `paddlenotification` types. In one DB transaction: `INSERT INTO billing_events(event_id, event_type, occurred_at, org_id) … ON CONFLICT (event_id) DO NOTHING`; zero rows → duplicate → 200 immediately.
3. Resolve org: `data.custom_data.org_id` first; fallback `billing_customer_id = data.customer_id`. Unknown → Warn + 200 (existing policy; a 5xx would retry-storm for 3 days).
4. Apply only when `occurred_at >= organizations.billing_status_event_at` (new column, null-safe); older events are recorded but do not overwrite state. Update `billing_status`, `billing_subscription_id`, `billing_customer_id`, `billing_status_event_at` in the same tx. Commit → 200. No outbound calls inside the handler (5 s budget).
5. Enforcement side effects (D4) are triggered from the committed transition, not from the raw event.

### D3 — State machine (event → `billing_status`)

| Event | `data.status` | New status | Notes |
|---|---|---|---|
| subscription.created | trialing/active | active | binds `billing_subscription_id`, `billing_customer_id` |
| subscription.activated / resumed / trialing | — | active | |
| subscription.updated | active/trialing | active | dunning recovery arrives here |
| subscription.updated | past_due | past_due | |
| subscription.updated | paused/canceled | inactive | |
| subscription.past_due | — | past_due | |
| subscription.paused | — | inactive | |
| subscription.canceled | — | inactive | scheduled cancel fires `updated` first, `canceled` at effect |
| transaction.completed | — | unchanged | audit + id binding only |
| transaction.payment_failed | — | unchanged | Warn log; `subscription.past_due` carries state |
| anything else | — | unchanged | 200 ack |

Destination subscribes to: all `subscription.*` + `transaction.completed` + `transaction.payment_failed`.

### D4 — Enforcement (flag `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED`, default off) — sweep, not inline
- The webhook path only writes `billing_status` + `billing_status_event_at`. A **billing enforcement sweep** on the existing fleet reconciler tick (same shape as the B7 idle sweep) reads orgs and acts; this keeps the webhook inside its 5 s budget, retries naturally on the next tick if a write fails, and is the operator-recovery path (pitfall `terminal-status-without-rearm-probe`).
- **past_due**: grace first. Paddle retries the payment for up to 30 days and its own guidance is "a few days of grace". Instances are suspended only when `now − billing_status_event_at ≥ PILOT_CONSOLE_BILLING_PAST_DUE_GRACE` (default 72 h). Until then the UI shows "payment failed, update your card" (L3) and a Warn log fires once per org.
- **inactive** (canceled/paused): suspend on the next tick, no grace.
- Suspend = for each org instance with `desired_state = running`, conditional write `running → suspended` in the B7 idiom (`SetDesiredStateIfRunning` shape, new event type `billing_suspended`, detail = event_id). `suspended` (not `stopped`) so the B7 wake path never revives a non-paying tenant.
- **active** again: for each instance with `desired_state = suspended` whose latest journal event is `billing_suspended`, conditional write `suspended → running` (`billing_resumed`). Instances suspended for any other reason stay put.
- Flag off: the sweep logs would-be actions at Warn with instance ids (dry run). Operator tools: `consolectl billing set-status <org> <status>` (writes status only, journals `operator_override`) and `consolectl billing resync <org>` (fetches the subscription from the Paddle API and applies its status through the same apply path — the recovery for events lost past Paddle's 3-day retry window).
- Prerequisite check inside the leg: confirm the reconciler treats `suspended` as "stop and keep"; if it does not, the leg adds that handling before enabling anything.
- Cancel → grace → snapshot-and-terminate (roadmap item 10) is **not** this task.

### D5 — Portal
`POST /api/v1/billing/portal-session` (session + CSRF): 409 if no `billing_customer_id`; SDK `CreateCustomerPortalSession(customer_id, subscription_ids=[billing_subscription_id])` → `{url: general.overview, cancel_url, update_payment_method_url}`; `Cache-Control: no-store`; 502 on Paddle error. Portal URLs are **one-time use and time-limited**: mint a fresh session per click, never cache, never reuse. UI opens `url` in a new tab (never iframe).

### D6 — Config / env

| Env | Required when | Notes |
|---|---|---|
| `PILOT_CONSOLE_BILLING_CHECKOUT_ENABLED` | — | kept; off → no routes registered |
| `PILOT_CONSOLE_BILLING_PADDLE_ENVIRONMENT` | flag on | `sandbox` \| `live`; selects `NewSandbox` / `New`; must match key prefix (`pdl_sdbx_` / `pdl_live_`) or `Load` fails; `GET /api/v1/billing/config` maps `live` → `production` (Paddle.js vocabulary) |
| `PILOT_CONSOLE_BILLING_PADDLE_API_KEY` | flag on | process env from SSM SecureString; 90-day expiry → rotation runbook line |
| `PILOT_CONSOLE_BILLING_PADDLE_WEBHOOK_SECRET` | flag on | `pdl_ntfset_…`; accepts a comma-separated pair during rotation |
| `PILOT_CONSOLE_BILLING_PADDLE_CLIENT_TOKEN` | flag on | public-safe; served by `GET /api/v1/billing/config`; must match environment (`test_` / `live_`) |
| `PILOT_CONSOLE_BILLING_PRICE_ID` | flag on | kept; `pri_…` |
| `PILOT_CONSOLE_BILLING_SUCCESS_URL` | flag on | kept; `https://<console>/settings/billing?checkout=success` |
| `PILOT_CONSOLE_BILLING_WEBHOOK_TOLERANCE` | optional | default `5m` |
| `PILOT_CONSOLE_BILLING_PAST_DUE_GRACE` | optional | default `72h`; served as `past_due_grace_hours` on `GET /api/v1/billing/config` (L5) |
| `PILOT_CONSOLE_BILLING_WEBHOOK_IP_ALLOWLIST` | optional | default `false`; enforce Paddle's published egress IPs from `GET /ips` (D2 step 0) |
| `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` | optional | default `false` (L5) |
| ~~`…_STRIPE_API_KEY`, `…_STRIPE_WEBHOOK_SECRET`, `…_CANCEL_URL`~~ | — | removed; `Load` fails loudly if any is still set, so a stale task definition cannot half-configure |

### D7 — Data model (one new migration, L1)
- `organizations`: rename `stripe_customer_id → billing_customer_id`, `stripe_subscription_id → billing_subscription_id`; add `billing_status_event_at timestamptz null`; partial unique index on `billing_customer_id` where not null.
- `billing_events(event_id text primary key, event_type text not null, occurred_at timestamptz not null, org_id uuid null references organizations(id) on delete set null — `uuid`, not text: `organizations.id` is uuid, Pilot caught this in L1, received_at timestamptz not null default now())` + index on `(org_id, occurred_at desc)`.
- Down migration reverses both. No data backfill (Stripe path never ran in production; flag has been off).

---

## Legs (issues) — dispatch order L1 → L2 → (L3 ∥ L4) → L5 → L6 → L7 (go-live, Navigator + founder, not a Pilot issue)

Issue-body rules at dispatch: H2 headers `## Problem / ## Fix / ## Acceptance`, inline `Depends on: #N`, backticked paths only if present on the target repo's `origin/main` (run the path check), no migration version literals, `no-decompose` label, mutation command in acceptance.

### L1 — console: Paddle provider core — ✅ [console#297](https://github.com/qf-studio/pilot-console/issues/297) → **PR#298 merged 15:51Z, post-merge review APPROVE-w-defects** (3/3 mutation pins hold; migration correctly uses `uuid` for `billing_events.org_id`; defects → [console#299](https://github.com/qf-studio/pilot-console/issues/299) → **PR#301 merged 16:15Z, reviewed APPROVE-w-notes**: conflict recovery via email lookup, `SuccessURL` removed)
- **Scope**: `internal/config/config.go` billing block per D6 (+ environment/key-prefix cross-check, stale-Stripe-var refusal); `internal/billing/service.go` — `OrgStore` gains `UpdateBillingStatusByCustomer` → vendor-neutral names, new `PaddleClient` interface (create customer, create transaction, create portal session) implemented over paddle-go-sdk v5, faked in tests; `internal/billing/handlers.go` checkout handler per D1; `GET /api/v1/billing/config`; new migration per D7; `internal/orgs/store.go` methods renamed; `main.go` `registerBilling` + adapter; `go.mod` swap; `README.md` config section; handler DTOs become structs with json tags so `scripts/check-wire-contract-tests.sh` covers them.
- **Acceptance**: flag off → zero billing routes (existing test kept) · flag on with sandbox env + live-prefixed key → `Load` error · checkout-session creates customer once then reuses id (fake client asserts call counts) · transaction request carries `custom_data.org_id` and `price_id` · `{url}` is the fake's `checkout.url` verbatim · `go.mod` has no stripe module · wire-contract gate now fires on the billing handler DTOs · **mutation**: deleting the `custom_data` field from the transaction request fails a test; removing the `registerBilling` call fails `main_test.go`.

### L2 — console: Paddle webhook — signature, idempotency, ordering, state machine — ✅ [console#300](https://github.com/qf-studio/pilot-console/issues/300) → **PR#302 merged 17:04Z, reviewed pre-merge: APPROVE-w-notes** (verifier vectors, ledger insert-first, SQL `occurred_at` guard, state table verbatim, `/ips` allowlist; handler-level mutations c/d/e fail named tests, store-level a/b covered by fail-not-skip DB tests in CI; notes: allowlist is `RemoteAddr`-only so it must stay OFF behind the ALB, stale events unlogged) → both notes filed as [console#303](https://github.com/qf-studio/pilot-console/issues/303) → **PR#304 reviewed pre-merge: APPROVE-w-notes** (resolver table 7 cases, config validation, README caveat, stale Info line; mutations a/b/c fail named tests). Paste gate ignored a sixth time → executor gap filed as [pilot#5435](https://github.com/qf-studio/pilot/issues/5435) → PR#5436 **REQUEST-CHANGES** (issue-text commands run with the daemon's full env and pasted unredacted; mutation file path escapes the worktree; no timeout; cue list ignores most real phrasings) → converted to draft, revision [pilot#5437](https://github.com/qf-studio/pilot/issues/5437).
- **Scope**: `internal/billing/` signature verifier (own, vectors, multi-`h1`, tolerance, constant-time), `billing_events` insert-first idempotency, `occurred_at` ordering guard, D3 table, org resolution by `custom_data.org_id` then customer id, `Cache-Control: no-store`, structured logs with `event_id`/`notification_id`; source-IP allowlist per D2 step 0 (fetcher with hourly refresh + last-good cache, fail-closed only when the flag is on and no list was ever loaded, client-IP resolution through the trusted-proxy chain); `internal/orgs/store.go` one method that applies a status transition atomically with the event insert and the `billing_status_event_at` guard; tests: vectors (generated in-test with `crypto/hmac` from a fixed secret + a tampered-body negative + expired-ts negative + rotated second-`h1` positive), duplicate `event_id` → single apply, older `occurred_at` after newer → ignored, every D3 row table-driven, unknown org → 200 + Warn, 413 cap, allowlist: flag off → no check, flag on + IP outside → 403, fetch failure keeps last list, replayed fixture files under `internal/billing/testdata/`.
- **Acceptance**: all above green · handler does no outbound HTTP (fake client asserts zero calls) · **mutation**: removing the `ON CONFLICT DO NOTHING` path or the `occurred_at` guard each fails a named test; swapping `hmac.Equal` for `==` fails the vector test that checks constant-time compare is used (or a lint guard).
- Depends on L1 (columns + DTOs).

### L3 — ui: checkout route + settings-billing actions — ✅ ui#156 → **PR#157 merged 15:40Z, reviewed pre-merge: APPROVE-w-defects** (contract-faithful; 3/3 mutation pins hold, run by the reviewer since the PR body omitted them; defect: portal `window.open` after an await → popup blockers → [ui#158](https://github.com/qf-studio/pilot-console-ui/issues/158) filed, `pilot`). Follow-up ui#158 → **PR#159 REQUEST-CHANGES** (the placeholder tab is opened with `noopener`, so `window.open` returns null and the fix can never redirect it) → PR#159 was merged anyway; revision [ui#160](https://github.com/qf-studio/pilot-console-ui/issues/160) → **PR#161 merged 16:20Z, reviewed APPROVE** (no `noopener`, opener severed, real-helper test).
- **Scope** (Vue 3): add `@paddle/paddle-js` dependency (package.json change → needs approval per project rules; call it out in the issue); route `/billing/checkout` reading `_ptxn`, fetching `GET /api/v1/billing/config`, `initializePaddle`, `Paddle.Checkout.open({transactionId, settings})`, error state if `_ptxn` missing or Paddle.js fails to load; `src/views/SettingsBillingView.vue` — Subscribe (status ∉ {active, past_due}) → checkout-session → `navigateTo(url)`; Manage subscription (active/past_due) → portal-session → open in new tab; Update payment method (past_due) → `update_payment_method_url`; `?checkout=success` → poll status 2 s × 30 → chip flips or "payment received, activation pending — refresh in a minute"; `src/lib/api/httpAdapter.ts` + `mockAdapter.ts` + `types.ts` gain `getBillingConfig`, `createPortalSession`; chip states unchanged, `past_due` label becomes "payment failed — update your card within N days" using `past_due_grace_hours`; portal URLs are fetched per click, never stored.
- **Acceptance**: unit tests for the route (missing `_ptxn`, load failure, open called with the transaction id and `allowLogout:false`), polling stops on `active` and on timeout, mock adapter parity, `npm run build` green · **mutation**: removing `transactionId` from the open call fails a test.
- Depends on L1 (config endpoint) · parallel with L4 · L4's endpoint may be stubbed in mock adapter first.

### L4 — console: customer portal session endpoint
- **Scope**: `POST /api/v1/billing/portal-session` per D5 in `internal/billing/handlers.go`; fake client test; 409/502 paths; `no-store`.
- **Acceptance**: session + CSRF required · request carries the org's subscription id · response shape pinned by a wire-contract test · **mutation**: dropping `subscription_ids` from the SDK request fails a test.
- Depends on L1.

### L5 — console: billing enforcement sweep (past_due grace → suspend, active → resume, resync)
- **Scope**: per D4 — sweep on the reconciler tick in the B7 idle-sweep shape; `internal/fleet/store.go` conditional `running→suspended` / `suspended→running` writers with journal event types; `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` + `PILOT_CONSOLE_BILLING_PAST_DUE_GRACE`; dry-run logging; `consolectl billing set-status` and `consolectl billing resync` (Paddle API read → same apply path as the webhook); reconciler `suspended` handling verified or added; Warn-level log on every suspend with org + instance ids (alert routing if the console has an alert sink); `past_due_grace_hours` on the config endpoint, plus `past_due_since` (ISO time of the transition) so the UI can show time remaining instead of the configured window (PR#157 review note).
- **Acceptance**: table test: (billing status × age vs grace × instance desired_state × last journal event) → resulting desired_state; flag off → no writes, one Warn per would-be write; instance suspended by an operator (journal ≠ `billing_suspended`) is not resumed by a billing recovery; `resync` on an org whose webhooks were lost lands on the API's status · **mutation**: replacing the conditional `WHERE desired_state = 'running'` with an unconditional update fails a test; removing the sweep registration from the tick fails a wiring test (source-level guard in the `check-wire-contract-tests` idiom, given console #295's lesson); removing the grace comparison fails a test.
- Depends on L2.

### L7 — go-live migration + pre-verification readiness (adapted from Paddle's onboarding step 01 prompt, 2026-09-12)
Paddle's dashboard supplies three agent prompts (migrate sandbox → live · verify · test and go live). Their catalog step was done by hand on 09-12; their migrate prompt is Next.js/Retain-flavoured, so this is the Pilot version. Runs **after L6's sandbox smoke passes**, executed by a Navigator session with the `paddle-live` MCP plus the founder for dashboard-only steps. **Guardrails inherited verbatim in spirit**: live is additive and code-side only; never delete, archive or recreate a live product, price, discount, customer, subscription or notification destination (recreating a destination rotates its secret and silently breaks every future delivery); prices are immutable once used, a wrong price means a new price and code pointing at it; the sandbox is the source of truth and is never modified; when a fix would need deleting or recreating a live entity, stop and ask.
1. **Audit first** (report done vs outstanding, redo nothing correct): live catalog vs sandbox catalog (as of 09-12 live already has `pro_01m2az5r1fm3pm71f3hpdyh704` / `pri_01m2azamdhj869n9vg6hc1wy85`; record the sandbox → live id mapping once the sandbox catalog exists) · live credentials (a `live_` client token, a `pdl_live_apikey_` key, a live notification destination with its secret) · code/config pointing at live (`PILOT_CONSOLE_BILLING_PADDLE_ENVIRONMENT=live`, live `PRICE_ID`, SSM parameters populated). Pilot has no Retain, so no `pwCustomer`.
2. **Recreate what is missing in live, additively**: catalog equivalents via `paddle-live` (skip test items) · client token via MCP if supported, API key by the founder in the dashboard, both into SSM, never in code · ONE notification destination for the live webhook URL, secret captured once into SSM (it cannot be read again).
3. **Point the deployment at live**: only env changes (D6), no code edits, because the Go client selects base URL from the environment and the UI receives `production` from `GET /api/v1/billing/config`. Verify no `sandbox` literal survives in the live task definition.
4. **Finish the live account** (dashboard, founder): payment methods under Checkout → Checkout settings · **default payment link = the console's real origin + `/billing/checkout`** (not localhost; live checkouts fail otherwise) · submit the console domain under Checkout → Request domain approval, now, so approval runs during verification · payouts under Business account → Payouts. Webhook IP allowlist: D2 step 0, driven by the `GET /ips` endpoint, not a pasted list. **Do not enable `PILOT_CONSOLE_BILLING_WEBHOOK_IP_ALLOWLIST` behind the ECS load balancer**: the console resolves the client IP from `RemoteAddr` only (no trusted-proxy chain exists, PR#302), so every webhook would arrive as the balancer's address and be rejected. Enabling it needs a trusted-proxy resolver first (console#303 adds `PILOT_CONSOLE_TRUSTED_PROXY_CIDRS`).
5. **Pre-verification readiness audit** (what Paddle's reviewers check; confirm current requirements against their verification guidance): public Terms & Conditions, Privacy Policy, Refund/Cancellation Policy with real URLs on the marketing site (flag 404s or generic redirects) · a clear product description · contact reachable from the homepage in ≤ 2 clicks · on-site pricing equals the live catalog (USD 500/month) · every checkout domain serves the real product. List each gap with a concrete fix.
- **Acceptance**: audit table posted in the task doc · sandbox → live id mapping recorded in `system/references/reference_paddle_account.md` · live env verified by a read-only status call through the Go client · no live entity deleted or recreated (diff of live catalog + destinations before/after) · readiness gaps listed with owners.
- Depends on L6 + founder items (domain, policy pages, verification started).

### L6 — sandbox smoke SOP + fixtures + docs
- **Scope**: `.agent/sops/billing/paddle-sandbox-smoke.md` in this repo (sandbox catalog + default payment link already exist, see `system/references/reference_paddle_account.md`; the link is `https://localhost:5173/...`, so run the UI dev server with HTTPS (Vite basic-ssl) or point the link at a tunnel; notification destination → tunnel URL, run checkout with test card `4242 4242 4242 4242`, confirm `active`, simulate `subscription.past_due` via Paddle simulations, confirm `past_due` + grace notice + dry-run suspend log after the grace elapses (set grace to `1m` for the smoke), simulate recovery via `subscription.updated` status active, simulator-cancel the subscription and confirm `updated` (scheduled_change) then `canceled` → inactive, failure card `4000 0000 0000 0002` for the checkout error path, notification logs read via the `paddle-sandbox` MCP; sandbox only emails the registered domain); console `README.md` billing section (env table from D6, key-expiry rotation, secret-rotation dual-secret window); captured sandbox payloads added as fixtures for L2's replay tests; docs-site page if the customer-facing docs describe billing.
- Depends on L1–L5 merged and § Founder inputs delivered.

---

## Founder inputs (blocking sandbox smoke + flag-on; NOT blocking L1–L5 code)

1. **Paddle sandbox account** (separate signup at sandbox-vendors.paddle.com). In Developer Tools → Authentication create an **API key** scoped to customers / transactions / subscriptions / notifications / customer portal, expiry set to the max (1 year) with a calendar reminder, and a **client-side token**. Then run `/plugin configure paddle@claude-community` with the sandbox key so the `paddle-sandbox` MCP works in the next Navigator session. Both secrets also go to SSM SecureString → ECS task env; agree the parameter path convention with Nelya (e.g. `/console/<env>/billing/paddle_api_key`) and record it in `system/references/`.
2. **Live catalog DONE 2026-09-12 via dashboard**: product `pro_01m2az5r1fm3pm71f3hpdyh704` + price `pri_01m2azamdhj869n9vg6hc1wy85` (USD 500.00/month, SaaS tax category, no trial, qty 1; see `system/references/reference_paddle_account.md`). **Sandbox DONE 2026-09-12 too**: product `pro_01m2b1xhwtvnmh5atk6zpdagdb`, price `pri_01m2b1zrqvpp1388t77wqystqt`, default payment link `https://localhost:5173/billing/checkout` (https forced → local UI must serve HTTPS or use a tunnel, see L6), client token `pilot-console-ui` created; notification destination (URL = tunnel to the local console, events per D3) → `pdl_ntfset_…` secret handed back for SSM. Default payment link (`http://localhost:5173/billing/checkout` now, the console origin later) is a dashboard setting under Checkout → Checkout settings — one click, founder or screen-share.
3. **Dunning end action**: recommend **cancel** after the default 30-day window (canceled → inactive → new checkout to return; pause has no self-serve resume). Retain not needed for v1. Confirm the 72 h past_due grace default.
4. **Live account prerequisites** (start now, 2–4 business days): business + identity verification; domain review needs the marketing site to link Terms, Refund Policy, Privacy Policy over HTTPS (depends on the domain pick).
5. **Plan copy for CON-5** (settings-billing page: plan name, price, what is included, refund policy pointer since Paddle handles refunds).

---

## Out of Scope

- Usage-based / metered billing (usage rollup #283 stays groundwork; v2).
- Trials, plan changes, proration, multiple plans/currencies.
- Cancel → grace → snapshot-and-terminate (roadmap item 10; separate task after L5).
- Paddle Retain, invoicing UI, tax, refunds (merchant of record scope).
- CSP hardening (Paddle.js is the first third-party script; when a CSP lands it must allow `cdn.paddle.com` scripts and `*.paddle.com` frames — note for that task).
- Migrating any Stripe data (none exists).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Checkout surface | Paddle.js overlay on our route · inline checkout · Paddle-hosted page | Overlay on `/billing/checkout` via `checkout.url` | Default payment link must load Paddle.js anyway; overlay keeps the existing `{url}` redirect contract and no UI branding work; hosted pay.paddle.io is documented for mobile apps |
| Transaction creation | Client-side `items` open · server-side transaction + `transactionId` | Server-side | Locks price and customer, carries `custom_data.org_id` to the subscription, keeps the price id off the client |
| Org resolution in webhooks | customer id only · `custom_data.org_id` only · both | `org_id` first, customer id fallback | `custom_data` propagates transaction → subscription (documented); customer id covers events created outside checkout |
| Idempotency | in-memory LRU · `event_id` table · `notification_id` table | `billing_events(event_id pk)` insert-first in the same tx | Paddle's documented key; survives restarts and multiple replicas; `notification_id` differs per destination/replay |
| Ordering | trust arrival · `occurred_at` guard | `occurred_at >= billing_status_event_at` | Paddle states events arrive out of order |
| Signature verification | SDK `WebhookVerifier` · own | Own (SDK for API + payload types) | Multi-`h1` rotation support in the SDK unverified; 40 lines with vectors is cheaper than a rotation outage |
| Timestamp tolerance | 5 s (Paddle example) · 5 min | 5 min default, env-overridable | Retries and clock skew; replay attacks are already bounded by `event_id` dedupe |
| Status vocabulary | add `paused`/`trialing` · keep four | Keep `none/active/inactive/past_due` | UI and `/me` contract unchanged; paused/trialing have no product meaning for a flat plan |
| Suspend target state | `stopped` (B7 idiom) · `suspended` | `suspended` | B7's wake signal must not revive a non-paying tenant |
| Enforcement rollout | on by default · flag default off with dry-run | Flag off, dry-run logs | Status tracking ships and is observed before any tenant is suspended; re-arm pitfall |
| Enforcement trigger | inline from the webhook transition · sweep on the reconciler tick | Sweep | Webhook stays inside the 5 s budget; failed writes retry next tick; the sweep is the operator-recovery path |
| past_due handling | suspend immediately · grace then suspend · wait for cancel | Grace (72 h default) then suspend | Paddle retries for up to 30 days and recommends grace; immediate cutoff punishes a card hiccup; waiting for cancel gives 30 days free |
| Lost-event recovery | rely on Paddle replay · API resync command | `consolectl billing resync` | Replays are impossible past the 3-day window; a resync reads current truth through the same apply path |
| Env naming | keep `STRIPE_*` names · rename | Rename + refuse stale vars | A stale ECS task definition cannot half-configure the new provider |
| Secrets path | tenant SSM writer · process env from SSM | Process env | Vendor keys are process-level, not per-tenant; matches every other console secret |

---

## Verify

```bash
# console (worktree on origin/main after each leg)
go build ./... && go vet ./... && go test ./internal/billing/ ./internal/orgs/ ./internal/fleet/ ./internal/config/ . && make check-wire-contract-tests && make check-fleet-org-scoping
# ui
npm test && npm run build
# sandbox smoke (L6 SOP): checkout with 4242 4242 4242 4242 → billing_status=active; simulate subscription.past_due → past_due (+ dry-run suspend log); simulate subscription.updated status=active → active
```

---

## Done

- [ ] L1–L5 merged with same-hour review verdicts; follow-ups filed
- [ ] `go.mod` free of stripe-go; no `STRIPE` identifier left in console (`git grep -i stripe` empty except migration history)
- [ ] Sandbox smoke passes end-to-end on the local stack with the founder's sandbox account (L6 SOP executed, evidence in the SOP)
- [ ] Flag-on in production waits for: live account verified, live catalog + destination created, domain picked, enforcement flag still off for the first paying tenant

---

## Refs

- Decision memory `knowledge/memories/decisions/payment-processor-is-paddle-not-stripe.md`; `no-stripe-local-first-s3-testing`
- Research digests `.agent/research/paddle-billing-docs/01…05`
- Console review lineage for the wiring-pin rule: console PR#292 → #293 → PR#294 → #295
- Roadmap: `system/saas-roadmap.md` S5 row; item 10 (termination/retention) for the follow-on task
- Dead-end Stripe scaffolding: console #60 / #71 / #72 / #73
- Pilot issues: L1 console#297 → PR#298 (merged) · L1 follow-up console#299 · L2 console#300 · L3 ui#156 → PR#157 (merged) · L3 follow-up ui#158 → PR#159 (superseded) → ui#160

---

**Last Updated**: 2026-09-12 (rev 3: L7 go-live leg adapted from Paddle's prompt; D2 source-IP allowlist from `GET /ips`; rev 2: Paddle plugin installed; D4 → sweep + grace + resync; founder checklist shrunk)
