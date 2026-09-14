---
name: pricing-flat-fee-byo-tokens-sonnet-default
description: Pilot Cloud pricing decided 2026-09-14 — one flat monthly fee for box + Pilot, model tokens bring-your-own billed by Anthropic directly, default claude-sonnet-5 with a customer-facing switch. Usage-based billing rejected because our cost does not scale with tickets and the customer's own token bill already meters usage.
type: decision
---

# Pricing: flat fee, BYO tokens, Sonnet 5 default with a switch

**Decided 2026-09-14 by the founder**, after modelling four structures against
30 days of measured usage from the founder box.

## The decision
One flat monthly fee covering the dedicated box and Pilot itself. **Model
tokens are the customer's own**, billed by Anthropic directly on their key,
never marked up. Default model **`claude-sonnet-5`**, changeable by the
customer in settings.

## Why usage-based billing was rejected
- **Our cost does not scale with tickets.** A tenant costs $40–89 of infra
  whether they ship 20 or 375, because tokens are theirs and the box is per
  tenant. Ticket allowances would be revenue engineering with no cost behind
  them.
- **The customer already has a meter running** — their own Anthropic bill rises
  with usage, so they self-limit. Our meter on top would double-charge the
  sensation of variable cost.
- It would penalise deep usage, which is exactly what makes Pilot hard to remove.

A base-plus-overage model ($349 + $3/merged PR over 100) was modelled and
initially recommended — 2.4x revenue at the measured volume — then **withdrawn**
once the cost-does-not-scale argument was made explicit. Recorded so the
reasoning is not re-litigated from the revenue number alone.

## Model default matters for the customer's bill
The shipped daemon routes complex work to **Opus** by default; the console
renders **no model config at all**, so a tenant silently inherits that. Our own
measured token figures are **Sonnet-only** and would understate an Opus-routed
tenant. Pinning the default and exposing the switch are both required work.

## Price
Still open at decision time; **$299/month recommended** for the design-partner
cohort (acquisition beats margin at this customer count; under the ~$500
procurement threshold; still ~81% margin and one customer covers the $215/mo
fixed control plane). Catalog currently USD 500. **Paddle prices are immutable
once used** — a change means a new price object plus repointing the price-id
env var, never an edit.

## Refs
- `product/PRICING-MODEL.md` (canonical), `product/UNIT-ECONOMICS.md` (measured)
- `product/PRICING.md` superseded for cost/margin
- Related: [[payment-processor-is-paddle-not-stripe]]
