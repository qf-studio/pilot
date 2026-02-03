# Autopilot Mode

Autopilot provides end-to-end automation: from issue pickup to PR merge. Choose your level of automation.

## Environments

| Mode | CI Check | Auto-Merge | Human Approval |
|------|----------|------------|----------------|
| `dev` | Skip | Yes | No |
| `stage` | Wait | Yes | No |
| `prod` | Wait | No | Required |

## Usage

```bash
# Fast iteration - skip CI, auto-merge
pilot start --autopilot=dev --github

# Balanced - wait for CI, then auto-merge
pilot start --autopilot=stage --github

# Safe - wait for CI + human approval
pilot start --autopilot=prod --github
```

## How It Works

### Dev Mode

```
Issue claimed → Branch → Implement → PR → Auto-merge
                                           │
                                           └── Immediate, no CI wait
```

Best for:

- Rapid prototyping
- Documentation changes
- Trusted environments with good test coverage

### Stage Mode

```
Issue claimed → Branch → Implement → PR → CI → Auto-merge
                                          │
                                          └── Wait for green checks
```

Best for:

- Regular development
- Teams with CI/CD pipelines
- Non-critical codebases

### Prod Mode

```
Issue claimed → Branch → Implement → PR → CI → Review → Merge
                                          │      │
                                          │      └── Human approval
                                          └── Wait for green checks
```

Best for:

- Production codebases
- Compliance requirements
- Security-sensitive changes

## Feedback Loop

When post-merge CI fails, Autopilot:

1. Detects the failure
2. Creates a fix issue
3. Labels it `pilot`
4. Implements the fix
5. Creates a new PR

```
Merge → CI fails → Auto-create fix issue → Pilot fixes → PR → CI → Merge
```

## Dashboard Integration

Monitor autopilot status in real-time:

```bash
pilot start --autopilot=stage --github --dashboard
```

```
┌─ Pilot Dashboard ─────────────────────────────────────────┐
│                                                           │
│  Status: ● Running    Autopilot: stage    Queue: 2       │
│                                                           │
│  Autopilot Status                                         │
│  ├─ PR #142: Waiting for CI (2/5 checks)                 │
│  ├─ PR #141: Merged ✓                                    │
│  └─ PR #140: Merged ✓                                    │
│                                                           │
│  Current Task                                             │
│  ├─ GH-156: Add caching layer                            │
│  ├─ Phase: Implementing (45%)                             │
│  └─ Duration: 1m 12s                                      │
│                                                           │
└───────────────────────────────────────────────────────────┘
```

## Direct Deploy Options

For teams that rely on manual QA instead of code review:

### Local Only (No PR)

```bash
# Changes stay local - no branch, no PR, no push
pilot start --no-pr --github
```

### Direct to Main (No PR, No Branch)

```bash
# ⚠️ Requires double opt-in for safety

# 1. Add to ~/.pilot/config.yaml:
executor:
  direct_commit: true

# 2. Use the flag:
pilot start --direct-commit --github
```

!!! warning "Use with caution"

    Direct commit mode bypasses all review processes. Only use when:

    - You have a staging environment for manual QA
    - Your CI/CD can catch issues
    - You have rollback mechanisms in place

## Notifications

Autopilot sends status updates via configured channels:

```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "${TELEGRAM_BOT_TOKEN}"
```

You'll receive:

- PR created notifications
- CI status updates
- Merge confirmations
- Failure alerts

## Configuration

Full autopilot configuration:

```yaml
orchestrator:
  execution:
    mode: sequential
    wait_for_merge: true
    poll_interval: 30s
    pr_timeout: 1h

autopilot:
  ci_timeout: 30m          # Max wait for CI checks
  approval_timeout: 24h    # Max wait for human approval (prod mode)
  feedback_loop: true      # Auto-fix post-merge failures
  max_fix_attempts: 3      # Max automated fix attempts
```
