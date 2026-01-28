# TASK-10: Daily Briefs

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Assignee**: Pilot

---

## Context

**Problem**:
Team leads and managers need visibility into Pilot's work without checking each PR. Currently there's no aggregated view of what Pilot accomplished, what's in progress, or what blockers occurred.

**Goal**:
Generate and deliver daily summary briefs of Pilot's activity, including completed tasks, PRs created, blockers encountered, and upcoming work.

**Success Criteria**:
- [x] Daily brief generated at configured time
- [x] Brief delivered via Slack/email
- [x] Summary includes completed work, in-progress, and blockers
- [x] Configurable per-team or per-project

---

## Implementation Summary

### Files Created

| File | Purpose |
|------|---------|
| `internal/briefs/types.go` | Data structures: Brief, TaskSummary, BriefMetrics, configs |
| `internal/briefs/generator.go` | Brief generation from memory store |
| `internal/briefs/scheduler.go` | Cron-based scheduling with robfig/cron |
| `internal/briefs/formatter.go` | Plain text formatter + interface |
| `internal/briefs/formatter_slack.go` | Slack mrkdwn + Block Kit formatter |
| `internal/briefs/formatter_email.go` | HTML email formatter |
| `internal/briefs/delivery.go` | Delivery orchestration to channels |
| `internal/briefs/generator_test.go` | Generator tests |
| `internal/briefs/formatter_test.go` | Formatter tests |

### Files Modified

| File | Changes |
|------|---------|
| `internal/memory/store.go` | Added BriefQuery, GetExecutionsInPeriod, GetActiveExecutions, GetBriefMetrics, GetQueuedTasks |
| `internal/config/config.go` | Expanded DailyBriefConfig with channels, content, filters |
| `internal/adapters/slack/client.go` | Added Elements field to Block for context blocks |
| `cmd/pilot/main.go` | Added `pilot brief` command with --now and --weekly flags |

### CLI Commands

```bash
# Show scheduler status
pilot brief

# Generate and optionally deliver brief immediately
pilot brief --now

# Generate weekly summary
pilot brief --weekly
```

---

## Configuration

```yaml
orchestrator:
  daily_brief:
    enabled: true
    schedule: "0 9 * * 1-5"  # 9 AM weekdays (cron syntax)
    timezone: "America/New_York"

    channels:
      - type: "slack"
        channel: "#dev-briefs"

      - type: "email"
        recipients:
          - "team-lead@company.com"

    content:
      include_metrics: true
      include_errors: true
      max_items_per_section: 10

    filters:
      projects: []  # empty = all projects
```

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Scheduler | Built-in, cron pkg | robfig/cron | Battle-tested, cron syntax |
| Primary channel | Slack, Email | Both | Slack for real-time, email for archive |
| Brief scope | Global, Per-project | Configurable | Different team needs |
| Metrics period | Day, Week | Both | Daily summary, weekly trends |

---

## Verify

```bash
# Run tests
go test ./internal/briefs/... -v
# Output: PASS (17 tests)

# Run linter on production code
golangci-lint run internal/briefs/*.go
# Output: 0 issues

# Manual test - generate brief now
pilot brief --now

# Check scheduled brief status
pilot brief
```

---

## Done

Observable outcomes that prove completion:

- [x] Brief generates at scheduled time (scheduler implemented with cron)
- [x] Slack receives formatted brief (Block Kit + mrkdwn formatters)
- [x] Email delivery works (HTML formatter, EmailSender interface)
- [x] Brief includes all sections (completed, progress, blocked, upcoming)
- [x] Metrics accurate (success rate, avg duration, PRs created)
- [x] Configurable schedule works (cron syntax, timezone support)
- [x] Tests pass (17 tests, all passing)

---

## References

- [robfig/cron](https://github.com/robfig/cron) - Used for scheduling
- [Slack Block Kit](https://api.slack.com/block-kit) - Used for Slack formatting

---

**Last Updated**: 2026-01-26
