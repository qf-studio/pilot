---
name: s4-dashboard-only-clause-blocks-local-console
description: S4's exit criterion is "operated dashboard-only", which is precisely the proxy surface a laptop console cannot reach — so unlike S3, the local-first precedent does NOT carry over; also the pen test is S5's gate, not S4's (the architecture doc's coarser Phase-2 grouping misleads)
type: pitfall
---

# "Just run S4 locally" fails on the one clause that names the proxy

**Established 2026-08-26.** The S3 exit passed local-first (local console +
real fleet-VPC tenants), so the reflex is to assume S4 can too. It cannot, and
the reason is specific.

## Two corrections to the obvious reading

**1. The exit clause names the unreachable surface.** The roadmap milestone
table's S4 row requires the tenant "operated **dashboard-only** for a full
week across ≥2 tracker types on one board; approvals survive instance
restart". Dashboard-only means the live board/timeline/logs/chat surface —
which is exactly the private-IP proxy a laptop console cannot reach (see
[[console-ssm-paths-work-locally-proxy-does-not]]). S3's criteria were
provisioning/execution-shaped and rode entirely on SSM-mediated paths, so the
precedent genuinely does not transfer. Approvals are the sharp edge: the
approve verb mirrors `approval_pending` from the instance and posts decisions
back through that same proxy.

**2. The pen test is S5's gate, not S4's.** `saas-architecture.md` bundles the
hostile-ticket pen test and the redaction review into its "Phase 2" exit,
which is a coarser grouping than the S-milestone table. The **roadmap table is
authoritative** (it is what TASK-405 and every progress log track), and it
puts the pen test in S5's scope and exit. Do not let the architecture doc's
wording add a phantom blocker to S4.

## The options, and the one worth costing

- Staging `ControlPlaneStack` with ALB — needs a pre-issued DNS-validated ACM
  cert imported by ARN plus an SES domain, i.e. the deferred domain purchase.
- **A minimal EC2 console inside the fleet VPC's control-plane SG, skipping
  ALB/ACM/SES entirely** — no domain needed; reach the UI from the laptop via
  an SSM port-forward to *that one instance*. Un-evaluated in any doc, and the
  cheapest path to a genuine dashboard-only week. `ControlPlaneStack` already
  provisions exactly such a t3.small app instance; the ALB is a separable
  concern.
- SSM tunnel straight to each tenant's 9090 — unbuilt, and IAM-denied today.

**How to apply**: before promising a local-first exit for any milestone, read
the exit clause for words like "dashboard", "console", "live" — those name the
proxy and force a VPC presence. Provisioning/execution wording does not.
Two further gaps found alongside this: the `consolectl run` reconciler is an
unsupervised `nohup` process (no systemd/launchd) and will die silently in a
week-long window, and the second-tracker rig (synthetic Linear/Jira on a
*console-provisioned tenant*, not the founder box) has never been exercised.
