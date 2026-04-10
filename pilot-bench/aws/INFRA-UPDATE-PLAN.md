# AWS Infra Updates for Bench Runner

**For:** Nelya
**Repo:** `qf-studio/aws-infrastructure-pilot`
**Why:** Enable Terminal Bench 2.0 execution on warm pool instances

## Changes Required

### 1. Instance Type — `stacks/workload/compute.yml`

**Line 27** — change Default:
```yaml
Default: t3.xlarge    # was: t3.small
```

**Lines 28-32** — add to AllowedValues:
```yaml
AllowedValues:
  - t3.small
  - t3.medium
  - t3.large
  - t3.xlarge      # ADD
  - c5.xlarge      # ADD
```

**Why:** t3.small = 2GB RAM. Bench tasks need 4-16GB (Docker container + Claude Code + agent overhead).

---

### 2. EBS Volume — `stacks/workload/compute.yml`

**Line 35** — change Default:
```yaml
Default: 40    # was: 20
```

**Why:** Each task pulls a Docker image (100MB-2GB). 20GB fills after 5-10 tasks.

---

### 3. Health Check Grace Period — `stacks/workload/compute.yml`

**Line 157** — change value:
```yaml
HealthCheckGracePeriod: 7200    # was: 120
```

**Why:** Tasks run 15-90 min. ASG kills instances after 120s grace period, destroying running tasks.

---

### 4. Max ASG Size — `stacks/workload/compute.yml`

**Line 43** — change Default:
```yaml
Default: 10    # was: 5
```

**Why:** Leaderboard runs need k=5 trials across 89 tasks. 10 instances halves wall-clock time.

---

### 5. Deploy Workflow — `.github/workflows/deploy-pilot.yml`

**Lines 159-163** — add parameter overrides:
```yaml
--parameter-overrides \
  NetworkStack=pilot-network \
  SecurityGroupStack=pilot-security-groups \
  KmsStack=pilot-kms \
  PilotAgentRoleStack=pilot-agent-role \
  InstanceType=t3.xlarge \
  VolumeSize=40
```

---

## After Deploy

```bash
# 1. Flush warm pool (pick up new instance type + disk)
bash scripts/flush-warm-pool.sh

# 2. Run moulage to validate
gh workflow run deploy.yml --repo qf-studio/pilot-moulage

# 3. Ping us — we'll run the bench smoke test from pilot repo
```

## Cost Impact

| | Before | After |
|---|---|---|
| Instance | t3.small ($0.02/hr) | t3.xlarge ($0.17/hr) |
| EBS | 20GB × 2 = $4/mo | 40GB × 2 = $8/mo |
| Warm pool (stopped) | $0 compute | $0 compute |
| Running 10 instances for 6hr bench | — | ~$10 |

Instances are stopped in warm pool when idle — only EBS storage cost.

## Summary

| File | Line | Before | After |
|------|------|--------|-------|
| `compute.yml` | 27 | `t3.small` | `t3.xlarge` |
| `compute.yml` | 28-32 | 3 types | +t3.xlarge, c5.xlarge |
| `compute.yml` | 35 | `20` | `40` |
| `compute.yml` | 43 | `5` | `10` |
| `compute.yml` | 157 | `120` | `7200` |
| `deploy-pilot.yml` | 159 | no type override | `InstanceType=t3.xlarge VolumeSize=40` |
