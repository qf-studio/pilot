# feat(controlplane): S4 unblock — minimal in-VPC console mode (no ALB/ACM/SES/SPA), SSM port-forward access

🟢 CODE COMPLETE 2026-08-31, one-day cycle — **legs 1+1b+2 all shipped, reviewed, review debt zero; OPERATOR DEPLOY IS THE GATE NOW.** Full chain: 5 issues → 5 PRs merged+verdicts same day (infra#41→PR#42 REQUEST-CHANGES → infra#43→**PR#44 APPROVE** [access-path ARN fix mutation-verified; default synth byte-identical] · console#239→PR#240 APPROVE-w-notes · console#241→**PR#243 APPROVE** [tarball verified by execution] · console#242→**PR#244 APPROVE** [ordering-oracle test killed 2 mutants]). Founder decision memory: `s4-unblock-minimal-invpc-ec2-console`. Next: Leg 4a operator deploy (runbook in infra docs: make package → S3 upload → cdk deploy minimal → DB-URL SSM param → attach port-forward policy to an IAM user) → then Leg 3 tracker-rig dispatch → Leg 4 exit week.

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
| 1 | pilot-cloud-infra | Minimal mode flag (no ALB/ACM/SES/SPA), 5432 rule, port-forward IAM policy + outputs, reconciler systemd unit, runbook section | ⚠️ #41 → **PR#42 MERGED 12:33Z same day** · post-merge review **REQUEST-CHANGES** (verdict on PR): port-forward policy renders the SSM document ARN WITH account id — AWS-owned docs authorize account-less, so StartSession = AccessDenied on minimal mode's ONLY access path; presence-only test couldn't catch it. Fix + 4 test/docs hardenings → [infra#43](https://github.com/qf-studio/pilot-cloud-infra/issues/43) (pilot-labeled). Held items in #43: 5432-rule tests don't pin GroupId (reversed rule passes) · policy test Action/Effect-only · user-data extracts only the pilot-console tar member (consolectl guard unsatisfiable) · runbook IAM-user + shell-session caveats |
| 1b | pilot-console | Package consolectl into the console release tarball (producer side; infra#43 item 4 is the consumer) — repo has ZERO packaging machinery today, tarball is operator-hand-built | 🚀 dispatched → [console#241](https://github.com/qf-studio/pilot-console/issues/241) |
| 2 | pilot-console | Deprovision cascade: connections rows + board children reaped at terminate, SSM param DeleteAll wired (zero prod callers today), syncingest terminated-org skip + status=error flagging | ✅ #239 → **PR#240 MERGED 13:00Z same day** · post-merge review **APPROVE-w-notes** (verdict on PR; reap order/skip/restore all mutation-verified — 3/3 test kills). Notes: settleDrift bypass = no reap on drift-terminate (layer 2 covers noise) · error flag couples to provisioning gate (transient 412 window) · latent DeleteAll pagination bug now live → follow-up [console#242](https://github.com/qf-studio/pilot-console/issues/242) (pilot-labeled) |
| 3a | pilot-console | Tenant config derivation for jira/linear — render adapter blocks (polling on pilot label), generation bump on non-github connection change, preflight secret gating. **THE rig blocker**: console sync plane is tri-tracker but tenant render is github-only → dispatching a jira card labels an issue NO daemon polls, silently | ✅ #245 → **PR#247 MERGED 16:00Z, APPROVE-w-notes** (rendered YAML replayed through a replica of the daemon decode path — exact tag match, NOT dead-on-the-wire; skip-removal mutation-verified). Notes → hardening follow-up [console#249](https://github.com/qf-studio/pilot-console/issues/249) (unescaped YAML interpolation + tautological byte-identity test + last-connection-deletion never converges the render) |
| 3b | pilot-console | Require project_key/team_key at the connection write boundary — today PUT returns 200 "connected" then the row error-flips dead-on-arrival on the first tick | ✅ #246 → **PR#248 MERGED 16:08Z, APPROVE** (validator mutation kills 4 tests; cross-decoder agreement test real) |
| 4 | operator | Deploy minimal stack · place DB URL param · attach port-forward policy · rig setup (checklist below) · run the S4 exit week | ⏸ after 1–3 |

## Leg 3 rig — operator checklist (research 2026-08-31, console origin/main ac779a8)

Second connection enters via the API/UI only — consolectl has NO connection command (seed-secret writes SSM but not the connections row). One board per org is automatic (two connections converge on it; card_links unique per connection — no cross-tracker collisions).

1. Seed the credential: JIRA_API_TOKEN (Atlassian API token) or LINEAR_API_KEY into the tenant SSM path (consolectl seed-secret works for this half).
2. PUT /api/v1/connections/jira with base_url (https://SITE.atlassian.net), email, **project_key** — or linear with **team_key**. After #246 lands, missing keys 422 instead of silently error-flipping. Jira Cloud only (platform hardcoded).
3. Status maps: jira/linear seed from OBSERVED states only (the github seed route 409s for them) — either drive real issue traffic through the tracker first, or PUT the statusmaps for the tracker with all 7 canonical rows manually. Without this, status write-back is a no-op and unmapped states freeze cards in backlog.
4. Verify the connection stays status=connected after one poll interval (60s default) — an error flip = connector build failed.
5. Accepted S4 degrades (documented, won't fix this leg): done-transition PR link github-only (jira/linear cards show no PR) · priority write-back no-op on github/jira (Linear-only round-trip) · Linear error classification is substring-heuristic with retry-everything fallback (watch for hot retry loops on novel Linear failures).

Deferred (not S4): consolectl connection command for reproducible rigs · github-only provisioning readiness gate (fine for GitHub+X tenants) · jira/linear e2e fixtures beyond what #245 adds.

## Refs

- Decision memory: `.agent/knowledge/memories/decisions/s4-unblock-minimal-invpc-ec2-console.md`
- Pitfall: `.agent/knowledge/memories/pitfalls/s4-dashboard-only-clause-blocks-local-console.md`
- Learning: `.agent/knowledge/memories/learnings/console-ssm-paths-work-locally-proxy-does-not.md`
- TASK-405 (program) · saas-roadmap.md S4 row
