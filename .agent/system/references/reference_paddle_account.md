---
name: reference_paddle_account
description: Paddle account facts for Pilot Cloud billing — live vendor account (Quantflow), catalog ids created 2026-09-12 (product pro_01m2az5r1fm3pm71f3hpdyh704, price pri_01m2azamdhj869n9vg6hc1wy85, USD 500/month, SaaS tax category), what is still unset (sandbox account, keys, default payment link, destination, verification).
type: reference
---

# Paddle account — Pilot Cloud

**Live vendor account**: `vendors.paddle.com`, account name "Quantflow", founder login. Onboarding at 0/3 (migrate · verify · go live). **Sandbox account: NOT created yet** (separate signup at sandbox-vendors.paddle.com; ids will differ from live and must be recreated there for testing).

## Live catalog (created 2026-09-12 via dashboard, founder decision: one plan, no trial, monthly only)

| Entity | ID | Details |
|---|---|---|
| Product | `pro_01m2az5r1fm3pm71f3hpdyh704` | "Pilot Cloud — Design Partner", tax category **SaaS**, status Active |
| Price | `pri_01m2azamdhj869n9vg6hc1wy85` | "Design Partner — monthly", internal `design-partner-monthly-usd`, **USD 500.00 / month**, recurring, tax mode account default, no trial, quantity 1–1, no country overrides, Active |

Currency: Paddle converts automatically from the USD base by buyer location ("Based on the buyer's location, we convert currency automatically"), so EUR buyers (Montenegro, euro area) pay a converted EUR amount without a price override. Add explicit country prices later only if the converted amounts need rounding.

Decisions NOT taken (founder, 2026-09-12): no 7-day trial, no annual price, no GBP/EUR/AUD overrides, no Solo/Team tiers from `product/PRICING.md` (those predate hosted COGS: ~$52/mo per tenant → no tier below ~$99 until scale-to-zero).

## Still unset in live
- Default payment link (Checkout → Checkout settings): needs the console's real domain; live checkout requires an approved domain.
- Client-side token + API key (Developer tools → Authentication): founder creates; API key never enters chat.
- Notification destination (Developer tools → Notifications): needs the public webhook URL.
- Website approval, business/identity verification, payouts: founder.
- Live prices are immutable once used: a price change = new price + code pointing at it.

## Related
- `tasks/TASK-497-paddle-billing-integration.md` § Founder inputs
- `system/references/reference_paddle_claude_code_plugin.md`
