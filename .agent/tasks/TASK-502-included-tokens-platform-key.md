# TASK-502: Included token volume — platform Anthropic key per tenant, allowance metering, own-key overage

**Status**: 📋 PLANNED 2026-09-30 (founder decision: Pilot Console $499/month, Sonnet 5.5 volume included, extra on the customer's own key). Included volume number pending (recommendation: 100 tickets/month = $100 allowance, hard cap; `product/UNIT-ECONOMICS.md` § "$499 with included tokens").
**Created**: 2026-09-30 · **Owner**: Navigator plans, Pilot executes per repo · **Parent**: TASK-405 · **Sibling**: TASK-497 (Paddle L7, price object pri_01m3rf7qws3gzscj9fttg4aywe)

## Why
Today the tenant box runs only on the customer's key (BYO enforced at onboarding, `organizations.credentials` requires `ANTHROPIC_API_KEY`). The decided plan bundles tokens, so the box must run on a key we own until the allowance is used, then fall back to the customer's key, and the console must show both meters.

## Legs (draft, to be confirmed after the volume decision)
1. **Key ownership**: a platform Anthropic key per tenant (Anthropic Admin API workspace + key per org, so spend is attributable and revocable) provisioned by the reconciler into the tenant's secrets; customer key becomes optional at onboarding.
2. **Metering**: extend the S5 usage rollup with per-org month-to-date token dollars at list price, split platform-key vs own-key.
3. **Enforcement**: at allowance exhaustion, switch the box to the customer key (config generation bump) or pause dispatch if none is set; alert at 80 % and 100 %; reset on the billing period from Paddle's subscription dates.
4. **UI**: Plan & Billing shows "included: X of 100 tickets / $Y of $100 used", overage state, and the own-key prompt.
5. **Copy + catalog**: plan name "Pilot Console", $499, included volume in the Paddle product description and the console page (Paddle reviewers compare them).

## Open questions
- Included volume: tickets vs dollars (recommend: sell tickets, meter dollars).
- Hard cap vs soft overage on our key (recommend hard cap; own key = overage path).
- Whether the platform key is one Anthropic workspace per tenant (attribution, rate limits) or one shared key with console-side accounting.

## Refs
- `product/PRICING-MODEL.md` (decision banner), `product/UNIT-ECONOMICS.md` (margin table), `product/PRICING.md` (old Team tier, 150 tickets framing)
- TASK-497 § L7
