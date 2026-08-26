---
name: console-ssm-paths-work-locally-proxy-does-not
description: A laptop-run pilot-console CAN provision, readiness-gate and config-push tenant EC2 boxes (all SSM RunCommand / EC2 API — zero inbound networking) but CANNOT reach the dashboard proxy, which is a direct HTTP call to the tenant's private IP:9090 behind a console-SG-only rule
type: learning
---

# Local console: SSM-mediated paths work, the dashboard proxy does not

**Established 2026-08-26** while evaluating whether the S4 exit week could run
off the laptop instead of waiting on the deferred domain/ACM/SES purchase.

## The split (this is the whole story)

Two classes of console→tenant interaction, and only one survives on a laptop:

| Path | Mechanism | Works from laptop? |
|---|---|---|
| Provision / terminate | EC2 API | ✅ |
| Observe state | `DescribeInstances` | ✅ |
| **Readiness gate** | SSM RunCommand runs `curl -sf localhost:9090/ready` **ON the instance** (`internal/fleet/ready.go`) | ✅ |
| Config push | `pilot-apply-config` SSM document | ✅ |
| Secrets | SSM Parameter Store | ✅ |
| **Dashboard proxy** — status · metrics · queue · history · logs · timeline · `/ws/dashboard` · chat · docs | Direct HTTP from console-api to the tenant's **private IP :9090** (`internal/proxy/proxy.go`) | ❌ |

The readiness row is the one that fools people: an instance reaching
`observed=running` looks like proof the console reached port 9090. It is not —
the curl runs *on the box*, dispatched via SSM. That is exactly why the 08-18
three-tenant S3 exit passed from a laptop with zero VPC presence.

## Why the proxy can't work from a laptop

Tenant SG allows tcp/9090 **from the console-api SG only** — a hard invariant
(`saas-fleet-design.md` §5; enforced in `internal/fleet/tenantres.go`). Tenant
boxes have no public IP. A laptop has neither VPC presence nor SG membership,
so packets are dropped: probing the tenant's private address from the console
container **times out** rather than being refused (verified 2026-08-26 against
`i-0a3bf271d598196ca`). Already observed failing in the 08-19 E2E marker.

**No mitigation exists in the codebase** — zero hits for an SSM port-forward,
VPN or bastion path to 9090. And the obvious manual workaround is closed by
IAM: user `aleks` is **not authorized for `ssm:StartSession` on tenant
instances** (only the founder box), so even a hand-rolled tunnel needs a grant
via the mgmt runner first.

**How to apply**: any plan that runs the console locally gets the whole
autonomous loop (provision → pick → execute → PR → merge → deprovision) for
free, and none of the live dashboard surface. Decide which half the goal needs
before assuming "local is fine". See [[s4-dashboard-only-clause-blocks-local-console]]
for what this costs the S4 exit specifically, and
[[s3-exit-three-tenant-pass-proven]] for the half that already passed locally.
