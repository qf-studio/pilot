---
name: s3-exit-three-tenant-pass-proven
description: 2026-08-18 S3 exit test PASSED — 3 fresh tenants (signup→org→credentials→github connection→provision→box→pick→execute→PR→auto-merge→issue closed) concurrently on local console (docker) + laptop consolectl run + live fleet VPC, golden AMI ami-01ed3bb9600200ce4. Two operator bridges remain: SSM secret mirror (local-posture only — prod SSM driver writes direct) and consolectl add-repo (real product gap → console#177). Plus one pilot defect found live (label wedge → pilot#4961).
type: learning
created: 2026-08-18
---

# S3 exit test passed: 3 concurrent fresh tenants, tracker-to-merged-PR, no SSH

**The pass (2026-08-18, ~17:40–18:40 local).** Per the founder's local-first
exit definition ([[no-stripe-local-first-s3-testing]]): three brand-new users
registered against the local console stack (docker :8090, postgres secrets
driver), each created an org, PUT the anthropic credential + github connection
(live-validated), hit POST /instances/provision, and the laptop reconciler
(`consolectl run`, env preserved at `/tmp/fleet-test/consolectl.env`, rebuilt
from console main `09ab9e6`) drove three t3.large boxes off golden AMI
`ami-01ed3bb9600200ce4` in the fleet VPC. Each box picked its `pilot`-labeled
issue, executed (~30–60s, ~$0.06 each), opened a PR, autopilot merged it, and
the issue closed `pilot-done`. Fixture repos (kept as standing fixtures):
`qf-studio/pilot-s3exit-t1|-t2|-t3` (js/go/js), each seeded from the proven
08-16 scaffolds. Deprovision ran through the product API; reconciler
terminated all three, reaped factory IAM + volumes automatically; test-org SSM
secrets deleted manually (they persist post-deprovision by design — org may
re-provision).

**The two operator bridges (know them before calling anything "zero-touch"):**

1. **Secrets mirror (local-posture artifact, not a product bug):** the docker
   console writes credentials to postgres `local_secrets`; boxes fetch from
   SSM. Bridge = `aws ssm put-parameter /tenants/{org}/{KEY}` ×2 per org
   (replicates exactly what the prod SSM driver does on PUT). Disappears in
   prod posture.
2. **Repos → config spec (REAL product gap → console#177):** the org's github
   connection `config.repos` never reaches the tenant config spec; the only
   repo-bearing spec producers are operator CLIs (`consolectl add-repo`,
   `opsrerender`). Without the bridge every tenant runs the zero-repo gen-1
   config forever (github adapter disabled). The reconciler's config-sync only
   pushes existing drift; it derives nothing from connections.

**Defect found live (2 of 3 tenants):**
[[poller-labels-in-progress-before-dispatcher-claim-wedge]] → pilot#4961.
Trigger was self-inflicted (fixture issues duplicated already-merged helpers →
declines → re-dispatch inside repick backoff), but the wedge is a genuine
v2.259.3+main defect and the decline path is exactly TASK-480's territory.

**Also confirmed live:** decline burns an execution and loops generations
(TASK-480 motivation); zero-CI repos auto-merge fine; `desired=running /
observed=terminated` rows are safely inert on reconciler restart
(`decide()` → actionNone).
