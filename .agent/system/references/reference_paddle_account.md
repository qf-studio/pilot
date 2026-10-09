---
name: reference_paddle_account
description: Paddle account facts for Pilot Cloud billing — live (Quantflow) and sandbox accounts, catalog ids in both (live pro_01m2az5r1fm3pm71f3hpdyh704 / pri_01m2azamdhj869n9vg6hc1wy85 $500 / pri_01m4g870rh98m207cjwhjn8taq $499 live twin 10-09; live client token ctkn_01m4g87ga97b6stpk4hxp5zg41; sandbox pro_01m2b1xhwtvnmh5atk6zpdagdb / pri_01m2b1zrqvpp1388t77wqystqt; USD 500/month, SaaS), sandbox→live mapping, what is still unset.
type: reference
---

# Paddle account — Pilot Cloud

**Live vendor account**: `vendors.paddle.com`, account name "Quantflow", founder login. Onboarding at 0/3 (migrate · verify · go live). **Sandbox account created 2026-09-12** (`sandbox-vendors.paddle.com`, same email; business type Private, Montenegro). Ids differ from live.

## Live catalog (created 2026-09-12 via dashboard, founder decision: one plan, no trial, monthly only)

| Entity | ID | Details |
|---|---|---|
| Product | `pro_01m2az5r1fm3pm71f3hpdyh704` | "Pilot Cloud — Design Partner", tax category **SaaS**, status Active |
| Price | `pri_01m2azamdhj869n9vg6hc1wy85` | "Design Partner — monthly", internal `design-partner-monthly-usd`, **USD 500.00 / month**, recurring, tax mode account default, no trial, quantity 1–1, no country overrides, Active |
| Price | `pri_01m4g870rh98m207cjwhjn8taq` | **created 2026-10-09 11:54Z via paddle-live MCP** (Navigator, L7, additive): name "Pilot Console", internal `pilot-console-monthly-usd-499`, **USD 499.00 / month**, recurring, tax mode resolved to `location` (account default, same as the $500 row), no trial, quantity 1–1, `custom_data.plan=pilot-console`, Active, unused. **This is the live `PILOT_CONSOLE_BILLING_PRICE_ID` / `BillingPriceId` value.** |

Currency: Paddle converts automatically from the USD base by buyer location ("Based on the buyer's location, we convert currency automatically"), so EUR buyers (Montenegro, euro area) pay a converted EUR amount without a price override. Add explicit country prices later only if the converted amounts need rounding.

Decisions NOT taken (founder, 2026-09-12): no 7-day trial, no annual price, no GBP/EUR/AUD overrides, no Solo/Team tiers from `product/PRICING.md` (those predate hosted COGS: ~$52/mo per tenant → no tier below ~$99 until scale-to-zero).

## Sandbox catalog (created 2026-09-12 via dashboard, mirrors live)

| Entity | ID | Details |
|---|---|---|
| Product | `pro_01m2b1xhwtvnmh5atk6zpdagdb` | "Pilot Cloud — Design Partner", tax category SaaS, Active |
| Price | `pri_01m2b1zrqvpp1388t77wqystqt` | "Design Partner — monthly", internal `design-partner-monthly-usd`, USD 500.00 / month, recurring, no trial, quantity 1–1, Active (used by the 09-14 smoke → immutable) |
| Price | `pri_01m3ppe0ec65bq7312dpyf1mtg` | **created 2026-09-29 via API** (Navigator, L7): name "Design Partner", internal `design-partner-monthly-usd-299`, **USD 299.00 / month**, recurring, tax mode account default, no trial, quantity 1–1, `custom_data.plan=design-partner`, **ARCHIVED 2026-09-30** (superseded by the $499 decision, never used). |
| Price | `pri_01m3rf7qws3gzscj9fttg4aywe` | **created 2026-09-30 via API** (Navigator, L7): name "Pilot Console", internal `pilot-console-monthly-usd-499`, **USD 499.00 / month**, recurring, tax mode account default, no trial, quantity 1–1, `custom_data.plan=pilot-console`, Active, unused. **This is the `PILOT_CONSOLE_BILLING_PRICE_ID` candidate**; live twin pending. |
| (archived) | `pro_01m2b1xhwfc50f3c344awy8pj9` | duplicate from a double submit, archived immediately, no prices |

