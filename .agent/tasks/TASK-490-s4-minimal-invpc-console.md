# feat(controlplane): S4 unblock — minimal in-VPC console mode (no ALB/ACM/SES/SPA), SSM port-forward access

🚀 ACTIVE 2026-08-31 — founder decision recorded (memory `s4-unblock-minimal-invpc-ec2-console`): the S4 exit week runs on a minimal EC2 console inside the fleet VPC control-plane SG; domain purchase stays deferred. Leg 1 dispatching to pilot-cloud-infra.

## Problem

S4's exit clause is "operated dashboard-only for a full week across ≥2 tracker types on one board; approvals survive instance restart". The dashboard proxy is a direct HTTP call from console-api to the tenant's private IP:9090, allowed only from the control-plane SG — a laptop console times out (memories `console-ssm-paths-work-locally-proxy-does-not`, `s4-dashboard-only-clause-blocks-local-console`). The full staging ControlPlaneStack needs domain/ACM/SES (deferred). Cheapest genuine unblock: run console-api on the control-plane app instance that the stack already provisions, reach it from the laptop via SSM port-forward.

## Research facts (nav-research 2026-08-31, origin/main = `19aeede`)

- App instance t3.small EXISTS (`internal/stacks/controlplane/instance.go`, NewControlPlaneAppInstance) and joins the shared control-plane SG at instance.go:200 — that membership is what the tenant SG's :9090 rule trusts (tenantbase tenant_security_group.go:32-37 references the ControlPlaneStack SG, NOT the dead fleetvpc placeholder export).
- ALB / listener+ACM / SES / SPA(CloudFront) are UNCONDITIONAL calls in NewControlPlaneStack (stack.go:152/181/189/192) — separable constructs, zero flags today. Tests pin their existence (stack_test/listener_test/ses_test/spa_test, main_test ALB assertion, instance_test hardcodes SecurityGroupIngress count = 3).
- **Latent bug: no 5432 ingress rule exists anywhere in the repo** — RDS sits in the control-plane SG but SG co-membership does not permit traffic; even the full stack's app instance cannot reach the DB today.
- SSM port-forward needs NO ingress rule (agent-mediated, on-instance localhost) — but **zero StartSession IAM exists in the repo** (greenfield; instance side ready: AmazonSSMManagedInstanceCore + SSM interface endpoints in fleetvpc).
- Console binary arrives via user-data from S3 (gated on ConsoleBinaryS3URI env), systemd unit, port 8090; secrets fetched at boot from SSM path /controlplane/pilot-console. Instance role has NO secretsmanager read — DB URL is an operator-placed SSM param (runbook gap).
- SPA is CloudFront-only, nothing serves it from the instance → minimal mode: run the UI locally against the forwarded port (no infra change).
- No reconciler exists in the infra repo — consolectl is a pilot-console binary; the nohup gap needs a systemd unit + the tarball to carry consolectl (console-repo packaging leg, see Legs).
- Dropping the ALB REMOVES the internet-open 443 inline rule CDK's open listener puts on the shared control-plane SG (acknowledged in instance_test.go:81-88) — minimal mode is a security improvement.
- ControlPlaneStack has never been deployed for real (CONTROLPLANE-DEPLOY.md sign-off blank); minimal mode is the credible first real deploy.
- No open infra issue overlaps (#35/#36 are isolation-harness scope; beware their stale file paths). Precedent to cite: closed #24 → PR#25 built the full ALB/SES/SPA path this narrows.

## Legs

| # | Repo | What | Status |
|---|------|------|--------|
| 1 | pilot-cloud-infra | Minimal mode flag (no ALB/ACM/SES/SPA), 5432 rule, port-forward IAM policy + outputs, reconciler systemd unit, runbook section | 🚀 dispatched → issue #41 |
| 1b | pilot-console | Package consolectl into the console release tarball (reconciler unit's binary source) | 📋 file after Leg 1 lands |
| 2 | pilot-console | Deprovision cascade: terminated tenants keep connections rows after SSM params reaped → syncingest errors every 30s | 📋 parallel, must land before exit week |
| 3 | — | Second-tracker rig on a console-provisioned tenant (Jira/Linear; founder-box Jira does not count) | 📋 after Leg 1 live |
| 4 | operator | Deploy minimal stack · place DB URL param · attach port-forward policy · run the S4 exit week | ⏸ after 1–3 |

## Refs

- Decision memory: `.agent/knowledge/memories/decisions/s4-unblock-minimal-invpc-ec2-console.md`
- Pitfall: `.agent/knowledge/memories/pitfalls/s4-dashboard-only-clause-blocks-local-console.md`
- Learning: `.agent/knowledge/memories/learnings/console-ssm-paths-work-locally-proxy-does-not.md`
- TASK-405 (program) · saas-roadmap.md S4 row
