---
name: reference_paddle_account
description: Paddle account facts for Pilot Cloud billing — live (Quantflow) and sandbox accounts, catalog ids in both (live pro_01m2az5r1fm3pm71f3hpdyh704 / pri_01m2azamdhj869n9vg6hc1wy85; sandbox pro_01m2b1xhwtvnmh5atk6zpdagdb / pri_01m2b1zrqvpp1388t77wqystqt; USD 500/month, SaaS), sandbox→live mapping, what is still unset.
type: reference
---

# Paddle account — Pilot Cloud

**Live vendor account**: `vendors.paddle.com`, account name "Quantflow", founder login. Onboarding at 0/3 (migrate · verify · go live). **Sandbox account created 2026-09-12** (`sandbox-vendors.paddle.com`, same email; business type Private, Montenegro). Ids differ from live.

## Live catalog (created 2026-09-12 via dashboard, founder decision: one plan, no trial, monthly only)

| Entity | ID | Details |
|---|---|---|
| Product | `pro_01m2az5r1fm3pm71f3hpdyh704` | "Pilot Cloud — Design Partner", tax category **SaaS**, status Active |
| Price | `pri_01m2azamdhj869n9vg6hc1wy85` | "Design Partner — monthly", internal `design-partner-monthly-usd`, **USD 500.00 / month**, recurring, tax mode account default, no trial, quantity 1–1, no country overrides, Active |

Currency: Paddle converts automatically from the USD base by buyer location ("Based on the buyer's location, we convert currency automatically"), so EUR buyers (Montenegro, euro area) pay a converted EUR amount without a price override. Add explicit country prices later only if the converted amounts need rounding.

Decisions NOT taken (founder, 2026-09-12): no 7-day trial, no annual price, no GBP/EUR/AUD overrides, no Solo/Team tiers from `product/PRICING.md` (those predate hosted COGS: ~$52/mo per tenant → no tier below ~$99 until scale-to-zero).

## Sandbox catalog (created 2026-09-12 via dashboard, mirrors live)

| Entity | ID | Details |
|---|---|---|
| Product | `pro_01m2b1xhwtvnmh5atk6zpdagdb` | "Pilot Cloud — Design Partner", tax category SaaS, Active |
| Price | `pri_01m2b1zrqvpp1388t77wqystqt` | "Design Partner — monthly", internal `design-partner-monthly-usd`, USD 500.00 / month, recurring, no trial, quantity 1–1, Active |
| (archived) | `pro_01m2b1xhwfc50f3c344awy8pj9` | duplicate from a double submit, archived immediately, no prices |

Sandbox → live mapping: `pro_01m2b1xhwtvnmh5atk6zpdagdb` → `pro_01m2az5r1fm3pm71f3hpdyh704`; `pri_01m2b1zrqvpp1388t77wqystqt` → `pri_01m2azamdhj869n9vg6hc1wy85`.

## Sandbox settings (2026-09-12)
- **Default payment link**: `https://localhost:5173/billing/checkout` (entered as http, Paddle forced https). Consequence for L6: the local Vite dev server must serve HTTPS for `/billing/checkout` (Vite basic-ssl plugin) or the link gets pointed at a tunnel. Sandbox approves any domain.
- **Client-side token** `pilot-console-ui` (Active, value ends `78350`; copy it from Developer tools → Authentication → Client-side tokens when L3 needs `PILOT_CONSOLE_BILLING_PADDLE_CLIENT_TOKEN`).
- **API key**: NOT created; founder creates it (1-year expiry, scoped) and puts it into `/plugin configure paddle@claude-community` + SSM.
- **Notification destination**: NOT created; needs the public webhook URL (tunnel or staging). Create once, capture the secret once.
- Payment methods enabled by default: PayPal, Apple Pay, Bancontact (cards always on). Business name in sandbox has a typo "Quntflow" (Account settings; cosmetic).

## Still unset in live
- Default payment link (Checkout → Checkout settings): needs the console's real domain; live checkout requires an approved domain.
- Client-side token + API key (Developer tools → Authentication): founder creates; API key never enters chat.
- Notification destination (Developer tools → Notifications): needs the public webhook URL.
- Website approval, business/identity verification, payouts: founder.
- Live prices are immutable once used: a price change = new price + code pointing at it.

## Related
- `tasks/TASK-497-paddle-billing-integration.md` § Founder inputs
- `system/references/reference_paddle_claude_code_plugin.md`
