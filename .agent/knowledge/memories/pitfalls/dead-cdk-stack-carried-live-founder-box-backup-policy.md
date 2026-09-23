---
name: dead-cdk-stack-carried-live-founder-box-backup-policy
description: A 'dead' estate stack can carry a live cross-estate control: DataLifecycleStack (old CDK 10.30) owned the tag-targeted DLM policy that was the founder box's only nightly backup — check DLM TargetTags / tag-scoped resources before deleting an estate
type: pitfall
---

# A dead estate stack can own a live control elsewhere — check tag-targeted policies before teardown

**What happened (2026-09-23):** tearing down the superseded CDK estate (VPC 10.30: `TenantBaseStack`, `ControlPlaneStack`, `FleetVpcStack`, `DataLifecycleStack`). Everything in it was unused — except `DataLifecycleStack`'s DLM policy `policy-057f7e35f2adc5fba`, which targets **tag** `pilot:data-volume=true`. The founder box's data volume (`vol-068f78767202b20aa`, the terminal Pilot's ledger) carries that tag, so a stack in the estate being deleted was the only daily backup of the estate we keep. It had also accumulated 3 TB of snapshots, 1.6 TB of them orphans of dead tenant volumes.

Resolution: Nelya recreated the policy in her `pilot` family (`pilot-data-lifecycle`, `policy-01ba06ee281502274`, 01:30 UTC, keep 7), removed her duplicate fleet copy so exactly one policy owns the tag account-wide, and the old stack is deleted only after the first snapshot from the new policy is confirmed.

## Rule
Before deleting an estate, list every **tag-scoped** resource in it (DLM policies `TargetTags`, backup plans, EventBridge rules, IAM policies with tag conditions) and check whether the tag is carried by resources outside the estate. `list-imports` only catches CloudFormation exports; tag targeting crosses stacks silently.

## Related
[[console-email-transport-ses-not-email-service-repo]] · TASK-495 teardown log.
