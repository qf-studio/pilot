# SOP: Console email transport smoke (auth-service → console relay → SES)

**When**: after Nelya's template edits for TASK-501 leg C are on S3 (console-api `EmailEnabled` true + `PILOT_CONSOLE_EMAIL_TOKEN` secret; auth-service `EMAIL_ENABLED` true + `EMAIL_SERVICE_URL=https://console.pilotcloud.dev/api/v1`, sender, both URL bases, `EMAIL_API_KEY` secret) and console-api then auth-service have been redeployed (ours: Deploy QuantFlow AWS with `tenant_factory=true`, then auth-service's own workflow with `image_tag=v0.74.0`; on 2026-09-28 Nelya deployed both herself from the template sync), and after any later change to the relay, the token, or SES.
**Preconditions**: console PR#342 (relay under `/api/v1/send-email`, public verify/reset relays) and ui PR#187/#189 (landing pages) in prod. SES identity `console@quantflow.studio` verified, production access granted.

## 1. Relay is up and locked (no mail sent)
```bash
# route exists and refuses without the bearer (401), not 404
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://console.pilotcloud.dev/api/v1/send-email \
  -H 'Content-Type: application/json' -d '{"from":"x","to":["x"],"subject":"x","body":"x","transport":"default"}'
# expect 401. A 404 means EMAIL_ENABLED is still false on console-api; a 403 means the WAF or origin-verify blocked it.
```
Do not test with the real token from a laptop; the token lives only in SSM and the two task definitions.

## 2. One real send through the whole chain
1. In a private window open https://console.pilotcloud.dev/register and register a throwaway address you control (a `+tag` on the founder mailbox is fine).
2. Within ~1 min a mail from `console@quantflow.studio` arrives with a link `https://console.pilotcloud.dev/verify-email?token=…`.
3. Open the link: the page shows the verified state. A second open shows the **same** verified state — auth-service `VerifyEmail` is idempotent by design (`internal/auth/service.go`: double-click safe; `ConsumeEmailVerifyToken` returns early once `email_verified` is true) and logs another `email_verified` audit event on every replay until the 24h expiry. Do not file this as a defect (smoke 2026-09-28 did, then found the comment).
4. From the login page use "Forgot password?" with the same address → a reset mail arrives → `/reset-password?token=…` → set a new password → log in with it.

## 3. Evidence to record
- console-api log (`/pilot-fleet/console-api`): one `send-email` request per step with a recipient **domain** only, status 2xx; no 429.
- auth-service log: `EventRegister` then `EventEmailVerified`; `EventPasswordReset` then `EventPasswordResetConfm`.
- SES console → sending statistics: deliveries +2, bounces 0, complaints 0 (the founder mailbox must not mark it spam).
- Post the four timestamps in `#infrastructure` and mark TASK-501 leg C done.
- Founder mailbox: the Gmail MCP connector may return nothing even for `newer_than:3h`; read the alias inbox through the browser (`mail.google.com/mail/u/1/`, the gmail.com account slot) instead. Both mails landed in Inbox on 2026-09-28.
- The register/verify/forgot pages redirect to the dashboard while any console session is active: log out first (register logs the new user in immediately).

## Run log
- **2026-09-28 PASSED** — register 16:12:47Z → verify 16:15:32Z → reset mail 16:16:47Z → confirm 16:27:37Z / login 16:29:07Z (UTC). SES 2 attempts / 0 bounces / 0 complaints; console-api two `send-email` 202 with `to_domains=[gmail.com]`; auth-service audit `register`, `email_verified`, `password_reset`, `password_reset_confirm`, `login_success`. Smoke user `usr_dad0a776…`, org `ses-smoke-0928` (tenant hygiene: delete with the ship-test clone).

## 4. If it fails
| Symptom | Where to look |
|---|---|
| Register works, no mail, auth-service logs `EventEmailVerifyEmailFailed` | auth-service cannot reach `EMAIL_SERVICE_URL` or the bearer mismatches: compare `EMAIL_API_KEY` with `PILOT_CONSOLE_EMAIL_TOKEN` in SSM (same secret, both templates). |
| console-api logs 401 on `/api/v1/send-email` | token mismatch (same as above). |
| console-api logs 2xx, no mail | SES: identity, region (eu-central-1), sandbox status, or the task role lacks `ses:SendEmail` on the identity ARN. Check `aws sesv2 get-email-identity`. |
| 403 at the edge | WAF rule on the JSON body or missing `X-Origin-Verify`; ask Nelya for the sampled request (`wafv2 get-sampled-requests`). |
| Link opens a 404 | UI not deployed (`build-sha.txt` vs main) or route missing from the public set. |

Never paste tokens, links with tokens, or full recipient addresses into Slack or issues.
