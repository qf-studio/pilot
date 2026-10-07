# TASK-513: Real allowance exhaustion on a prod test org — suspend without a key, resume on own-key save, run on the own key

**Status**: 🛑 **BLOCKED at step 3 2026-10-07 ~10:45Z — prod cannot provision any box since console PR#321 (09-24).** Fix dispatched: [console#367](https://github.com/qf-studio/pilot-console/issues/367) (`pilot`). Resume at step 3 once #367 is merged + deployed (console deploys on main automatically — mem-202) and the looping row is cleared. Org **Testica** `b79d7a56-ab31-4dff-8844-961afc1239c7` (name differs from the plan; harmless). Instance `5ee13c38-db8e-42b9-ad16-ad13f4165916` eu-central-1.
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
- 10-07 ~12:xxZ: step 1 done (org "Testica", ORG_ID `b79d7a56-ab31-4dff-8844-961afc1239c7`, user `usr_5d71a99d…`). Step 2 done — GitHub PAT validated, sandbox repo "watching". Step 3 — Provision clicked, `POST /api/v1/instances/provision` 201, instance `5ee13c38-…` provisioning.
- Product gaps found on the way (file after the test): (a) GitHub form names no PAT permissions and no link — founder had to ask; (b) `GitHubValidator.Validate` checks `GET /user` only, so a zero-permission token passes "Validate & connect"; (c) the org appears as its display name "QuantFlow" in GitHub's Resource-owner list, not `qf-studio` — worth one line of help text; (d) `billing/allowance.usage.key_source` reads `own` with no own key saved and no snapshot yet (`as_of: null`) — check whether it flips to `pool` after provision.
- 10-07 ~10:45Z: **BLOCKED.** Provision loops 1/min: reconciler log shows `context canceled` on every stage (`create_volume`, `ensure_tenant_resources`, once `run_instances` with the IAM-propagation 400). Root cause: PR#321's `runTickWithDeadline` passes `tickCtx` down to `dispatchLong`, so the provision goroutine is canceled the moment the ~1 s tick body returns; and `Provisioner.fail` settles the row on that same canceled ctx, so the row never flips to terminated and is re-claimed every tick. Filed [console#367](https://github.com/qf-studio/pilot-console/issues/367). The `TickTimeout` doc comment promised the opposite — comment-vs-code drift, same class as mem false-success.
- Operator cleanup after #367 deploys (Nelya or founder, needs `ec2:DeleteVolume`): orphaned `vol-09960b931f44e2f68` (100 GiB, eu-central-1a, created 10:32:56Z). pool-01 is held by the looping row (`platform_keys_free=4`); it frees when the row settles. Do NOT terminate the org from the UI (retires pool-01 for good).
- Nelya's 09-25 box (`i-0289111000cebcee9`, org `425f9f99…`) was launched 09-25 07:14Z, the day after #321 merged — whether it was provisioned before or after the #321 image reached prod is unverified; either way the defect was invisible until today.
