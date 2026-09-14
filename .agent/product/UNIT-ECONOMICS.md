# Unit economics — Pilot Cloud, one tenant

**As of 2026-09-14.** Every figure below is either a measured value from the
founder box's own ledger or a line item from `system/saas-fleet-design.md` §7.
Nothing here is an estimate dressed as a measurement; where a number is
modelled rather than observed, it says so.

Supersedes the tier model in `product/PRICING.md` for anything to do with cost
or margin. That document was last touched 2026-07-03, predates hosted COGS and
the Paddle decision, and still describes a per-ticket value metric with
Free/Solo/Team/Enterprise tiers that no longer exist. **Treat it as historical
until it is rewritten.**

## The model in one line

The customer pays **us $500/month** for a dedicated box and the orchestration
on it, and pays **Anthropic directly** for model tokens on their own API key.
Two bills, one of which we never see and never mark up.

Bring-your-own key is enforced, not just claimed: the Anthropic credential is
required at onboarding and provisioning is blocked without it. It is also the
single biggest margin lever — token COGS to us is zero.

## Our side — contribution margin per tenant per month

| Scenario | Infra | Paddle fee | Total COGS | Margin | Margin % |
|---|---|---|---|---|---|
| Always-on | $89 | $25.50 | $114.50 | $385.50 | 77.1% |
| Typical (≈50% duty cycle) | $52 | $25.50 | $77.50 | $422.50 | 84.5% |
| Typical + 1-yr savings plan | $40 | $25.50 | $65.50 | $434.50 | 86.9% |
| Idle / parked | $14 | $25.50 | $39.50 | $460.50 | 92.1% |

Infra lines are `system/saas-fleet-design.md` §7 (t3.large, 30GB root + 100GB
data gp3, DLM snapshots, NAT share). Paddle is merchant-of-record at **5% +
$0.50 per transaction**, so $25.50 on a $500 charge.

**Paddle is the second-largest cost line** and overtakes compute entirely once
the tenant sleeps. It is fixed as a percentage, so it does not improve with
scale the way infra does.

## Fixed control plane and break-even

**~$215/month per environment**, independent of tenant count: ALB $25, NAT
gateway base $33, RDS multi-AZ $60, console-api $35, auth-service + Redis $30,
CloudFront/S3/Route53/ACM $5, ops Prometheus/Grafana $25.

**The first customer covers it**, with ~$220 left over.

| Tenants | Revenue | Gross margin | Net of fixed |
|---|---|---|---|
| 1 | $500 | $435 | $220 |
| 2 | $1,000 | $869 | $654 |
| 5 | $2,500 | $2,172 | $1,958 |
| 10 | $5,000 | $4,345 | $4,130 |
| 25 | $12,500 | $10,862 | $10,648 |
| 50 | $25,000 | $21,725 | $21,510 |

Gross margin uses the typical + savings-plan case ($434.50/tenant). The model
scales linearly and boringly, which is the point.

## Customer side — measured, not modelled

Reference tenant is **our own founder box, 30 days to 2026-09-14**, read from
`executions` in the box ledger:

| Measure | Value |
|---|---|
| Executions | 586 |
| Distinct tasks shipped | 375 |
| Executions per shipped task | 1.34 |
| Token spend (their bill) | $1,317.96 |
| Tokens | 19.6M |
| Token cost per shipped task | $3.51 |
| All-in cost per shipped task | $4.85 |
| Our share of their total bill | 28% |

Reproduce with:

```sql
SELECT count(*), round(sum(estimated_cost_usd),2), round(sum(tokens_total)/1e6,1)
  FROM executions WHERE created_at >= datetime('now','-30 days');
SELECT count(DISTINCT task_id) FROM executions
  WHERE created_at >= datetime('now','-30 days') AND status='completed';
```

### What a tenant pays at different intensities

| Tickets/month | Token bill | Plus our $500 | All-in per ticket | We are |
|---|---|---|---|---|
| 20 | $70 | $570 | $28.51 | 88% |
| 50 | $176 | $676 | $13.51 | 74% |
| 100 | $351 | $851 | $8.51 | 59% |
| 200 | $703 | $1,203 | $6.01 | 42% |
| 375 | $1,318 | $1,818 | $4.85 | 28% |

Per-ticket token cost is held at the measured $3.51. Real spend varies with
ticket size; this is a linear projection off one tenant's mix, so treat the
shape as sound and the absolute as indicative.

## Findings

### 1. The flat fee has the wrong shape at the low end
A 20-ticket tenant pays $28.51 per ticket and **88% of their bill is us**. A
375-ticket tenant pays $4.85 and we are 28%. Our cost barely moves between
them. The flat fee therefore punishes light users and is close to free for
heavy ones. That argues for a lower entry price, a usage floor, or an explicit
"design partner" framing — **not** for charging more.

### 2. Retries are billed to the customer, not to us
586 executions produced 375 shipped tasks. **$156.84 of token spend — 12% —
bought work that never shipped** (failed, stalled, declined, no-op). Because
tokens are BYO, every reliability fix is money back in the customer's pocket,
not ours. That is a sharper story than "quality improvements" and it is
measurable per tenant.

### 3. Idle-sleep is a revenue feature, not an optimisation
Always-on costs $89 and drops margin to 77%. The fleet design's own conclusion:
anything below ~$99/month does not survive an always-on tenant. At $500 there
is comfortable headroom, but the sleep scheduler is what keeps it.

### 4. Disclosure is the real gap, not the price
Nothing on the billing page tells a prospect their token bill will likely
exceed our invoice. The page says "Your plan is flat — this is what the box did
with it", which is true of our line and false of their total. The **Your
Anthropic tokens** tile is the honest place to close this, and it is empty
today because the box does not report usage into the console yet. Wiring it
turns an awkward surprise into proof of the no-markup claim.

## Value benchmark

At a $50/hour loaded developer rate:

| Tickets/month | All-in cost | Equivalent dev-hours | Break-even if Pilot saves |
|---|---|---|---|
| 50 | $676 | 13.5 h | ~16 min/ticket |
| 200 | $1,203 | 24.1 h | ~7 min/ticket |
| 375 | $1,818 | 36.4 h | ~6 min/ticket |

## Open questions for the founder

- Entry price or usage floor for light tenants (finding 1).
- Whether to surface a projected token range during onboarding (finding 4).
- Annual pricing: not offered today; Paddle supports it, and it would cut the
  per-transaction fee count from 12 to 1 per tenant-year (~$280 saved per 100
  tenant-years, minor).
- Live catalog says "Pilot Cloud — Design Partner"; the console page says
  "Team". Same class of mismatch as the price defect closed on 2026-09-14, and
  Paddle's verification reviewers compare the two.

## Sources

- `system/saas-fleet-design.md` §7 — infra and fixed control-plane line items
- `tasks/TASK-497-paddle-billing-integration.md` — Paddle fee, catalog price
- `system/references/reference_paddle_account.md` — USD 500.00/month catalog
- Founder box ledger (`~/.pilot/data/pilot.db`, `executions`) — all measured values
- `product/PRICING.md` — superseded tier model, kept for history
