# AWS Bench Runner

Run Terminal Bench 2.0 on AWS warm pool EC2 instances instead of Harbor/Daytona/Modal.

## Architecture

```
orchestrator.py (local/GHA)          EC2 warm pool (pilot-agent-pool)
  ├── Scale ASG to N                   ├── Instance resumes (~20s)
  ├── Upload assets to S3              ├── Downloads run-bench-task.sh from S3
  ├── SSM SendCommand per task         ├── docker pull <task-image>
  ├── Poll completion                  ├── Runs pilot binary inside container
  ├── Collect results from S3          └── Uploads results to S3
  └── Scale ASG to 0
```

## Quick Start

```bash
# Prerequisites
pip install boto3
make bench-binary  # From project root

# Generate task manifest (first time)
python3 extract_tasks.py

# Validation run (3 tasks)
./run-aws-bench.sh

# Full run
./run-aws-bench.sh --tasks all --k 1 --parallel 5

# Leaderboard run (k=5)
./run-aws-bench.sh --tasks all --k 5 --parallel 10 --run-id leaderboard-v1
```

## Files

| File | Purpose |
|------|---------|
| `config.py` | AWS constants (region, bucket, ASG, SSM params) |
| `extract_tasks.py` | Clone terminal-bench repo, generate task manifest |
| `run-bench-task.sh` | On-instance task runner (executed via SSM) |
| `orchestrator.py` | Main orchestrator — scales ASG, dispatches tasks, collects results |
| `instance_pool.py` | EC2 warm pool instance management |
| `ssm_executor.py` | SSM RunCommand wrapper with timeout handling |
| `pattern_sync.py` | S3-based learning DB sync |
| `run-aws-bench.sh` | CLI convenience wrapper |

## AWS Infrastructure Requirements

**Existing** (in `aws-infrastructure-pilot` repo):
- ASG `pilot-agent-pool` with warm pool
- S3 bucket `pilot-s3-agent-data`
- SSM params: `/pilot/ANTHROPIC_API_KEY`, `/pilot/GITHUB_TOKEN`
- IAM roles for agent instances and deployer runner

**Recommended updates** (CloudFormation parameter changes):
- Instance type: t3.small → **t3.xlarge** (4 vCPU, 16GB — handles all 89 tasks)
- EBS size: 20GB → **40GB** gp3 (Docker image caching)
- Health check grace period: → **7200s** (prevent mid-task termination)

## GitHub Actions

Trigger via `workflow_dispatch` at `.github/workflows/aws-bench.yml`:
- Runs on `[self-hosted, aws-agent-deployer]` runner
- Inputs: run_id, tasks, k_trials, max_parallel, model
- Auto-scales down on completion or failure
