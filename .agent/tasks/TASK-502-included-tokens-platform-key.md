# TASK-502: Included token volume — platform Anthropic key per tenant, allowance metering, own-key overage

**Status**: ✅ DECIDED 2026-10-01 → 📋 READY TO DISPATCH (leg 0 + leg 2 first). Founder accepted the recommendation as-is: **100 tickets/month = $100 token allowance at Anthropic list price, hard cap, then the customer's own key**. Founder requirement added the same day: **the number must be easy to change later** — it is a runtime plan setting, never a constant.
**Created**: 2026-09-30 · **Decided**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes per repo · **Parent**: TASK-405 · **Sibling**: TASK-497 (Paddle L7, price object pri_01m3rf7qws3gzscj9fttg4aywe)

## Why
Today the tenant box runs only on the customer's key (BYO enforced at onboarding, `organizations.credentials` requires `ANTHROPIC_API_KEY`). The decided plan bundles tokens, so the box must run on a key we own until the allowance is used, then fall back to the customer's key, and the console must show both meters.

## Decisions (closed 2026-10-01)
| Question | Answer |
|---|---|
| Included volume | **100 tickets/month**, sold as tickets, **metered as $100** at Anthropic list price |
| Cap | **Hard cap** on our key; overage = the tenant's own key (no soft overage, no surprise invoice) |
| Where the number lives | **Console DB plan-allowance row** (`included_tickets`, `included_usd_cents`, `hard_cap`), one row per plan code (`pilot-console`), optional nullable per-org override. Edited via `consolectl billing set-allowance`, read by enforcement and UI. **Not** an env var (would need a Nelya template change + redeploy per edit) and **not** a Go constant. |
| Platform key shape | One Anthropic Admin API workspace + key per tenant (attribution, revocation, per-tenant rate limits). Shared-key accounting rejected: spend not attributable at Anthropic. |

## Legs
0. **Allowance config (console)** — migration adds the plan-allowance table seeded with `pilot-console: 100 tickets / 10000 cents / hard_cap=true`; `consolectl billing set-allowance --plan pilot-console --tickets N --usd N [--org <id>]` + `show-allowance`; `GET /api/v1/billing/allowance` returns the effective allowance for the caller's org (plan row + org override). No enforcement yet. Small, no external dependency, unblocks 2–4.
1. **Key ownership (console + reconciler)** — platform Anthropic key per tenant (Admin API workspace + key per org) provisioned by the reconciler into the tenant's secrets; customer key becomes optional at onboarding; key revoked on org deletion/suspension.
2. **Metering (console)** — extend `usage_rollup` (0019) with per-org month-to-date token dollars at list price, split platform-key vs own-key (needs the box ledger to tag which key served each execution — pilot-repo leg). Period = Paddle subscription billing period from the webhook ledger, not calendar month.
3. **Enforcement (console + reconciler)** — at allowance exhaustion: switch the box to the customer key (config-generation bump) or pause dispatch if none is set; alerts at 80 % and 100 %; reset on the billing period. Reads the leg-0 row, never a literal.
4. **UI (console-ui)** — Plan & Billing shows "included: X of {tickets} tickets / $Y of ${usd} used", overage state, own-key prompt. Numbers come from the leg-0 endpoint so a `consolectl` edit is live without a UI deploy.
5. **Copy + catalog** — plan name "Pilot Console", $499, included volume in the Paddle product description and the console page (Paddle reviewers compare them). Copy references the configured number at render time where it is dynamic; the Paddle description is a manual operator step when the number changes.

Dispatch order: **0 → 2 (console, parallel-safe) → 1 → 3 → 4 → 5**. Leg 1 needs the Anthropic Admin API key in Doppler `prd` first (founder step).

## Refs
- `product/PRICING-MODEL.md` (decision banner, updated 10-01), `product/UNIT-ECONOMICS.md` § "$499 with included tokens" (margin table, 100-ticket row = 64 % at $1.00/ticket, 21 % stress case at $3.15), `product/PRICING.md` (old Team tier, 150 tickets framing)
- TASK-497 § L7 · console schema: `internal/db/migrations/0019_usage_rollup.up.sql`, `0020_paddle_billing.up.sql`; `cmd/consolectl/billing.go` (`set-status`, `resync` — add the allowance verbs beside them)
