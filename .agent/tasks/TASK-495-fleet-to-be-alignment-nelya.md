# TASK-495: Pilot Fleet TO-BE alignment — Nelya's control-plane transfer (CloudFormation, ECS console, central auth)

**Status**: 🟢 Code asks SHIPPED + REVIEWED 2026-09-06 (pilot-console PR#265–#269, cloud-infra PR#50 merged same day; #263 release workflow + #270 role-converge follow-up in flight); infra build on Nelya's side pending
**Created**: 2026-09-06
**Assignee**: Pilot (code) · Nelya (stacks) · founder (decisions)

---

## Context

Nelya's artifact "Pilot Fleet TO-BE" (Slack `#infrastructure` msg `1788706191.406399`, 2026-09-06 16:49): the Pilot SaaS control plane moves into her infrastructure project as CloudFormation stacks (naming, tagging, KMS `alias/pilot-fleet`, least privilege, her deploy pipelines), the same way website/docs/auth went. Our CDK stacks (`pilot-cloud-infra`) stay until hers run, then retire.

**Founder decisions 2026-09-06:** (1) console runtime = **ECS** (API ×2 with `PILOT_CONSOLE_FLEET_RECONCILE=false` + reconciler ×1 via `consolectl run`); (2) **accept** her CloudFormation replacing CDK, with our review of templates; (3) **founder box stays** (runs the Pilot daemon; move = own project later); test tenant (S3-exit fixture, t3.large since 08-19) re-created in the new VPC; (4) **Q8**: her pipeline builds+deploys the console image from `prod-X.Y.Z` tags; tenant tarball + golden AMI stay ours.

**Her "Decided" items, accepted:** central auth `auth.quantflow.studio` (no second auth-service, no Redis, no JWT key on the console) · one SG per role, tenants trust the console SG only · new VPC `pilot-fleet` 10.50.0.0/16 (10.30/16 is pointer's).

## Observations verified (nav-research 2026-09-06, pilot-console origin/main)

| # | Claim | Verdict |
|---|---|---|
| 1 | Tenant roles read other tenants' `/tenants/*` params | **CONFIRMED** — `AmazonSSMManagedInstanceCore` (`internal/fleet/tenantres.go:30`) grants `ssm:GetParameter*` on `*`, additive over the org-scoped inline policy (373-381) |
| 2 | Volumes unencrypted | CONFIRMED — `internal/fleet/provisioner.go:454-465` no `Encrypted`/`KmsKeyId`; no root `BlockDeviceMappings` (504-524) |
| 3 | user-data runs `consolectl reconcile --loop` (nonexistent) | CONFIRMED — cloud-infra `userdata.sh.tmpl:119`; real entrypoint `cmd/consolectl/run.go` |
| 4 | Dockerfile ships only pilot-console | CONFIRMED |
| 5 | API+reconciler one process, no leader election | CONFIRMED; `PILOT_CONSOLE_FLEET_RECONCILE=false` disables cleanly → ECS split feasible today |
| 6 | ValidateToken per request, no cache, no custom CA | CONFIRMED — `internal/bff/middleware.go:70-99`; `internal/authclient/client.go:70-78`; only `_INSECURE` exists |
| 7 | Sessions plaintext, RLS dormant | CONFIRMED (documented; TASK-487) |
| 8 | SSM per secret per tenant per loop | PARTIAL — one `GetParametersByPath` per config push on drift (`configpush.go:358-393`), uncached across retries |
| 9 | SES ignores fleet region | CONFIRMED — `internal/email/ses.go:37-42` |
| 10 | No tags / release workflow | CONFIRMED |
| 11 | IdleWindow not env-wired | CONFIRMED — `reconciler.go:117-120` |

## Dispatched (all `pilot` + `no-decompose`, order-independent by design — see pitfall `pilot-decomposes-parent-issues-one-pr-instructions-not-honored`)

| Ask | Issue |
|---|---|
| Q2b encrypted volumes + root, `PILOT_CONSOLE_FLEET_VOLUME_KMS_KEY_ARN` | [pilot-console#259](https://github.com/qf-studio/pilot-console/issues/259) |
| Q2a inline SSM-agent statements + `PILOT_CONSOLE_FLEET_TENANT_ROLE_BOUNDARY_ARN` | [#260](https://github.com/qf-studio/pilot-console/issues/260) |
| Q2c tags from env (`Environment`, `Relation`, `Project`) | [#261](https://github.com/qf-studio/pilot-console/issues/261) |
| Q1 validation cache + gRPC CA/server-name | [#262](https://github.com/qf-studio/pilot-console/issues/262) |
| Q4/Q8 consolectl in image + `prod-X.Y.Z` release → GHCR | [#263](https://github.com/qf-studio/pilot-console/issues/263) → **PR#273 merged 09-06 23:20** (3rd run, after 2 false no-ops = pilot#5342); review APPROVE-w-notes → [#274](https://github.com/qf-studio/pilot-console/issues/274): **ECS reconciler task must use `entryPoint: ["/consolectl"], command: ["run"]`** (exec-form ENTRYPOINT; a command override silently starts a 3rd API server) · `latest`/dispatch guards · GHCR package is private on first push |
| Q6/Q7 + SES region (later, batched) | [#264](https://github.com/qf-studio/pilot-console/issues/264) |
| Q4 user-data `consolectl run` | [pilot-cloud-infra#49](https://github.com/qf-studio/pilot-cloud-infra/issues/49) |

## Post-merge reviews (2026-09-06, verdicts on the PRs)

| PR | Verdict | Follow-up |
|---|---|---|
| pilot-console #265 encryption | APPROVE-w-notes (root device name is a constant tied to the AL2023 golden AMI) | — |
| pilot-console #266 SSM policy | **REQUEST-CHANGES** — converge only runs on provision/replace; pre-#266 tenants keep `AmazonSSMManagedInstanceCore` until replaced; live SSM check not recorded | [#270](https://github.com/qf-studio/pilot-console/issues/270) (pilot) |
| pilot-console #267 tags | APPROVE (convergeTags has no diff guard; fine <100 tenants) | — |
| pilot-console #268 auth cache + TLS opts | APPROVE | — |
| pilot-console #269 idle/SES/preflight | APPROVE-w-notes (idle window inert: sleep adapters nil in both entrypoints) | — |
| cloud-infra #50 user-data | APPROVE | — |
| auth-service #509 per-app aud | APPROVE-w-notes — **refresh path drops per-client aud** (breaks on first refresh once enforcement is on) | [auth-service#512](https://github.com/qf-studio/auth-service/issues/512) (pilot, before any `JWT_AUDIENCE_ENFORCE=true`) |
| auth-service #510 TLS_ENABLED removal | APPROVE-w-notes | [auth-service#513](https://github.com/qf-studio/auth-service/issues/513) reminder (no pilot label) |
| pilot-console #271 role converge on tick (follow-up to #266) | APPROVE-w-notes — live evidence impossible until first AWS boot; no backoff for a persistently failing org | → first-deploy checklist below |
| auth-service #514 DPoP scheme, #515 refresh aud | reviewed by the auth-service agent (founder's call 09-06) | operator: set `TRUSTED_PROXY_CIDRS` SSM param (Nelya) |

## First-deploy validation checklist (add to the runbook 3-step validation)

- `aws iam list-attached-role-policies --role-name <tenant role>` on the re-created test tenant → no managed policy; SSM port-forward to the tenant still opens (proves #266/#271 narrowed policy).
- Tenant volume + root device show `Encrypted=true` with `alias/pilot-fleet` (#265).
- Tags `Environment=pilot-fleet`, `Relation`, `Project` on instance/volume/SG/role (#267).
- Console → central auth: a session validates once per TTL (cache hit metric), gRPC dial with `PILOT_CONSOLE_AUTH_GRPC_CA_FILE`/`_SERVER_NAME` (#268).
- ECS: reconciler task = `entryPoint: ["/consolectl"], command: ["run"]` (NOT a command override — exec-form ENTRYPOINT); API tasks `entryPoint: ["/pilot-console"]` with `PILOT_CONSOLE_FLEET_RECONCILE=false` (#263/#274).
- GHCR package `pilot-console` visibility/pull permission set once before the first ECS pull (created private).

## Late 09-06 / 09-07 additions

- **pilot-console#45 ready-gate decoupling** (blocked since 07-24 on headers) re-specced 22:00 → PR#272 merged 23:13 → review APPROVE-w-notes (transient github:false flips the connection to `error`; consider a separate health field).
- **pilot-console#274** (pilot): README ECS contract wrong for the exec-form ENTRYPOINT + `latest`/dispatch guards. **#275** (pilot): `consolectl run` drops `TENANT_ROLE_BOUNDARY_ARN`, `IDLE_WINDOW`, `SECRETS_DRIVER` — boundary not applied on ECS until it merges.
- **ECS env manifest sent to Nelya** 09-07 00:27Z (TO-BE thread): task shapes, secrets split, AWS values, task-role IAM, the two caveats.
- **pilot#5342 executor false no-op**: PR#5345 pre-merge review **REQUEST-CHANGES** → drafted; revision [#5346](https://github.com/qf-studio/pilot/issues/5346) — first two runs classified *epic* (30 min) and timed out → 09-07 re-armed with `no-decompose` + step order in body. **pilot#5344** classifier OAuth 401 → PR#5347 merged 00:46.
- **09-07 11:29 — #5348 → PR#5349 merged** (SHA recorded+parsed, worktree from SHA/live branch, escalation when unfetchable = items 1–3 ✅); review REQUEST-CHANGES → [#5351](https://github.com/qf-studio/pilot/issues/5351) for the own-close marker + evidence-gated superseded. [#5350](https://github.com/qf-studio/pilot/issues/5350): decomposer ignores `no-decompose`, TUI paints subtasks red.
- **09-07 10:xx — FALSE SUCCESS caught:** pilot-console #275 (consolectl wiring: boundary/idle-window/secrets-driver) never landed — PR#276 failed on an unrelated flake, the CI-fix run rebuilt `pilot/GH-275` from main, PR#278 fixed only the flake, #277 done, #275 superseded. **Reopened #275 (pilot) with a cherry-pick note for the dangling commit 597710bc**; flow bug filed on pilot (see README). Until #275 lands the ECS reconciler applies NO permissions boundary. PR#279 (#274) APPROVE. **PR#5345 un-drafted per founder (merge partial fix; #5346 = follow-up on main).**
- **09-07 15:35–15:50Z — queue check ("autopilot got conflicts?" — no):** auth-service **v0.73.0 released 14:02Z, Nelya's deploy green 14:32Z, live healthy** → `TRUSTED_PROXY_CIDRS=10.50.0.0/16` posted to her. studio-sdk **v0.38.1 tagged 14:01Z** (sdk#140 → PR#141 APPROVE-w-notes; URL branch ignores owner/repo) → pin bump [#5358](https://github.com/qf-studio/pilot/issues/5358). pilot v2.273.1 tagged 14:15Z, box hot-upgraded 14:19Z. **#5353 was HELD by the base-presence gate 13:29→15:34Z (18 holds)** on my own backticked `internal/executor/store.go` (real path `internal/memory/store.go`) — body fixed, running 15:39Z; #5354/#5355 queued behind it. The 12:52Z "stalls" = boot reconciliation after the hot upgrade; board prints `created_at` (learning `board-stalled-timestamp-is-created-at-after-self-upgrade-reconciliation`). **#5351/PR#5356**: run 1 shipped the PR but was recorded `no_op` (merge-base unresolvable → [#5359](https://github.com/qf-studio/pilot/issues/5359)); one lint `unused` finding → **CI-fix size guard refused a fix issue → `pilot-needs-human`, no comment/alert** ([#5360](https://github.com/qf-studio/pilot/issues/5360); pitfall `ci-fix-size-guard-holds-own-pr-on-one-lint-annotation`). Recovery: pre-merge review of PR#5356 running → manual revision issue with `autopilot-meta` footer.
- **09-07 12:55Z — #280 + sdk#138 shipped and reviewed same hour.** pilot-console **PR#281 APPROVE-w-notes, delivery VERIFIED on origin/main** (`wiring.go:39` single helper; boundary/idle-window/secrets-driver reach `consolectl run`; cherry-pick of 597710bc byte-identical; 5 green check-runs) → **first `prod-*` tag can be cut (founder, org-admin tag rule)**. studio-sdk **PR#139 APPROVE-w-defects: does NOT prevent the incident** — bare `#n` regex accepts the CI-fix template's `Original Issue: #275` line → follow-up [studio-sdk#140](https://github.com/qf-studio/studio-sdk/issues/140) (closing-keyword-only + real-template fixture); **no sdk tag/pin bump until #140 lands**.
- **09-07 12:40Z — owed reviews posted:** PR#5343 (#5272 stalled re-arm) **APPROVE-w-defects** → follow-up [#5353](https://github.com/qf-studio/pilot/issues/5353) (`escalateStalledTask` re-stamps `completed_at` on the alreadyStalled path, so a poller pickup before the sweep hides the re-arm evidence forever; claim-lost log is Info not WARN; `gh-5272.md` to archive). PR#5352 (#5350) **APPROVE-w-defects** → follow-up [#5354](https://github.com/qf-studio/pilot/issues/5354) (running in-process subtasks still flipped to failed by the dead-owner sweep — no executions row, ID not in live set; widen acceptance-header pattern). **#5350's "ignores no-decompose" claim was wrong:** #5348 was picked with `[bug pilot]`; the label arrived 7 min later. Rule: label `no-decompose` before/with `pilot`, never after. Pitfall memory corrected.
- **09-07 12:50Z — #275 UNPICKABLE, refiled as [pilot-console#280](https://github.com/qf-studio/pilot-console/issues/280) (picked 12:53Z).** Box log 10:35:41Z: `github-sdk-poller` "Merged PR found via branch lookup" `pilot/GH-275` → "marking as done" — PR#278 (the #277 CI-fix) merged on that head branch, so every re-arm was reverted in one poll cycle. Upstream defect filed [studio-sdk#138](https://github.com/qf-studio/studio-sdk/issues/138) (pilot-labeled; pin bump on pilot after release). Pitfall memory `poller-branch-lookup-marks-issue-done-from-another-issues-pr`. "Needed from you" list posted to Nelya (PAT vs Actions access · CFN templates · SES identity · NLB ALPN · compression/CSP · #447).
- **09-07 12:37Z — Nelya's five questions answered in the TO-BE thread** (founder decisions: sender `console@quantflow.studio`; **tenant binary = golden AMI only, `PILOT_CONSOLE_FLEET_BINARY_S3_URI` unset, no tenant S3/KMS grant, her `pilot-fleet-s3-app-data` not used for tenants**). Facts posted: GHCR package not created until the first `prod-*` tag (none exist yet; PAT must be classic `read:packages`, or grant `pilot-fleet` repo Actions access); gRPC addr unset ⇒ API boots, BFF routes 503 (`main.go:705`); TLS via NLB TLS listener + `_SERVER_NAME` + ALPN `HTTP2Preferred`; #275 unmerged (daemon re-stamped `pilot-done` 10:35Z after strip at 10:31Z — #5351 class), first prod tag will carry it; tarball upload has never had a workflow (manual `aws s3 cp --sse aws:kms` from the founder box; key `releases/vX.Y.Z/pilot-linux-amd64.tar.gz`). Still owed: `TRUSTED_PROXY_CIDRS` key after the 16:00 tag; her answer PAT-vs-Actions-access. Her 09-06 18:43 CEST file `auth-service-findings-2026-09-06.md` NOT yet read (Slack attachment).
- **09-07 morning:** pilot-console #275 → PR#276 failed CI → fix #277 (old-binary body carried `Depends on: #275`; #275 closed to unblock). #274 parked on a backticked `/entrypoint.sh` → reworded, unparked. **Box rebuilt from main + restarted 09:18Z** (v2.273.0-23-ge054b01c: deadlock fix, re-adoption cutoff, classifier fix, docs sync all live); receipts digest scheduler active.

## Operator / founder items

- **Domain pick still parked** (Q9, phase 2 blocked on it).
- After #259–#263 merge: post the ECS task-definition env manifest to Nelya (promised on Slack).
- Review Nelya's CloudFormation templates when she shares them (sequence step 2).
- Cut the first pilot-console `prod-*` tag once #280 (ex-#275) lands; post the tag to Nelya; then GHCR pull access (her choice: PAT or Actions access).
- Read Nelya's `auth-service-findings-2026-09-06.md` (Slack file, TO-BE channel 09-06 18:43 CEST) — not triaged.
- Founder box move: separate future task; NOT part of cut-over.

## Out of Scope

- Sessions plaintext / RLS activation (TASK-487 documented; security-review item).
- Local JWT validation instead of gRPC (cache is the agreed mitigation).
- Golden AMI / tenant tarball pipeline changes.

## Refs

- Slack thread: `#infrastructure` C0BV37L87C1 `1788706191.406399` (her post) · our reply 2026-09-06
- TASK-405 (SaaS program), TASK-490 (S4 minimal in-VPC console — superseded on the infra side by this transfer; code decisions stand)
- pilot-cloud-infra user-data fix: [infra#49](https://github.com/qf-studio/pilot-cloud-infra/issues/49)

---

**Last Updated**: 2026-09-06
