# Pitfall: CDK apps are invisible to Environment-tag cost views unless Tags.of(app) is applied

## Summary
CDK never applies the account's `Environment` tag convention — it only emits its own `aws:cdk:*` tags. The pilot-cloud-infra fleet stacks (FleetVpcStack, ControlPlaneStack, TenantBaseStack, DataLifecycleStack) ran as "No tag key" spend from the 2026-07-24 deploy until the 2026-07-31 cost audit.

## Context
2026-07-31 AWS cost investigation: the untagged-cost block that appeared Jul-24 traced exactly to the fleet VPC bring-up (TASK-405 S2 exit step 4). Every other workload in the account (pointer: RDS ×2, ALB, NAT, ECS) was correctly `Environment`-tagged — only the CDK-deployed estate was invisible. The two FleetVpc NAT gateways (~$32/mo each + data) were the bulk of the jump.

## Details
Second defect found in the same audit: `internal/stacks/fleetvpc/fleetvpc.go` sets `NatGateways: jsii.Number(minAzCount)` with `minAzCount = 2` (one NAT per AZ), but the committed v1 design (`saas-fleet-design.md` §5) specifies ONE NAT per env — per-AZ NAT is a v2 availability upgrade. The code shipped the v2 posture: ~$33/mo avoidable spend. Design-doc cost decisions don't enforce themselves; synth tests must assert them.

## Recommended Approach
- Every new CDK app: `awscdk.Tags_Of(app).Add(jsii.String("Environment"), jsii.String("<env>"), nil)` at app scope in `main.go` at SCAFFOLD time — one line, all stacks inherit. Add it to the scaffold checklist, not as a retrofit.
- Add synth unit tests asserting (a) the `Environment` tag is present on taggable resources, (b) cost-relevant resource counts (NAT gateways) match the design doc.
- `CDKToolkit` bootstrap stack stays untagged — ~$0, out of scope.

## Related
- TASK-405 (Pilot Cloud SaaS — fleet infra)
- Fix dispatched: [pilot-cloud-infra#26](https://github.com/qf-studio/pilot-cloud-infra/issues/26)
- `pilot-cloud-infra/main.go`, `pilot-cloud-infra/internal/stacks/fleetvpc/fleetvpc.go`
- `.agent/system/saas-fleet-design.md` §5 (NAT sizing decision)

---
**Captured**: 2026-07-31
**Confidence**: 95%
**Concepts**: cdk, aws, cost-allocation, tagging, nat-gateway, pilot-cloud-infra
