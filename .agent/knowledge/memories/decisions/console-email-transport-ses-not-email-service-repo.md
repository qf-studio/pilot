# Decision: Console email: SES transport, not the email-service repo and not Resend (2026-09-22)

## Summary
Founder decision 2026-09-22: keep SES as the console's email transport. The qf-studio/email-service repo stays undeployed (rejected 2026-07-13: unauthenticated API, at-most-once Redis queue, no DLQ); its SES + Resend transports are already vendored into pilot-console internal/email behind the synchronous /send-email endpoint that auth-service calls for reset/verification mail.

## Context
Asked while Nelya's SES identity item (console@quantflow.studio, eu-central-1) was open: 'do we need SES or can we use our own email-service repo?'

## Details
Live console-api task def already sets PILOT_CONSOLE_EMAIL_DEFAULT_TRANSPORT=ses with EMAIL_ENABLED=false; task role pilot-fleet-task-console-api already has ses:SendEmail. Resend would still need the same DKIM CNAMEs in Route 53 (Nelya's) plus an API key in SSM and an external vendor — saves her nothing. auth-service (quantflow cluster) has EMAIL_ENABLED=false and points at the console's /send-email via EMAIL_SERVICE_URL when enabled.

## Recommended Approach
Nelya: SES domain identity quantflow.studio in eu-central-1 with Easy DKIM, sandbox exit if needed. Then flip PILOT_CONSOLE_EMAIL_ENABLED=true on the next console deploy and enable auth-service email with EMAIL_SERVICE_URL → console. Do not deploy email-service standalone; do not add Resend unless SES is blocked.

## Related
- TASK-495
- TASK-405
- `pilot-console/internal/email/transport.go`
- `pilot-console/internal/email/ses.go`

---
**Captured**: 2026-09-22
**Confidence**: 95%
**Concepts**: ses, email, email-service, resend, pilot-console, nelya, auth-service
