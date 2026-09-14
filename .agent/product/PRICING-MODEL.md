# Pricing model — Pilot Cloud

**Decided 2026-09-14 (founder).** This is the canonical statement of what we
charge for. `product/UNIT-ECONOMICS.md` holds the measured cost and margin
behind it; `product/PRICING.md` is the superseded tier model, kept for its
positioning and competitor sections only.

## The model

> **One flat monthly fee for the box and Pilot itself. Model tokens are the
> customer's own, billed by Anthropic directly on their key. We default to
> Sonnet 5 and let them change it in settings.**

Two bills. We never touch the second one, never mark it up, and never see it.

### What the fee covers

| Included | Not included |
|---|---|
| A dedicated EC2 instance, one tenant per VM (hard isolation boundary) | Anthropic model tokens — the customer's own API key, their own invoice |
| The Pilot daemon: executor, autopilot, CI monitoring, memory, knowledge graph | Their GitHub, Linear, Jira or Slack subscriptions |
| All connectors | |
| The console: board, instances, usage, billing, chat | |
| Provisioning, upgrades, backups, monitoring, support | |

### Price

**Open — the one item still to confirm.** The live and sandbox catalogs both
sit at USD 500.00/month today. The console page has always displayed $299,
which was never a decision: it was placeholder copy from a design mockup, and
the old tier doc said $149. Whatever we pick, the two must match — Paddle's
verification reviewers compare the site against the catalog.

**Recommendation: $299/month for the design-partner cohort.**

- Acquisition is the binding constraint at our customer count, not margin.
- $299 sits clearly under the ~$500 line where procurement gets involved at
  most companies; $500 sits exactly on it.
- Still ~81% margin ($243/mo after infra and Paddle fees), and **one customer
  still covers the $215/mo fixed control plane**.
- A founding price is easy to raise for later cohorts, and grandfathering the
  early ones is a feature rather than an apology.

The counter-argument, recorded honestly: at 100 tickets a month the customer's
all-in cost is roughly $650 against perhaps $10,000 of developer time, so we
capture a few percent of the value we create. Starting low makes raising harder
later. The judgement is that learning beats margin until the model is proven.

**Operational consequence:** Paddle prices are immutable once used, and both
catalog prices have now been used. Changing the number means **creating a new
price object and repointing `PILOT_CONSOLE_BILLING_PRICE_ID`** — never editing
the existing one. See `tasks/TASK-497-paddle-billing-integration.md` §L7.

## Why tokens stay bring-your-own

Three reasons, in order of weight.

1. **The customer already has a meter running.** Their Anthropic bill rises
   with usage, so they self-limit without us policing anything. Adding our own
   usage charge on top would bill them twice for the same sensation of
   variable cost.
2. **Zero cost, zero risk to us.** No token COGS, no credit exposure, no
   rate-limit pooling, no reselling margin to defend. The fleet design calls
   this the single biggest margin lever and it is right.
3. **It is a real differentiator.** "Never marked up" is a claim competitors
   who resell inference cannot make, and it is verifiable by the customer
   against their own Anthropic invoice.

It is already enforced, not merely promised: the Anthropic credential is
required at onboarding and provisioning is blocked without it.

## Why flat, not usage-based

**Our cost does not scale with tickets.** A tenant costs the same $40–89 of
infrastructure whether they ship 20 tickets or 375, because tokens are theirs
and the box is per tenant. A ticket allowance with overage would therefore be
revenue engineering with no cost behind it, and customers can tell.

It would also penalise exactly the behaviour we want. Deep usage is what makes
Pilot hard to remove; metering it discourages the adoption we are trying to
buy.

Rejected on 2026-09-14 after modelling four structures against real usage. The
base-plus-overage option was attractive on revenue (2.4x at our own box's
volume) and was the initial recommendation, withdrawn once the
cost-does-not-scale argument was made explicit.

## Model policy

**Default: `claude-sonnet-5` for every complexity tier. Changeable by the
customer in settings.**

Sonnet 5 is what our own box has run for all 8,046 routed executions in the
measured window, and it works well across trivial through complex work.

Two things make this a decision rather than a description of today:

1. **The shipped daemon default is not Sonnet everywhere.** Its routing table
   sends trivial to Haiku, simple and medium to Sonnet, and **complex to
   Opus**. A provisioned tenant inherits that unless we pin it.
2. **The console renders no model configuration at all.** The tenant config it
   generates carries org, environment, gateway port, repos and trackers, and
   nothing else. There is no switch today, and no way for a customer to choose.

**This matters for pricing**, because model choice drives the bill the customer
pays Anthropic. Opus on complex work is materially more expensive than Sonnet.
Any token budget we quote is only meaningful alongside a stated model default.
It also means the measured figures in `UNIT-ECONOMICS.md` are a **Sonnet-only**
baseline and would understate an Opus-routed tenant.

Choosing a model is the customer's call to make and ours to surface honestly,
with the cost implication shown next to the choice.

## What this requires us to build

| Gap | Where | Why it matters |
|---|---|---|
| Pin the tenant default to Sonnet 5 | Console tenant config render | Otherwise tenants silently inherit Opus on complex work |
| Model selector in settings | Console API + UI + config render + an allowlist of permitted models | The decided product surface; today there is none |
| Show projected token cost beside the choice | Settings | A model switch without its cost implication is a trap |
| Wire the Anthropic-tokens tile | Console usage page | Sits empty today; it is where "never marked up" becomes provable |
| Align plan name and price across catalog and page | Paddle catalog + console | Catalog says "Pilot Cloud — Design Partner", page says "Team"; reviewers compare them |
| Verify the "2 parallel executions" claim | Console tenant config render | Advertised on the billing page but never provisioned; it is only the daemon default |

## What a customer should budget

From our own box, 30 days, Sonnet 5 throughout, per completed execution:

| | |
|---|---|
| Median | $2.03 |
| Mean | $2.80 |
| 90th percentile | $5.87 |
| Range | $0.19 – $13.90 |

Per shipped ticket, including retries, $3.51. **Quote a range, never the mean:**
a heavy ticket costs seven times a light one. Caveat: this is Pilot developing
Pilot, a Go and TypeScript codebase with heavy test suites and a demanding
reviewer. A customer's mix will differ.

## Rejected alternatives

| Option | Why not |
|---|---|
| Base + included tickets + overage | Our cost does not scale with tickets; the customer's token bill already meters usage; penalises deep adoption |
| Pure per-ticket | Same, plus our fixed per-box cost needs a floor anyway, which collapses it back into base-plus-overage |
| Reselling tokens with a markup | Destroys the "never marked up" differentiator, introduces real COGS and credit risk, and forfeits the biggest margin lever we have |
| Per seat | One box serves a whole team; seats measure nothing we deliver |
| Free tier | Every tenant costs a real dedicated VM; there is no marginal-cost-zero tier to give away |

## Open items

- **Confirm the price** ($299 recommended) and, once chosen, create a new
  Paddle price object rather than editing the live one.
- Decide whether the model allowlist is open (any Anthropic model) or curated.
- Annual billing is not offered; Paddle supports it and it would cut the
  per-transaction fee from twelve charges a year to one.

## Sources

- `product/UNIT-ECONOMICS.md` — measured cost, margin, break-even, customer TCO
- `system/saas-fleet-design.md` §4, §7 — BYO-key decision, infra cost lines
- `tasks/TASK-497-paddle-billing-integration.md` — Paddle fees, catalog, L7 rules
- Founder box ledger — all measured token figures
