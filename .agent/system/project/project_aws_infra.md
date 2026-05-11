---
name: AWS Infrastructure for Bench/Pilot
description: Nelya built AWS infra (aws-infrastructure-pilot repo) — warm pool EC2, SSM, S3, ECR in eu-central-1. Verified live 2026-04-10.
type: project
originSessionId: adae5544-217b-48f2-9dbf-debe4fb5e06a
---
Nelya created the AWS infrastructure in `qf-studio/aws-infrastructure-pilot` repo.

**AWS Account**: `529088297614` | **Profile**: `quantflow` | **User**: `aleks` | **Region**: `eu-central-1`

**Architecture**: Two-plane VPCs (mgmt 10.10.0.0/16 + workload 10.20.0.0/16) in eu-central-1.
- ASG `pilot-agent-pool` (min 0, max 5) with warm pool (5 stopped t3.xlarge instances, ~20s resume)
- Golden AMI: Node 22, Claude Code, Python 3, uv, Docker, Git, AWS CLI
- S3 `pilot-s3-agent-data` (KMS encrypted, lifecycle rules) — created 2026-03-29
- ECR `pilot-agent` (immutable tags)
- SSM params: `/pilot/ANTHROPIC_API_KEY`, `/pilot/GITHUB_TOKEN`
- Management: `mgmt-infra-admin-runner` (t3.small, running), `pilot-agent-deployer-runner` (t3.medium, running)
- Deployer runner: self-hosted `aws-agent-deployer` GH Actions label

**Validation**: `qf-studio/pilot-moulage` repo — smoke tests (tools, DNS, HTTPS, S3, git clone, Claude Code).

**Verified 2026-04-10**: All resources confirmed live. ASG idle (0 desired), 5 warm pool instances stopped, 2 mgmt runners active, S3 bucket present.

**CLI access**: `aws <command> --profile quantflow --region eu-central-1`

**Why:** Run bench tasks and Pilot workloads on isolated AWS compute instead of Daytona/Modal.

**How to apply:** When working on bench infrastructure, reference aws-infrastructure-pilot for CloudFormation stacks. Use pilot-moulage patterns for ASG scale + SSM execution. Branch `feat/aws-bench` has the orchestrator.
