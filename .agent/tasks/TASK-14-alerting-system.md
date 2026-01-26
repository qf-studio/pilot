# TASK-14: Alerting System

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Monitoring

---

## Context

**Problem**:
No proactive notifications when things go wrong - stuck tasks, repeated failures, budget exceeded.

**Goal**:
Alert operators/teams when Pilot needs attention.

---

## Alert Types

### Operational
- Task stuck (no progress > 10 min)
- Task failed
- Consecutive failures (> 3)
- Service unhealthy

### Cost/Usage
- Daily spend exceeded threshold
- Token budget depleted
- Unusual usage spike

### Security
- Unauthorized access attempt
- Sensitive file modified
- Unusual execution pattern

---

## Alert Channels

1. **Slack** (existing adapter)
2. **Telegram** (existing adapter)
3. **Email** (new)
4. **PagerDuty** (new, enterprise)
5. **Webhook** (custom integrations)

---

## Configuration

```yaml
alerts:
  enabled: true
  channels:
    - type: slack
      channel: "#pilot-alerts"
      severity: [warning, critical]
    - type: email
      to: "ops@company.com"
      severity: [critical]

  rules:
    - name: task_stuck
      condition: "progress_unchanged > 10m"
      severity: warning

    - name: daily_spend
      condition: "daily_cost > $50"
      severity: warning
```

---

**Monetization**: Premium alert channels, custom rules for enterprise