Sandbox → live mapping: `pro_01m2b1xhwtvnmh5atk6zpdagdb` → `pro_01m2az5r1fm3pm71f3hpdyh704`; `pri_01m2b1zrqvpp1388t77wqystqt` → `pri_01m2azamdhj869n9vg6hc1wy85` ($500); `pri_01m3ppe0ec65bq7312dpyf1mtg` ($299) archived 09-30, no twin; `pri_01m3rf7qws3gzscj9fttg4aywe` ($499, decided 09-30) → **`pri_01m4g870rh98m207cjwhjn8taq` (live twin created 2026-10-09)**.

## Sandbox settings (2026-09-12)
- **Default payment link**: `https://localhost:5173/billing/checkout` (entered as http, Paddle forced https). Consequence for L6: the local Vite dev server must serve HTTPS for `/billing/checkout` (Vite basic-ssl plugin) or the link gets pointed at a tunnel. Sandbox approves any domain.
- **Client-side token** `pilot-console-ui` (Active): `test_05a1a67b5e1f7ce4cead5778350` — public-safe by design (Paddle.js browser token); value for `PILOT_CONSOLE_BILLING_PADDLE_CLIENT_TOKEN` in sandbox.
- **API key** `pilot-console-dev` created 2026-09-12 (expires 2027-09-11, All read + All write, not rotatable). Its value was never found on disk (the plugin user-config was empty, hence the standing `paddle-sandbox` MCP 401). Treat as orphaned; revoke when convenient.
- **API key** `pilot-console-doppler-dev` created 2026-09-29 (expires 2027-09-29, rotatable, 23 scoped permissions: product/price/customer/address/business/transaction/subscription/notification_setting RW; adjustment/discount/client_token/notification/payment_method R; notification_simulation RW). **Value lives only in Doppler** `pilot-console` / `dev` as `PADDLE_SANDBOX_API_KEY` (decision `secrets-live-in-doppler-pilot-console-project`); sessions use it via `doppler run`. Verified 09-29: `GET /products` → 200. Rotation reminder: before 2027-09-29.
- **Notification destination**: NOT created; needs the public webhook URL (tunnel or staging). Create once, capture the secret once.
- Payment methods enabled by default: PayPal, Apple Pay, Bancontact (cards always on). Business name in sandbox has a typo "Quntflow" (Account settings; cosmetic).

## Production console state (checked 2026-09-29)
- ECS task def `pilot-fleet-console-api:21` (image `prod-0.1.6`) carries **no** `PILOT_CONSOLE_BILLING_*` env or secrets: checkout routes are unregistered in production. Going live needs Nelya's console-api template to gain the six billing values (D6 table in TASK-497) with the three secret ones from SSM SecureString.

## Live MCP connection (2026-10-09)
- `vendors.paddle.com/mcps` → connection "Claude Code" (OAuth, founder login `hello@quantflow.studio`). "Inherit my permissions" OFF; granted **Prices Write** and **Client-side tokens Write** only, everything else read. The OAuth-issued token picked the new scopes up without re-auth. Grant more scopes there if a later leg needs them (e.g. Notification settings Write).

## Live client-side token (2026-10-09)
- `ctkn_01m4g87ga97b6stpk4hxp5zg41` name `pilot-console-ui`, Active, created via MCP. Value `live_315cd0503ade84f882cd7679662` (public-safe by design; Paddle.js browser token). Stored in Doppler `pilot-console`/`prd` as `PADDLE_CLIENT_TOKEN`; SSM `/pilot-fleet/console/BILLING_PADDLE_CLIENT_TOKEN` still to be written once Nelya's template lands.

## Live API key (2026-10-09)
- `pilot-console-prod`, created in the dashboard (form filled by Navigator via Chrome, Save + copy by founder), **rotatable**, expires **2027-01-07** (90 days — rotation runbook line needed before then), **24 scopes** = the 23 sandbox scopes + `customer_portal_session.write` (portal route). Value in Doppler `pilot-console`/`prd` as `PADDLE_API_KEY` (prefix `pdl_live_` verified; `GET /products` → 200 via `doppler run`). SSM `/pilot-fleet/console/BILLING_PADDLE_API_KEY` still to be written once Nelya's template lands. Value never entered chat.

## Still unset in live
- Default payment link (Checkout → Checkout settings): needs the console's real domain; live checkout requires an approved domain.
- Notification destination (Developer tools → Notifications): needs the public webhook URL.
- Website approval, business/identity verification, payouts: founder.
- Live prices are immutable once used: a price change = new price + code pointing at it.

## Related
- `tasks/TASK-497-paddle-billing-integration.md` § Founder inputs
- `system/references/reference_paddle_claude_code_plugin.md`
