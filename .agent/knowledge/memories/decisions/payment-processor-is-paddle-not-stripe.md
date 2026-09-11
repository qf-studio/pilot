---
name: payment-processor-is-paddle-not-stripe
description: Founder decision 2026-09-10 — Pilot Cloud billing uses Paddle (merchant of record), not Stripe; Stripe is unavailable for the founder's location/regulatory situation. Existing flag-gated Stripe Checkout code in pilot-console (#60/#71/#72/#73) is dead-end scaffolding; the S5 "billing lifecycle" leg must be designed against Paddle Billing.
type: decision
---

# Payment processor: Paddle, not Stripe

**Decision (founder, 2026-09-10):** "Next we gonna use Paddle, I can't use Stripe for the location and regulations they have."

## Consequences
- The S5 roadmap item "billing lifecycle (suspend on past_due)" and the architecture's "Stripe subscription lifecycle" are re-targeted to **Paddle Billing**. Paddle is merchant of record (handles VAT/sales tax and invoicing), which also removes the tax-handling scope Stripe would have left with us.
- pilot-console already carries flag-gated Stripe Checkout scaffolding (#60 checkout session + webhook + billing status, #71 config flag, #72 handlers, #73 routes + vendored stripe-go) and the UI has `billingStatus`/checkout-session plumbing. Treat as **dead-end**: do not extend; the Paddle leg replaces the provider behind the same `billingStatus` contract where possible and removes stripe-go when the Paddle path lands.
- Integration shape to plan (nav-task before dispatch): Paddle.js overlay/hosted checkout from the settings-billing page; webhook endpoint verifying the Paddle-Signature HMAC; events subscription.created/updated/canceled and transaction.completed/past_due → org billing status → instance suspend on past_due (fleet desired_state), resume on payment; Paddle sandbox for tests; customer portal link for self-serve cancel.
- The usage rollup (#283) remains flat-plan groundwork; Paddle usage-based billing is v2.

**Contradiction**: compliance/eligibility (Stripe unavailable for the founder's jurisdiction) vs. the sunk Stripe scaffolding and its familiarity.
**Separation**: system — swap the provider behind the console's existing billing-status contract rather than redesigning billing.
**Principle**: keep the domain contract, replace the vendor adapter.

Related: [[stripe-scaffolding-is-flag-gated-dead-end]], TASK-405, TASK-496.
