---
name: s4-unblock-minimal-invpc-ec2-console
description: Founder decision 2026-08-31 — S4 exit unblocked via a minimal EC2 console inside the fleet VPC control-plane SG (no ALB/ACM/SES/domain), laptop access via SSM port-forward to that one instance; staging domain purchase stays deferred
type: decision
---

# S4 unblock: minimal in-VPC EC2 console (founder, 2026-08-31)

The open decision recorded in [[s4-dashboard-only-clause-blocks-local-console]]
is settled: deploy console-api + UI + reconciler on the control-plane app
instance inside the fleet VPC, skipping ALB/ACM/SES entirely. No domain
purchase — that stays deferred to release. Laptop reaches the UI via SSM
port-forward to that single instance (needs an `ssm:StartSession` grant
scoped to it; user `aleks` is denied on tenant instances and that stays
denied).

## Why this option

- The S4 exit clause "operated dashboard-only" names exactly the surface a
  laptop console cannot reach — the tenant private-IP:9090 proxy behind the
  console-SG-only rule ([[console-ssm-paths-work-locally-proxy-does-not]]).
  In-VPC console-api in the right SG makes the proxy work with zero tenant
  changes.
- ~$15/mo (one t3.small); the RDS/NAT burn already exists (~$120/mo idle).
- Alternatives rejected: full staging `ControlPlaneStack` (needs
  domain/ACM/SES — deferred) · SSM tunnel per-tenant to 9090 (unbuilt,
  IAM-denied by design).

## Leg order (agreed 2026-08-31)

1. Infra: minimal in-VPC console deploy + systemd supervision for the
   reconciler (the `nohup` gap) + SSM port-forward access path — the gate.
2. Console: deprovision cascade fix (stale `connections` rows → syncingest
   error loop) — parallel, must land before the exit week.
3. Second-tracker rig on a console-provisioned tenant — after leg 1.
4. Operator: the S4 exit week itself (dashboard-only, ≥2 trackers,
   approvals survive restart).

**How to apply**: S4-exit planning should target this in-VPC console as the
operative surface — do not resurrect the domain/staging purchase as an S4
blocker, and do not propose laptop-local consoles for any milestone whose
exit clause names the dashboard.
