---
name: cdk-merged-not-deployed-rots
description: CDK/IaC that merges green on synth-only CI but is never deployed rots against the cloud provider — retired engine minors disappear and missing required params only surface at CloudFormation apply; first deploy of old-but-merged infra is a bug-finding exercise, not a formality
type: pitfall
---

# Merged-but-never-deployed CDK rots silently

**What happened (2026-07-24):** the pilot-cloud-infra fleet stacks had been
merged for days with green CI. Their **first actual deploy** (4 stacks, fleet
VPC for S2 exit step 4) hit two failures that no amount of review or CI would
have caught:

- **infra#20** — DLM lifecycle policy missing `ExecutionRoleArn`. A required
  parameter that CloudFormation only demands at apply time; synth was happy.
- **infra#21** — a pinned Postgres **17.4** minor that AWS had since retired.
  The pin was valid when written and invalid by the time it ran.

Both fixed same-day, but they turned "deploy the infra" into a debugging
session on the critical path of an exit gate.

## Mechanism

- Synth-only CI (`cdk synth` / unit assertions) validates the *template*, not
  the *provider's acceptance of it*. Required-property gaps and
  resource-level constraints live in the CloudFormation/service API.
- Cloud providers retire RDS engine minors, AMIs, and instance types on their
  own schedule. A version pin is a time bomb whose fuse starts at merge, not
  at deploy.
- The gap widens with delay: merged 07-2x, deployed 07-24. Nothing in the
  repo signals "this has never actually run."
- Companion friction the same day: the mgmt runner's PAT couldn't read
  `pilot-cloud-infra`, forcing a bundle-over-S3 workaround to deploy at all —
  another thing only discovered on first real deploy.

## How to avoid

1. Treat "merged" and "deployed" as different states for IaC. Don't schedule
   a first deploy on the critical path of a gate; budget it as bug-finding.
2. Prefer floating minors / `auto_minor_version_upgrade` over exact engine
   pins, or pin with an explicit expiry review. Pinned minors rot fastest.
3. Deploy IaC to a throwaway account/stack soon after merge, even if the real
   rollout is later — the value of CI here is bounded by whether anything ever
   calls the provider.
4. On first deploy, expect the failure list to be *required params* and
   *retired versions* before anything more interesting; check those first.

Related: [[workflow-yaml-requires-front-matter]] (config that silently falls
back and is never exercised), [[absolute-state-paths-bypass-cutover-shim]]
(assumptions that hold until the first real environment runs them).
