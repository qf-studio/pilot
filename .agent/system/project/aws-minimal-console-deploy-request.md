# Deploy request: minimal in-VPC Pilot console (for Nelya, AWS infra owner)

**Date**: 2026-09-01 · **Requester**: Aleks · **Account**: 529088297614 · **Region**: eu-central-1
**Contract**: everything below is code on `qf-studio/pilot-cloud-infra` main (`b83586c`) — `cdk synth ControlPlaneStack` with the env vars in §5 produces the exact CloudFormation template for review. This doc is the human summary; the template is authoritative. Deploy via CDK or replicate by hand under your own rules — the synth output is the contract either way.

## 1. Purpose

The Pilot SaaS console must run *inside* the fleet VPC to reach tenant dashboards: every tenant security group accepts inbound :9090 exclusively from the control-plane SG (hard invariant, unchanged). A laptop-run console cannot reach that surface. "Minimal mode" deploys the console on the app instance the stack already defines, with **zero public ingress** — no ALB, no ACM/domain, no SES, no CloudFront. Operator access is an SSM port-forward under a policy scoped to one instance + one SSM document.

## 2. Current deployed state vs requested end-state

Deployed ControlPlaneStack today (last updated 2026-08-07): KMS key · control-plane SG · RDS Postgres db.t4g.small MultiAZ (+ generated secret) · **an empty internet-facing ALB** (no listener ever deployed; it holds an inline 0.0.0.0/0:443 rule on the *shared* control-plane SG — the SG every tenant trusts). No app instance exists.

| Change | Resource | Why |
|---|---|---|
| **REMOVE** | ALB (+ its internet-open 443 inline rule on the CP SG) | unused, ~$20/mo, and the 443 rule is our worst SG exposure |
| ADD | t3.small app instance (AL2023, IMDSv2 required, no SSH, no key pair, SSM-managed) in the control subnets; joins its own app SG + the control-plane SG | runs pilot-console (:8090, private) + auth-service (docker, host network) + the consolectl reconciler (systemd) |
| ADD | Ingress on control-plane SG: tcp/5432 from the app SG only | app ↔ RDS; today NO 5432 rule exists anywhere (latent bug — even full mode couldn't reach the DB) |
| ADD | Instance role: AmazonSSMManagedInstanceCore + ssm:GetParameter* on `controlplane/*` params + s3:GetObject on exactly one tarball object (URI you choose, §5) | boot pulls the console binary from S3; secrets from SSM SecureString at boot; deliberately NO Secrets Manager read |
| ADD | Customer-managed IAM policy "port-forward": ssm:StartSession on that one instance ARN + the account-less `AWS-StartPortForwardingSession` document ARN (same statement); Terminate/ResumeSession on `session/${aws:username}-*` | the only human access path; grants no shell (document-scoped), no other instance |
| ADD | CfnOutputs: instance id, policy ARN, DB secret ARN | operator handles |
| UNCHANGED | RDS, KMS, control-plane SG itself, all tenant-side stacks | no data risk; rollback = redeploy with the flag unset |

## 3. Security posture (review points)

- **No public ingress anywhere** after this deploy — the stack's synth contains zero 0.0.0.0/0 rules (test-enforced: the suite asserts no internet-open ingress in minimal mode, and CI runs a real synth).
- IMDSv2 (`HttpTokens: required`) on the instance — repo-wide invariant, test-enforced.
- Access = SSM port-forward only, via the tiny policy above. Attach it to whatever principal you're comfortable with; the requirement is only that **Aleks's `aleks` IAM user (or an equivalent you issue) can port-forward to this one instance's :8090**. Note the session-scoping uses aws:username, which resolves only for IAM users (not SSO roles).
- Secrets: DB URL placed by an operator as SSM SecureString under `/controlplane/pilot-console/DATABASE_URL` + `/controlplane/auth-service/DATABASE_URL` (read from the RDS-generated secret — instance can't read Secrets Manager by design). No secrets in user-data or env vars of the stack.
- The console tarball: two static linux/amd64 Go binaries (pilot-console, consolectl), built by `make package` in `qf-studio/pilot-console` at `7f26a30` — reproducible; build it yourself or take Aleks's artifact. Bucket should be private (block-public-access); the instance role gets GetObject on the one object only.

## 4. How to review

```bash
# pilot-cloud-infra @ main (b83586c)
export AWS_REGION=eu-central-1 PILOT_CONTROLPLANE_MINIMAL=true \
  PILOT_CONTROLPLANE_CONSOLE_BINARY_S3_URI=s3://<your-bucket>/<ver>/pilot-console.tar.gz
npx aws-cdk synth ControlPlaneStack     # full template; diff against live stack with `npx aws-cdk diff`
```
Relevant source: `internal/stacks/controlplane/` (stack.go, instance.go, portforward.go, userdata.sh.tmpl) · runbook `docs/CONTROLPLANE-DEPLOY.md` § "Minimal mode (in-VPC console)" (env vars, DB-URL step, validation checklist, rollback). All shipped via reviewed PRs #42/#44 (post-merge review verdicts on the PRs).

## 5. Inputs you own (nothing hardcodes them)

- **Bucket + object key** for the tarball → `PILOT_CONTROLPLANE_CONSOLE_BINARY_S3_URI`
- Instance sizing/AMI if you want to override defaults (t3.small, latest AL2023 via SSM param)
- Deploy mechanism: `cdk deploy` recommended (drift-free, repeatable); change-set review first if you prefer
- The principal that receives the port-forward policy

## 6. What we need back

1. Confirmation deployed + the two outputs (`ControlPlanePortForwardInstanceId`, `ControlPlanePortForwardPolicyArn`)
2. DB URL params placed (or tell us and we'll walk it together)
3. Port-forward capability granted to `aleks` (or your equivalent)

From there Aleks runs the runbook's 3-step validation (ready endpoint through the forward · DB round-trip · tenant :9090 probe from the instance — that last one needs a shell-session grant the port-forward policy deliberately lacks; happy to do it together or you run the one curl).

## 7. Out of scope / explicitly NOT requested

No domain, no ACM, no SES, no CloudFront, no IAM users created by the stack, no changes to tenant stacks or the founder box, no Secrets Manager grants to instances. The full public stack (ALB path) remains available later by redeploying without the flag.

## 8. Supplement (2026-09-01, sent as DM #3)

**Repo/config map**: pilot-cloud-infra = the CDK app, 4 stacks (FleetVpc → ControlPlane [only one changing] → TenantBase; DataLifecycle standalone; others already live) · pilot-console = tarball source, runtime env = every SSM param under /controlplane/pilot-console/* · auth-service = docker container on the instance + redis:7-alpine sidecar (public Docker Hub), env from /controlplane/auth-service/* · pilot-console-ui = local-only SPA · daemon/founder/tenant boxes untouched.

**Finding 1 (boot blocker)**: default auth-service image ghcr.io/qf-studio/auth-service:latest is PRIVATE (anon pull 403, verified) and user-data does no registry login → auth-service units fail on first boot. Fix = [infra#45](https://github.com/qf-studio/pilot-cloud-infra/issues/45) (ECR pull path, pilot-labeled). **Deploy should wait for #45.**

**Finding 2 (owed to Nelya)**: exact env-key list per service (pilot-console, auth-service) before she places SSM params — enumerate from each repo's config loading. `:latest` tag flagged for pinning.
