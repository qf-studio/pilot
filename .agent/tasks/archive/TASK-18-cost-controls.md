# TASK-18: Cost Controls & Budgets

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Monetization / Safety

---

## Context

**Problem**:
Runaway tasks can burn through API credits. No spending limits.

**Goal**:
Protect users from unexpected costs with budgets and limits.

---

## Controls

### Budget Limits
- Daily spend limit ($)
- Monthly spend limit ($)
- Per-task token limit
- Per-task time limit

### Actions When Exceeded
1. **Warn** - Notify but continue
2. **Pause** - Stop new tasks, finish current
3. **Stop** - Terminate immediately

---

## Configuration

```yaml
cost_controls:
  daily_limit: 50.00  # USD
  monthly_limit: 500.00
  per_task:
    max_tokens: 100000
    max_duration: 30m

  on_exceed:
    daily: pause
    monthly: stop
    per_task: stop
```

---

## Implementation

### Phase 1: Per-Task Limits
- Token limit via Claude Code flags
- Timeout via context.WithTimeout
- Kill runaway processes

### Phase 2: Budget Tracking
- Aggregate costs from metrics
- Check before starting task
- Real-time budget remaining

### Phase 3: Alerts
- 80% threshold warning
- Budget exceeded notification
- Weekly cost summary

---

## UI

```
pilot status

💰 Budget Status
─────────────────────
Daily:   $12.50 / $50.00 (25%)
Monthly: $156.00 / $500.00 (31%)

⚠️  Warning: 3 tasks blocked due to per-task limits
```

---

**Monetization**: Essential for paid tiers - prevents bill shock
