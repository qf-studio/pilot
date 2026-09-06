# TASK-495: Pilot Fleet TO-BE alignment — Nelya's control-plane transfer (CloudFormation, ECS console, central auth)

**Status**: 🚀 Code asks DISPATCHED 2026-09-06 (pilot-console #259–#264, pilot-cloud-infra user-data fix); infra build on Nelya's side pending
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
| Q4/Q8 consolectl in image + `prod-X.Y.Z` release → GHCR | [#263](https://github.com/qf-studio/pilot-console/issues/263) |
| Q6/Q7 + SES region (later, batched) | [#264](https://github.com/qf-studio/pilot-console/issues/264) |
| Q4 user-data `consolectl run` | [pilot-cloud-infra#49](https://github.com/qf-studio/pilot-cloud-infra/issues/49) |

## Operator / founder items

- **Domain pick still parked** (Q9, phase 2 blocked on it).
- After #259–#263 merge: post the ECS task-definition env manifest to Nelya (promised on Slack).
- Review Nelya's CloudFormation templates when she shares them (sequence step 2).
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
