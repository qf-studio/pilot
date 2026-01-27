# Pilot: Pricing Strategy

## Pricing Philosophy

**Value metric:** Tickets completed (not seats, not tokens)

Why:
- Aligns with customer value ("I paid for 50 tickets, got 50 PRs")
- Predictable costs for budgeting
- Doesn't penalize team growth
- Easy to justify ROI ("$5/ticket vs $50/hour dev time")

---

## Tier Structure

### Free Tier
**$0/month**
- 5 tickets/month
- 1 project
- Community support
- Public repos only

**Purpose:** Try before buy. Prove value on real tickets.

---

### Solo
**$29/month**
- 30 tickets/month
- 3 projects
- Private repos
- Email support
- Usage dashboard

**Target:** Solo devs, freelancers

**ROI pitch:** "30 tickets × 2 hours saved = 60 hours. That's $3,600 of dev time for $29."

---

### Team
**$149/month**
- 150 tickets/month
- Unlimited projects
- Team usage dashboard
- Priority support
- Cross-project memory
- Slack notifications

**Target:** Team leads (3-8 devs)

**ROI pitch:** "150 tickets/month = 3 junior devs worth of CRUD work."

**Budget note:** Under $500/mo = no procurement needed at most companies.

---

### Enterprise
**Custom pricing**
- Unlimited tickets
- SSO/SAML
- Audit logging
- Dedicated support
- SLA
- On-prem/air-gapped option
- Custom integrations

**Target:** Engineering orgs 50+ devs

**Starting at:** $1,500/month (negotiable based on org size)

---

## Overage Handling

Options (pick one):

**A. Hard cap (recommended for launch)**
- Hit limit → tickets queue until next month
- Simple, predictable
- Users can upgrade mid-month

**B. Soft cap with overage**
- $3/ticket over limit
- More revenue but unpredictable bills
- Can cause billing surprises

**Recommendation:** Start with hard cap. Add overage option later if customers request it.

---

## Annual Discount

- 20% off for annual payment
- Solo: $278/year (saves $70)
- Team: $1,430/year (saves $358)

---

## Competitor Pricing Reference

| Tool | Model | Price | Notes |
|------|-------|-------|-------|
| GitHub Copilot | Per seat | $19/mo individual, $39/mo business | Usage unlimited |
| Cursor | Per seat | $20/mo pro, $40/mo business | Usage unlimited |
| ClawdBot | Token-based | Variable | Unpredictable costs |
| Pilot | Per ticket | $29-149/mo | Predictable, value-aligned |

**Positioning:** More expensive than Copilot per-seat, but different value prop. Copilot assists, Pilot completes.

---

## Launch Pricing Strategy

**Phase 1: Early Adopter (first 100 users)**
- 50% off first 3 months
- Lock in feedback loop
- Build case studies

**Phase 2: Public Launch**
- Full pricing
- Free tier available
- 14-day trial of Team tier

**Phase 3: Enterprise**
- After 10+ Team customers
- Build from champion referrals

---

## Metrics to Track

| Metric | Target | Why |
|--------|--------|-----|
| Free → Solo conversion | 15% | Proves value prop works |
| Solo → Team upgrade | 10% | Team expansion signal |
| Monthly churn | <5% | Product-market fit |
| Tickets/customer/month | 70% of limit | Usage = value |
| PR merge rate | >80% | Quality signal |

---

## Pricing Page Copy

**Header:** "Pay for tickets completed, not seats filled"

**Subhead:** "Pilot charges by results. Every tier includes unlimited team members."

| | Free | Solo | Team | Enterprise |
|---|------|------|------|------------|
| **Price** | $0 | $29/mo | $149/mo | Custom |
| **Tickets/month** | 5 | 30 | 150 | Unlimited |
| **Projects** | 1 | 3 | Unlimited | Unlimited |
| **Private repos** | - | ✓ | ✓ | ✓ |
| **Cross-project memory** | - | - | ✓ | ✓ |
| **Team dashboard** | - | - | ✓ | ✓ |
| **SSO/Audit logs** | - | - | - | ✓ |

**FAQ:**

*"What counts as a ticket?"*
One Linear/GitHub/Jira issue = one ticket. Regardless of complexity.

*"What if the PR needs revisions?"*
Revisions on the same ticket don't count as new tickets.

*"Can I roll over unused tickets?"*
No. Use it or lose it keeps pricing simple.

*"What if I need more tickets mid-month?"*
Upgrade anytime. Prorated billing.

---

## Internal Notes

**Why not per-seat?**
- Team leads want to add devs without cost increase
- Per-seat penalizes growth
- Ticket-based aligns incentives (we want them to use it)

**Why not pure token-based?**
- Unpredictable costs scare team leads
- Hard to budget
- ClawdBot's main complaint is token burn

**Why not unlimited?**
- Need sustainable unit economics
- Prevents abuse
- Creates upgrade pressure
