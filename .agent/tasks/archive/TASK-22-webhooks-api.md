# TASK-22: Webhooks API

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Integrations / DX

---

## Context

**Problem**:
Users can't integrate Pilot with their own tools/workflows.

**Goal**:
Outbound webhooks for custom integrations.

---

## Events

### Task Lifecycle
- `task.created` - New task received
- `task.started` - Execution began
- `task.progress` - Phase changed
- `task.completed` - Success
- `task.failed` - Error

### PR Lifecycle
- `pr.created` - PR opened
- `pr.merged` - PR merged
- `pr.closed` - PR closed without merge

### System
- `budget.warning` - Approaching limit
- `budget.exceeded` - Limit hit
- `health.degraded` - Service issues

---

## Webhook Payload

```json
{
  "event": "task.completed",
  "timestamp": "2026-01-26T14:30:00Z",
  "data": {
    "task_id": "TG-123",
    "project": "my-app",
    "duration_seconds": 120,
    "pr_url": "https://github.com/...",
    "files_changed": 5
  },
  "signature": "sha256=..."
}
```

---

## Configuration

```yaml
webhooks:
  - url: "https://my-app.com/pilot-events"
    secret: "${WEBHOOK_SECRET}"
    events:
      - task.completed
      - task.failed
    retry:
      max_attempts: 3
      backoff: exponential
```

---

## Use Cases

1. **Custom Dashboard** - Build your own UI
2. **Analytics** - Send to Datadog/Mixpanel
3. **Automation** - Trigger downstream pipelines
4. **Notifications** - Custom notification service

---

**Monetization**:
- Free: 1 webhook endpoint
- Pro: 5 endpoints
- Enterprise: Unlimited + custom events
