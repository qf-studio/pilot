# TASK-14: Alerting System

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Category**: Monitoring

---

## Context

**Problem**:
No proactive notifications when things go wrong - stuck tasks, repeated failures, budget exceeded.

**Goal**:
Alert operators/teams when Pilot needs attention.

---

## Implementation Summary

### Files Created

- `internal/alerts/types.go` - Core types: Alert, AlertRule, ChannelConfig, Severity
- `internal/alerts/engine.go` - Alert engine with event processing and rule evaluation
- `internal/alerts/dispatcher.go` - Multi-channel alert dispatch with parallel delivery
- `internal/alerts/channels.go` - Channel implementations (Slack, Telegram, Email, Webhook, PagerDuty)
- `internal/alerts/config.go` - Configuration adapter to avoid circular imports
- `internal/alerts/engine_test.go` - Comprehensive tests for engine and dispatcher

### Files Modified

- `internal/config/config.go` - Added AlertsConfig with channels, rules, and defaults

---

## Alert Types

### Operational
- `task_stuck` - No progress for configured duration (default: 10 min)
- `task_failed` - Task execution failed
- `consecutive_failures` - Multiple consecutive failures (default: 3)
- `service_unhealthy` - Service health check failed

### Cost/Usage
- `daily_spend_exceeded` - Daily spend exceeds threshold
- `budget_depleted` - Budget limit exceeded
- `usage_spike` - Unusual usage increase

### Security
- `unauthorized_access` - Unauthorized access attempt
- `sensitive_file_modified` - Sensitive file modified
- `unusual_pattern` - Unusual execution pattern

---

## Alert Channels

1. **Slack** - Block Kit formatted messages with severity colors
2. **Telegram** - Markdown formatted messages with emojis
3. **Email** - HTML formatted emails with styled alert boxes
4. **Webhook** - JSON payloads with optional HMAC signing
5. **PagerDuty** - Events API v2 integration with dedup keys

---

## Configuration

```yaml
alerts:
  enabled: true
  channels:
    - name: slack-alerts
      type: slack
      enabled: true
      severities: [warning, critical]
      slack:
        channel: "#pilot-alerts"

    - name: ops-email
      type: email
      enabled: true
      severities: [critical]
      email:
        to: ["ops@company.com"]

    - name: telegram-alerts
      type: telegram
      enabled: true
      severities: [warning, critical]
      telegram:
        chat_id: 123456789

    - name: custom-webhook
      type: webhook
      enabled: true
      severities: [critical]
      webhook:
        url: "https://api.example.com/alerts"
        method: POST
        headers:
          Authorization: "Bearer ${WEBHOOK_TOKEN}"
        secret: "${WEBHOOK_SECRET}"

    - name: pagerduty-critical
      type: pagerduty
      enabled: false
      severities: [critical]
      pagerduty:
        routing_key: "${PAGERDUTY_KEY}"

  rules:
    - name: task_stuck
      type: task_stuck
      enabled: true
      condition:
        progress_unchanged_for: 10m
      severity: warning
      channels: []  # Empty = all channels accepting this severity
      cooldown: 15m
      description: "Alert when a task has no progress for 10 minutes"

    - name: task_failed
      type: task_failed
      enabled: true
      severity: warning
      cooldown: 0
      description: "Alert when a task fails"

    - name: consecutive_failures
      type: consecutive_failures
      enabled: true
      condition:
        consecutive_failures: 3
      severity: critical
      cooldown: 30m
      description: "Alert when 3 or more consecutive tasks fail"

    - name: daily_spend
      type: daily_spend_exceeded
      enabled: false
      condition:
        daily_spend_threshold: 50.0
      severity: warning
      cooldown: 1h
      description: "Alert when daily spend exceeds $50"

    - name: budget_depleted
      type: budget_depleted
      enabled: false
      condition:
        budget_limit: 500.0
      severity: critical
      cooldown: 4h
      description: "Alert when budget limit is exceeded"

  defaults:
    cooldown: 5m
    default_severity: warning
    suppress_duplicates: true
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Alert Engine                         │
├─────────────────────────────────────────────────────────┤
│  Event Queue ──► Rule Evaluator ──► Alert Generator    │
│       ▲              │                    │             │
│       │              │                    ▼             │
│  Event Sources   Cooldown Check      Dispatcher        │
│  - Executor      State Tracking      (parallel)        │
│  - Monitor       History                │               │
│  - Metrics                              ▼               │
│                                  ┌─────────────┐       │
│                                  │  Channels   │       │
│                                  ├─────────────┤       │
│                                  │ Slack       │       │
│                                  │ Telegram    │       │
│                                  │ Email       │       │
│                                  │ Webhook     │       │
│                                  │ PagerDuty   │       │
│                                  └─────────────┘       │
└─────────────────────────────────────────────────────────┘
```

---

## Key Features

- **Rule-based evaluation** - Configurable rules with conditions and cooldowns
- **Severity filtering** - Channels can filter by severity level
- **Cooldown support** - Prevent alert fatigue with per-rule cooldowns
- **Parallel dispatch** - Alerts sent to multiple channels concurrently
- **Alert history** - Track fired alerts for debugging/auditing
- **Stuck task detection** - Background goroutine checks for stuck tasks
- **Consecutive failure tracking** - Per-project failure counters reset on success

---

## Usage

```go
// Initialize engine
config := alerts.DefaultConfig()
config.Enabled = true

dispatcher := alerts.NewDispatcher(config)

// Register channels
slackCh := alerts.NewSlackChannel("slack-alerts", slackClient, "#alerts")
dispatcher.RegisterChannel(slackCh)

engine := alerts.NewEngine(config,
    alerts.WithDispatcher(dispatcher),
    alerts.WithLogger(logger),
)

// Start engine
engine.Start(ctx)

// Process events from executor
engine.ProcessEvent(alerts.Event{
    Type:      alerts.EventTypeTaskFailed,
    TaskID:    "TASK-123",
    TaskTitle: "Implement feature X",
    Project:   "/path/to/project",
    Error:     "Build failed: missing dependency",
    Timestamp: time.Now(),
})
```

---

## Testing

All tests pass:
- `TestEngine_ProcessTaskFailedEvent` - Verifies task failed alerts
- `TestEngine_ConsecutiveFailures` - Verifies consecutive failure detection
- `TestEngine_CooldownRespected` - Verifies cooldown prevents duplicate alerts
- `TestEngine_TaskCompletedResetsFails` - Verifies success resets failure counter
- `TestEngine_DisabledRulesIgnored` - Verifies disabled rules don't fire
- `TestDispatcher_DispatchToMultipleChannels` - Verifies multi-channel delivery
- `TestDispatcher_ChannelNotFound` - Verifies error handling
- `TestAlertHistory` - Verifies history tracking

---

**Monetization**: Premium alert channels (PagerDuty), custom rules for enterprise, webhook integrations
