# TASK-513: Real allowance exhaustion on a prod test org — suspend without a key, resume on own-key save, run on the own key

**Status**: 🚀 **IN PROGRESS 2026-10-07** — step 1 (test org on console.pilotcloud.dev).
**Created**: 2026-10-07 · **Parent**: TASK-502 (closes the "one real exhaustion" item) · **Owner**: founder drives the console UI (account, GitHub token, own key); Navigator observes and records; Nelya runs one `consolectl` command.
**Why**: TASK-512 is live (reconciler rev 27, flag on, 15m poll) but has never acted on real usage. Unit tests + the local stack proved the logic on seeded rows; this proves the wire to a real box: snapshot → exhausted → EC2 stop → key saved → resume → selector `own` → model spend moves to the customer key.

## Constraints
- No IAM change for the founder user (decision 10-07): `consolectl` runs only as the reconciler task; Nelya runs it. One command, batched.
- Navigator never types credentials on a non-local host: founder registers, logs in, pastes the GitHub token and the own Anthropic key.
- Do NOT terminate the test org afterwards: terminate deactivates pool-01 at Anthropic (leg 1c) and retires the pool row for good. Stop or suspend the box instead.
- Spend: org credits are prepaid ($9.57 on 10-05); two tiny tasks cost cents.

## Steps
| # | Who | Action | Evidence |
|---|---|---|---|
| 1 | founder | Register `exhaustion-test@…`, create org "Exhaustion Test" on console.pilotcloud.dev | org id from `GET /api/v1/org` (Navigator reads it from the page/network) |
| 2 | founder | Connections → GitHub → token for `qf-studio/pilot-canary-sandbox`. No Anthropic key. | row "Connected" |
| 3 | Navigator | Instances → Provision | `platform_key.assigned=true label=pool-0N`; instance running |
| 4 | Navigator | Label one tiny issue `pilot` in the sandbox repo | PR opens; usage poll (≤15 min) → `usage.usd_cents > 0` on Plan & Billing |
| 5 | Nelya | `aws ecs run-task` reconciler task def, override `["billing","set-allowance","--org","<ORG_ID>","--tickets","1","--usd-cents","1","--hard-cap=true","--note","exhaustion test"]` | her log line `source=org_override` |
| 6 | Navigator | Watch next tick (≤60 s after the next poll) | events `allowance.exhausted`, `allowance.no_own_key`, `allowance_suspended`; Instances "Paused: …"; Plan & Billing suspended callout; EC2 stopped |
| 7 | founder | Connections → paste own Anthropic key | `allowance.resumed` reason `own_key_saved`; box starts; "Running on your Anthropic key"; Connections "pool-0N is held for this period" |
| 8 | Navigator | Label a second issue | runs; platform.claude.com: pool workspace spend flat, own key spend grows |
| 9 | founder | Stop/suspend the box from the UI (not terminate) | — |
| 10 | Nelya (any time) | `set-allowance --org <ORG_ID> --clear` | optional; the override only affects the test org |

## Log
- 10-07: plan agreed; step 1 started.
