# fix(controlplane): the in-VPC console can never proxy a tenant — instance role lacks tenant-secret read and ec2:DescribeInstances

**Status**: ⏸️ WITHDRAWN FROM QUEUE 2026-09-15 — dispatched as [infra#51](https://github.com/qf-studio/pilot-cloud-infra/issues/51) at 10:28Z, then pulled at 11:02Z on founder call. All labels removed from the issue and the queued execution row cancelled via `pilot task cancel` (never started; no worker, no worktree). Issue left OPEN with the full spec intact.

> **To re-run**: re-adding the `pilot` label is enough — per GH-5139 the poller re-arms a cancelled GH task only on a reopen/label *event dated after the cancel*, and a fresh label add produces exactly that. Do NOT hand-write `status='stalled'` (GH-4655).
**Created**: 2026-09-15
**Last Updated**: 2026-09-15
**Target repo**: qf-studio/pilot-cloud-infra
**Parent**: TASK-466 (console Docs page — read leg shipped 2026-08-19, never verifiable end-to-end)
**Follows**: qf-studio/pilot-cloud-infra#42 → PR (minimal in-VPC console mode), #44 (port-forward policy ARN fix)

## Context

The console Docs page (TASK-466) has been code-complete since 2026-08-19 and has never once
rendered a real `.agent` tree in a browser. Diagnosed 2026-09-15 against the live stack.

Every leg except one is proven working:

- Founder box daemon `GET :9091/api/v1/docs/tree` → **200**, real tree, counts 21/141/63/59.
- Tenant box (`i-0a3bf271d598196ca`) daemon `GET :9090/api/v1/docs/tree` with its gateway token
  → **200**, serves `/var/lib/pilot/repos/pilot-ship-test-js/.agent`.
- Tenant daemon listens on **9090**, matching `proxy.go`'s `pilotGatewayPort = 9090`.
- Tenant daemon rejects an unauthenticated request with 401 — auth is live, not dormant.
- Console proxy allowlists `docs/tree` and `docs/file` with query passthrough; the UI route,
  error ladder and fixtures are all correct.

The break is that **`pilot-console` has never run inside the VPC.** `internal/proxy/proxy.go`
resolves a tenant's *private* address and dials it directly:

```go
upstreamURL := fmt.Sprintf("http://%s:%d/api/v1/%s", ip, h.port, tail)  // 10.30.71.162:9090
```

A laptop has no route into the fleet VPC, so the only console that has ever existed cannot
complete this call. Minimal mode (#42/#44, 2026-08-31) exists precisely to close that gap —
it runs the same app instance in-VPC and reaches it via SSM port-forward. It has never been
deployed: `docs/CONTROLPLANE-DEPLOY.md`'s sign-off line still reads
``Last tested: `<date>` by `<operator>` ``.

The live `ControlPlaneStack` (last deployed **2026-08-07**, so it predates minimal mode)
contains only `RDS DBInstance · DBSubnetGroup · Secret · KMS Key · SecurityGroup · ALB`.
**There is no `AWS::EC2::Instance` in it.** Nothing runs `pilot-console` in AWS today.

**Deploying minimal mode alone will not fix the page**, which is why this is a code task and
not purely an operator runbook. The app instance role in
`internal/stacks/controlplane/instance.go` is missing two grants the proxy needs on its very
first request, and each failure reproduces a *generic* 502 that looks identical to the bug
already being chased:

1. **Tenant secrets are unreadable.** The role grants `ssm:GetParameter*` on
   `controlPlaneParameterResourceName = "controlplane/*"` only. The console reads the upstream
   bearer token from `/tenants/{org_id}/PILOT_GATEWAY_TOKEN` (`pilot-console`
   `internal/config/config.go`: `defaultPathPrefix = "/tenants"`), which that ARN does not
   cover. `proxy.go`'s `gatewayToken` → `GetGatewayToken` returns AccessDenied, and the handler
   answers **502 "upstream auth unavailable"** — byte-identical to the symptom on the laptop
   today, so this would read as "the fix didn't work" rather than as a new, different fault.
   Confirmed live: `/tenants/2a4bdc46-…/PILOT_GATEWAY_TOKEN` exists in SSM.

2. **The instance address cannot be resolved.** The role has no `ec2:DescribeInstances` at all.
   Immediately after the token, `proxy.go` calls `DescribeInstances` to turn the instance id
   into a private IP; on failure it answers **502 "resolve instance address failed"**.

No KMS grant is required: the tenant parameters are `SecureString` under **`alias/aws/ssm`**
(verified live), and `pilot-console`'s `SecretsConfig.KMSKeyID` is unset, so the AWS-managed
SSM key covers decryption for any principal already allowed `ssm:GetParameter*` in-account.
That stops being true the moment `PILOT_CONSOLE_SECRETS_KMS_KEY_ID` is set to a CMK.

## Task

**Grant the app instance role read access to tenant secrets.** Add an `ssm:GetParameter*`
statement covering `parameter/tenants/*`, alongside the existing `controlplane/*` statement
rather than widening it — the two paths have different lifetimes and different blast radii, and
keeping them separate keeps that legible. Follow the existing
`controlPlaneParameterResourceName` idiom: a named constant, an ARN built through
`Stack_Of(scope).FormatArn`, not a hand-spliced string.

**Grant `ec2:DescribeInstances`.** This action does not support resource-level permissions, so
the resource must be `*`; say so in a comment so a future reader does not mistake it for an
oversight and try to scope it. Keep it to `DescribeInstances` — the proxy needs nothing else.

**Both grants are conditional on nothing.** The instance exists only to run the control plane;
there is no mode in which it should hold one grant and not the other, and a partial grant
produces exactly the indistinguishable-502 confusion described above.

**Add tests that fail when a grant is missing.** Mirror the repo's existing synthesis-assertion
style (`internal/stacks/controlplane/*_test.go`): assert the rendered template's role policy
contains a statement allowing `ssm:GetParameter*` on a `tenants/*` ARN, and one allowing
`ec2:DescribeInstances`. Per the #40 lesson, each assertion needs to be able to fail for the
right reason — a test that merely counts statements, or that matches any `ssm:GetParameter*`
regardless of resource, would pass today against the broken role and is not acceptable.

**Record the KMS conditional** in `docs/CONTROLPLANE-DEPLOY.md`: tenant parameters currently
use `alias/aws/ssm`, so no key-policy grant is needed, and setting
`PILOT_CONSOLE_SECRETS_KMS_KEY_ID` to a customer-managed key makes an explicit `kms:Decrypt`
grant on the instance role a prerequisite. Leaving this implicit is how the next deploy breaks.

## Acceptance

- The app instance role allows `ssm:GetParameter*` on an ARN covering `parameter/tenants/*`,
  as a statement separate from the existing `controlplane/*` grant.
- The app instance role allows `ec2:DescribeInstances`, with a comment stating why the resource
  is `*`.
- A test fails if either grant is removed, and fails for the right reason — removing the
  `tenants/*` resource while leaving an unrelated `ssm:GetParameter*` statement in place must
  still fail.
- `cdk synth ControlPlaneStack` is green in both default and `PILOT_CONTROLPLANE_MINIMAL=true`
  modes; the two grants are present in both (the proxy path is identical either way).
- `docs/CONTROLPLANE-DEPLOY.md` states the KMS conditional.
- No change to the tenant security groups, the tenant role, the KMS key policies, or any
  isolation boundary asserted by the #38/#40 gate. This task widens the *control-plane* role
  only; if `cmd/tenant-boundary-check` disagrees, stop and report rather than relaxing it.

## Operator legs (NOT dispatched — founder work, in this order)

These follow the merged IAM leg. Full procedure in
`pilot-cloud-infra/docs/CONTROLPLANE-DEPLOY.md` § "Minimal mode (in-VPC console)".

1. **Publish the console binary to S3.** `pilot-console`'s `.github/workflows/release.yml`
   produces a `make package` tarball on a GitHub Release plus a GHCR image, and **nothing
   uploads to S3** — but `userdata.sh.tmpl` installs via
   `aws s3 cp {{.ConsoleBinaryS3URI}}`. There is no producer for
   `PILOT_CONTROLPLANE_CONSOLE_BINARY_S3_URI`. Bridge it by hand once, or file a follow-up to
   add the upload step to `release.yml`.
2. **Deploy.** `PILOT_ENV=staging PILOT_CONTROLPLANE_MINIMAL=true` plus the binary URI →
   `cdk deploy ControlPlaneStack`. Note this is the first deploy of this stack since 08-07 and
   the first ever to create the app instance; #22/#23 are the prior first-deploy rollback
   incidents.
3. **Place the console's env in SSM** under `/controlplane/pilot-console/*`: `DATABASE_URL`
   (from the stack's `ControlPlaneDatabaseSecretArn`) and **`PILOT_CONSOLE_SECRETS_DRIVER=ssm`**.
   The laptop container runs `postgres`, whose `local_secrets` table holds only
   `ANTHROPIC_API_KEY` and `GITHUB_TOKEN` — no `PILOT_GATEWAY_TOKEN` row. That mismatch is the
   present-day 502 and it will repeat in-VPC if this parameter is wrong.
4. **Attach `ControlPlanePortForwardPolicyArn` to an IAM user, not an SSO role.** The policy's
   `TerminateSession`/`ResumeSession` statement scopes to `session/${aws:username}-*`, which
   resolves only for IAM users; a federated principal gets `StartSession` and then cannot
   terminate its own session.
5. **Verify.** Port-forward 8090, run the SPA against it
   (`VITE_DEV_PROXY_TARGET=http://localhost:8090 npm run dev` from `pilot-console-ui`), open
   `/docs`.

## Expected on first success — neither of these is a bug

- **The tree will look nearly empty.** The only running tenant serves `pilot-ship-test-js`,
  whose `.agent` is 7 files with all four knowledge counts at **0**. That is a correct render.
- **The founder box's docs will not appear.** `i-0e0c1ca34e7b561f9` has no row in the console's
  `instances` table, so pilot's own `.agent` (21 patterns / 141 pitfalls / 63 learnings /
  59 decisions) is not reachable through the console at all. If surfacing *those* docs is the
  real goal, that is a separate task, not this one.

## Out of scope

- **The design's project picker.** `design/docs-v1.html` shows a project selector, but the
  daemon scopes docs to a single `SetDocsProjectPath(projectPath)` (one path, from
  `default_project`). Multi-project selection needs a daemon contract change first.
- **The Docs manage leg** (edit/create from console) — TASK-466 phase 3, still unscoped.
- **Whatever else the in-VPC console needs to be genuinely operable** (fleet reconciler
  permissions, provisioning grants). This task grants exactly what the read-only instance proxy
  needs and nothing more; other surfaces will surface their own gaps on first use.

## Follow-up candidate (not filed)

The secrets driver/store mismatch is **silent**: a `postgres` driver pointed at a store that has
no `PILOT_GATEWAY_TOKEN` row, and an `ssm` driver without the IAM grant, both surface as the
same generic 502 with no hint that the lookup went to the wrong place. This has now cost two
debug sessions (2026-08-19, 2026-09-15). A startup check or a named error class in
`pilot-console` would have collapsed both to a one-line diagnosis.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot-cloud-infra/issues/51

- Parent: `.agent/tasks/TASK-466-console-docs-page.md`
- Runbook: `pilot-cloud-infra/docs/CONTROLPLANE-DEPLOY.md` § Minimal mode (in-VPC console)
- Minimal mode: qf-studio/pilot-cloud-infra#42, #44
- Non-vacuous-test precedent: qf-studio/pilot-cloud-infra#38, #40
- Proxy source: `pilot-console/internal/proxy/proxy.go` (`gatewayToken`, `DescribeInstances`,
  `pilotGatewayPort = 9090`)
- Role source: `pilot-cloud-infra/internal/stacks/controlplane/instance.go`
  (`controlPlaneParameterResourceName`)
