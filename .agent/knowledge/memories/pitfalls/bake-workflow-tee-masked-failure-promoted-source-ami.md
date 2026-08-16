---
name: bake-workflow-tee-masked-failure-promoted-source-ami
description: build-ami.yml's `packer build | tee` without pipefail masked a failed build, and a bare `grep ami-*` then extracted the AL2023 SOURCE ami — only the (previously never-executed) throwaway validation stopped a blank Amazon image becoming the golden AMI. Assert-on-first-live-run + pipefail + artifact-anchored extraction.
type: pitfall
---

# Masked packer failure + loose grep nearly promoted the raw AL2023 source AMI to golden

**What happened (2026-08-16, aws-infrastructure-pilot run 31963034836):**
the first-ever live execution of the GH-2 bake machinery chained three
latent defects:

1. The in-build bake test asserted `pilot doctor`, which checks runtime
   config/tokens an unconfigured builder can never satisfy → build always
   fails at that step.
2. `packer build ... 2>&1 | tee log` in GitHub Actions without
   `set -o pipefail` → tee's exit 0 masked the failure, step went green.
3. `AMI_ID=$(grep -oP 'ami-[a-z0-9]+' log | tail -1)` matched the **AL2023
   source AMI** from earlier in the log. The validate step launched raw
   AL2023 (`gh: command not found`) — the only guard that stopped
   `/pilot/GOLDEN_AMI_ID` being pointed at a blank Amazon image. And that
   validate step itself only failed usefully by luck: the mgmt VPC SSM
   endpoint policy was missing `ssm:GetCommandInvocation`, so it had never
   worked either.

**Lessons:**
- CI machinery that has never had a live run (the bake test + validate
  script were added ~April, first executed August) should be assumed
  broken until one full green run exists.
- Never assert environment-dependent health checks (`pilot doctor`) in
  image-build contexts — assert binary presence/version only.
- Always `set -o pipefail` before `cmd | tee` in Actions steps.
- Extract artifact ids from the artifact block, never by bare pattern over
  a whole build log; empty-check the result.
- This is the TASK-460 false-success class in infra form: green step ≠
  the artifact you think.

**Fixes:** aws-infrastructure-pilot PR#6 (endpoint policy) + PR#7 (doctor
drop, pipefail, anchored extraction). First green end-to-end bake: run
31963758882 → `ami-01ed3bb9600200ce4`.

**Refs:** [[cdk-tenant-role-not-an-instance-role]] · aws-infrastructure-pilot#4
