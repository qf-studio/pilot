# TASK-499: Golden AMI refresh (pilot 2.276.1) → first fleet tenant → Docs real-tree proof

**Status**: 🟢 **L2 DONE 2026-09-23 11:12Z — `ami-01ecd7cefa1e6f316` (pilot 2.276.1, CC 2.1.220, gh 2.63.2), run 35851941616 green; both SSM params updated. L3 step 1 DONE 13:07Z: reconciler on the new AMI (run 35863755066, task def rev 5, `tenant_factory=false`). Next: founder go → `tenant_factory=true` → first tenant.** History: L1 MERGED 2026-09-23 (infra#10 → PR#11, squash `118182ef`, review APPROVE-w-notes → follow-up [infra#12](https://github.com/qf-studio/aws-infrastructure-pilot/issues/12)); **L2 bake attempt 1 (run 35849443228) FAILED at staging: `mgmt-infra-admin` has no `s3:PutObject` on `pilot-s3-agent-data/binaries/pilot/*`** (download + sha256 OK). **Not IAM — the bucket policy requires SSE-KMS**; Nelya fixed the staging line on main (`7171c35`, `--sse aws:kms --sse-kms-key-id alias/pilot`). **Bake attempt 2 = run 35851941616** (from main). infra#12 → PR#13 merged `b4f6df9d` (staging after the guard, SSE kept, hard step per Nelya; manual rebase again). Gate to the first customer after login passed (TASK-495).
**Created**: 2026-09-22
**Owner**: founder session (bake dispatch + console redeploy) · Nelya (SSM param + S3 staging or workflow tweak)
**Parent**: TASK-405 · **Follows**: TASK-495 (login gate open 09-22)

## Why

Fleet tenants launch from the golden AMI. The current one (`ami-01ed3bb9600200ce4`, `pilot-agent-20260816-180951`) bakes **pilot 2.259.3**; the box runs **2.276.1** (17 releases newer: evidence gate, argv-exec, auto-preserve, hosted-retry fixes). Provisioning a customer on 2.259.3 is a regression the first ticket would expose. The console DB is empty, so the first tenant is also the end-to-end proof the Docs page never got (TASK-466 / infra#51 lineage).

## Facts (verified 2026-09-22)

- **Bake pipeline = Nelya's repo** `qf-studio/aws-infrastructure-pilot` (we have admin): `.github/workflows/build-ami.yml` (`workflow_dispatch`, runner `aws-infra-admin` = mgmt), `packer/pilot-agent.pkr.hcl`, `scripts/validate-golden-ami.sh` (throwaway instance asserts `pilot version`, `pilot doctor`, `gh --version`, pinned Claude Code). Last successful run 31963758882 (08-16, ours).
- **Inputs**: `claude_code_version` (default 2.1.220) · `gh_cli_version` (2.63.2) · `pilot_binary_s3_uri` · `pilot_version`. The binary must be pre-staged at `s3://pilot-s3-agent-data/binaries/pilot/v<ver>/pilot-linux-amd64` (v2.259.3 is there; **v2.276.1 is not**; `release.yml` does not publish to S3; `user/aleks` has no write on that bucket).
- **Output param mismatch**: the bake writes `/pilot/GOLDEN_AMI_ID` (old path). The console deploy workflow reads **`/pilot-fleet/console/GOLDEN_AMI_ID`** and passes `AmiId` to the reconciler stack (env `PILOT_CONSOLE_FLEET_AMI_ID`). Nelya set the fleet param by hand today 14:41 CEST. Someone must copy the new id or the bake must write both.
- Box toolchain for reference: Claude Code **2.1.104** (older than the AMI pin — box drift, separate hygiene item), gh 2.96.0, node 22.22.2, go 1.25.8. Tenants on 2.259.3 + CC 2.1.220 passed the S3 exit (08-18), so 2.1.220 stays the pin.
- Console side needs nothing new: bootstrap (`internal/fleet/bootstrap.sh.tmpl`) uses the AMI-baked `/usr/local/bin/pilot` when `PILOT_CONSOLE_FLEET_BINARY_S3_URI` is unset (founder decision 09-07).

## Legs

### L1 — stage the binary + make the bake fleet-aware — 🚀 [infra#10](https://github.com/qf-studio/aws-infrastructure-pilot/issues/10) dispatched 2026-09-23 (releases are public → plain curl + sha256 verify; her `deploy-pilot-fleet.yml` already copies `/pilot/GOLDEN_AMI_ID` → fleet param on fleet ECS deploys, so the bake writing both is idempotent with hers)
1. `build-ami.yml`: make `pilot_binary_s3_uri` optional; when empty, **download `pilot-linux-amd64.tar.gz` from the GitHub release `v${pilot_version}`**, verify against `checksums.txt`, extract to `/tmp/pilot-binary`, and also copy it to `s3://pilot-s3-agent-data/binaries/pilot/v${pilot_version}/pilot-linux-amd64` for the record (runner already has bucket access).
2. "Store AMI ID" step: write **both** `/pilot/GOLDEN_AMI_ID` and `/pilot-fleet/console/GOLDEN_AMI_ID` (needs `ssm:PutParameter` on the second path for the `aws-infra-admin` runner role — Nelya confirms/grants).
3. Validation script unchanged. PR reviewed by Nelya (her repo, her runner role).

### L2 — bake + verify
1. Dispatch `Build Pilot Agent AMI` with `pilot_version=2.276.1`, `claude_code_version=2.1.220`, `gh_cli_version=2.63.2`, S3 URI empty (L1) or the staged path.
2. Green run ⇒ new `ami-…` tagged `PilotVersion=2.276.1`; both SSM params updated; throwaway validation passed (`pilot version` = 2.276.1, `pilot doctor` OK).
3. Cross-check from the laptop: `aws ec2 describe-images` tags; `aws ssm get-parameters` on both paths.

### L3 — first fleet tenant (console)
1. Re-run `Deploy QuantFlow AWS` (pilot-console) `image_tag=prod-0.1.0`, `tenant_factory=false` → reconciler stack picks the new `AmiId` from the fleet param (workflow line 138–150). Confirm `PILOT_CONSOLE_FLEET_AMI_ID` on the running task.
2. Re-run with **`tenant_factory=true`** (per Nelya's handover 3-step validation; pilot-console#275/#280 is in `prod-0.1.0`).
3. In the console: onboard our dogfood org → provision → instance launches from the new AMI → `/ready` green → **Docs page renders the tenant's real `.agent` tree** (TASK-495 checklist item). Record instance id, AMI id, time-to-ready.
4. Nelya verifies from the AWS side (tenant tags/encryption/no managed policy — TASK-495 checklist).

## Acceptance
- New AMI exists, `PilotVersion=2.276.1`, validated by the throwaway check; both SSM params point at it.
- Reconciler running with the new `AmiId`; `tenant_factory=true` deploy green.
- One fleet tenant RUNNING from the new AMI; Docs page shows its real tree through `console.pilotcloud.dev`.
- `internal/config/config.go` AMI comment updated (follow-up console issue, docs-only).

## Log
- **09-23 10:20Z** Pilot's PR#11 branched from an **August** base: the box clone of this repo had never been fetched since 08-03 and the pre-branch `git fetch` lost a ref-lock race; executor warned and fell back to stale `origin/main` → 100 commits behind, conflict on `scripts/validate-golden-ami.sh`, autopilot `needs-manual-rebase`. Rebased by hand in a temp clone (kept main's `go version` assert, no `pilot doctor`), merged 10:32Z. Executor defect filed on pilot (fetch failure must use FETCH_HEAD / retry / fail closed). Pilot picked infra#12 at 10:30Z from pre-merge main → expect one more rebase.
- **09-23 10:33Z** bake run 35849443228: release download + checksum verified, S3 staging **AccessDenied** — I blamed IAM; Nelya: `mgmt-infra-admin` is AdministratorAccess, the deny is the **bucket policy requiring SSE-KMS** on `pilot-s3-agent-data`. Fixed on main `7171c35`. Lesson: an AccessDenied on S3 PutObject from an admin role = bucket policy condition (encryption/TLS), check `aws s3api get-bucket-policy` before asking for grants.
- **09-23 10:56Z** bake attempt 2 run 35851941616 dispatched from main. PR#13 (infra#12) rebased by hand (SSE flags carried into the moved step), reviewed APPROVE, merged `b4f6df9d`. pilot#5449 → PR#5450 reviewed APPROVE pending tests.

- **09-23 11:12Z** bake attempt 2 **GREEN**: `ami-01ecd7cefa1e6f316`; throwaway validation `pilot (pinned 2.276.1)` OK; `/pilot/GOLDEN_AMI_ID` + `/pilot-fleet/console/GOLDEN_AMI_ID` written by the bake. Console redeploy dispatched for the reconciler `AmiId` pickup.

- **09-23 11:17Z** console redeploy 35853276663 for the AMI pickup: console-api OK, **reconciler stack UPDATE_ROLLBACK_COMPLETE** — ECS rejects `AvailabilityZoneRebalancing: ENABLED` (Nelya's 1ebdd93, 09-22) because the reconciler's placement is `binpack memory` only (console-api has `spread attribute:ecs.availability-zone` first, so it passed). Service still 1/1 on the old task def / old AMI; nothing down. Asked Nelya to fix the reconciler template (spread first, or rebalancing off for the singleton — recommended). Re-run after.
- **09-23 13:07Z** Nelya picked (a): rebalancing DISABLED on console-reconciler, template on S3 since 13:15Z (answered 12:54Z after a reminder). Re-run 35863755066 (`prod-0.1.0`, `tenant_factory=false`) **green**: reconciler stack `UPDATE_COMPLETE`, service steady 1/1 on task def rev 5; verified `PILOT_CONSOLE_FLEET_AMI_ID=ami-01ecd7cefa1e6f316`, `PILOT_CONSOLE_FLEET_TENANT_FACTORY=false`, image `prod-0.1.0`. Posted in the AMI thread. **Blocked on founder go/no-go for `tenant_factory=true`** (creates EC2 capacity); old AMI stays registered until the first tenant is green.

## Risks / notes
- pilot 2.276.1 on AL2023 via the AMI's systemd unit: bootstrap sets `User=pilot` + `HOME` (pitfall `claude-cli-refuses-root-hosted-units`); `pilot doctor` in validation catches most drift.
- Bake runner is the **mgmt** runner, not the fleet deployer — different role; SSM write to `/pilot-fleet/*` is the one new grant.
- Keep the old AMI registered until the first tenant is green (rollback = put the old id back in the fleet param, redeploy).

## Refs
- Bake run 08-16: https://github.com/qf-studio/aws-infrastructure-pilot/actions/runs/31963758882
- Console deploy workflow: pilot-console `.github/workflows/deploy-quantflow-aws.yml` (reconciler step reads `/pilot-fleet/console/GOLDEN_AMI_ID`)
- Fleet design §AMI: `.agent/system/saas-fleet-design.md` (rolling upgrade = stop→swap→reattach, canary org first)
