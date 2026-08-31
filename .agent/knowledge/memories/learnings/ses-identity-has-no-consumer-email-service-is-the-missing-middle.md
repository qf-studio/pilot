---
name: ses-identity-has-no-consumer-email-service-is-the-missing-middle
description: The ControlPlaneStack SES domain identity has zero deployed consumers — auth-service emails via HTTPSender → the standalone email-service REST API (SMTP/SES/Mailgun/Resend transports, GitLab linear-invoices org), which is deployed NOWHERE in the stack; using its Resend/Mailgun transport decouples the launch email leg from the SES domain/sandbox-exit entirely
type: learning
---

# SES identity has no consumer; email-service is the missing middle

**Established 2026-08-31** (founder pointed at the existing service during the
S4 minimal-console cycle; verified across three repos).

## The three pieces, and the gap between them

1. **email-service** (`~/Projects/startups/email-service`, GitLab
   `linear-invoices/email-service`): standalone multi-transport REST email
   service — SMTP, AWS SES, Mailgun, Resend — templating + Redis async.
2. **auth-service** production email path is already wired to it:
   HTTPSender in the auth-service email package posts to the email-service
   /send-email REST API (dev = console logger sender that never errors).
3. **pilot-cloud-infra** ControlPlaneStack provisions an SES *domain
   identity* (Easy DKIM) "for outbound console email" — and its own ses.go
   comment warns the account starts in the SES sandbox (200 msgs/day;
   production-access request required).

**The gap:** the control-plane instance user-data deploys only
pilot-console, auth-service(+migrate), and redis. email-service is deployed
nowhere; nothing in the stack calls SES directly. So the SES identity has
no consumer AND auth-service's sender has no endpoint — the email leg is
scaffolding on both ends with a missing middle.

## How to apply

- **S4 exit week: irrelevant** — email is off by design
  ([[no-stripe-local-first-s3-testing]] precedent; not in the exit clause).
- **Staging/launch planning**: the deferred "domain/ACM/SES" gate is
  separable — deploy email-service beside auth-service and pick its
  Resend/Mailgun transport, and the SES domain identity + sandbox-exit
  request drop out of the critical path (ACM/domain remain only for the
  public ALB). Don't spec an email leg that re-invents sending inside
  console/auth-service; the integration contract already exists.
- Related: [[s4-unblock-minimal-invpc-ec2-console]] (minimal mode already
  skips the SES construct with nothing lost).
