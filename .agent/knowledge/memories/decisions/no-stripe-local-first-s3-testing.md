---
name: no-stripe-local-first-s3-testing
description: Founder decisions 2026-08-18 — (1) Stripe is unusable, Montenegro is not a supported country; payment processing will be swapped to another provider later (MoR candidates work from ME). (2) No staging domain yet — SaaS testing continues on the local stack; the infra PR#25 staging deploy (ALB/ACM/SES/SPA) is deferred until a domain + processor exist.
type: decision
---

# No Stripe (Montenegro) + local-first S3 testing (founder, 2026-08-18)

## Payment

- **Stripe cannot be used at all**: Montenegro is not a Stripe-supported
  country. This invalidates the "Stripe test keys / price / webhook secret"
  founder-input line that gated the S3 exit since v9.x of the roadmap.
- Founder: "we will change the payment processing system a bit later."
  Processor selection is an open founder decision — merchant-of-record
  providers (Paddle, Lemon Squeezy, Polar) support ME-based sellers and
  avoid entity setup; revisit at release.
- Console impact: Stripe Checkout is flag-gated in pilot-console — keep the
  flag OFF; do not build further Stripe surface (webhook registration,
  billing lifecycle S5 leg) until the processor decision lands. CON-5
  billing portal (TASK-478) inherits this gate.

## Domain / staging

- **No domain purchase now.** The account only holds getpointer.net/.app
  zones (Pointer's product; wildcard cert bound to pointer-alb). Pilot
  Cloud branding domain = later founder decision.
- **S3 validation continues LOCAL-FIRST**: local console stack
  (`make local-up`, console :8090, UI :5173 http-mode) + live fleet VPC
  tenant boxes — the exact configuration that passed the un-patched ship
  test 08-16. The staging deploy per infra PR#25 (control-plane EC2 → ALB/
  ACM → SES → SPA hosting) stays merged-but-undeployed until domain +
  processor exist.
- Effective S3 exit test therefore excludes payment: signup → credentials
  → provision → first PR, zero operator SSH, 3+ tenants — payment leg
  deferred to the processor swap.

Supersedes the "exit gated on founder staging inputs (Stripe/domains/ACM)"
framing in TASK-405 / saas-roadmap "Next" blocks. See [[approvals-off-stage-auto-merge]]
for the decision-memory idiom.
