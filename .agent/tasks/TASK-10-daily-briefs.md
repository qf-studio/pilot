# TASK-10: Daily Briefs

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Team leads and managers need visibility into Pilot's work without checking each PR. Currently there's no aggregated view of what Pilot accomplished, what's in progress, or what blockers occurred.

**Goal**:
Generate and deliver daily summary briefs of Pilot's activity, including completed tasks, PRs created, blockers encountered, and upcoming work.

**Success Criteria**:
- [ ] Daily brief generated at configured time
- [ ] Brief delivered via Slack/email
- [ ] Summary includes completed work, in-progress, and blockers
- [ ] Configurable per-team or per-project

---

## Research

### Brief Content

| Section | Content | Source |
|---------|---------|--------|
| Completed | Tasks finished, PRs merged | Memory store |
| In Progress | Active tasks, current phase | Task queue |
| Blocked | Failed tasks, errors | Error logs |
| Upcoming | Queued tasks | Task queue |
| Metrics | Success rate, avg time | Memory store |

### Delivery Options

| Channel | Pros | Cons |
|---------|------|------|
| Slack | Real-time, threaded | Requires bot |
| Email | Universal, archivable | Setup complexity |
| Discord | Community teams | Different API |
| Webhook | Flexible | Recipient handles render |

### Brief Format (Slack)

```
📊 *Pilot Daily Brief* — Jan 26, 2026

*✅ Completed (3)*
• TASK-42: Add user auth — PR #123 merged
• TASK-43: Fix login bug — PR #124 ready
• TASK-44: Update docs — PR #125 merged

*🔄 In Progress (1)*
• TASK-45: Add payments — 65% (implementing)

*🚫 Blocked (1)*
• TASK-46: API refactor — Tests failing
  └ Error: `auth_test.go:42: expected 200, got 401`

*📋 Upcoming (2)*
• TASK-47: Dashboard redesign
• TASK-48: Performance audit

*📈 Metrics*
• Success rate: 85% (17/20)
• Avg completion: 12 min
• PRs this week: 23
```

---

## Implementation Plan

### Phase 1: Brief Generator
**Goal**: Generate daily summary content

**Tasks**:
- [ ] Create `internal/briefs/generator.go`
- [ ] Query completed tasks from memory
- [ ] Query active/queued tasks
- [ ] Aggregate error logs for blockers
- [ ] Calculate metrics (success rate, avg time)

**Files**:
- `internal/briefs/generator.go` - Brief generation
- `internal/briefs/types.go` - Brief data structures
- `internal/memory/queries.go` - Add brief queries

### Phase 2: Scheduler
**Goal**: Trigger brief generation at configured time

**Tasks**:
- [ ] Create scheduler with cron-like syntax
- [ ] Support timezone configuration
- [ ] Support multiple schedules (daily, weekly)
- [ ] Handle missed briefs (system was down)

**Files**:
- `internal/briefs/scheduler.go` - Cron scheduler
- `internal/config/config.go` - Add brief config

### Phase 3: Formatters
**Goal**: Format brief for different channels

**Tasks**:
- [ ] Create Slack formatter (mrkdwn)
- [ ] Create email formatter (HTML)
- [ ] Create plain text formatter
- [ ] Support custom templates

**Files**:
- `internal/briefs/formatter_slack.go` - Slack format
- `internal/briefs/formatter_email.go` - Email format
- `internal/briefs/formatter.go` - Interface

### Phase 4: Delivery
**Goal**: Send briefs to configured channels

**Tasks**:
- [ ] Send via Slack adapter
- [ ] Add email delivery (SMTP)
- [ ] Support multiple recipients per brief
- [ ] Add delivery confirmation logging

**Files**:
- `internal/briefs/delivery.go` - Delivery orchestration
- `internal/adapters/email/sender.go` - Email adapter (new)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Scheduler | Built-in, cron pkg | robfig/cron | Battle-tested, cron syntax |
| Primary channel | Slack, Email | Slack | Already integrated, real-time |
| Brief scope | Global, Per-project | Configurable | Different team needs |
| Metrics period | Day, Week | Both | Daily summary, weekly trends |

---

## Configuration

```yaml
briefs:
  enabled: true
  schedule: "0 9 * * 1-5"  # 9 AM weekdays
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

## Dependencies

**Requires**:
- [ ] Memory store with task history
- [ ] Slack adapter (existing)
- [ ] Email adapter (new, optional)

**Related Tasks**:
- Extends Slack adapter
- Uses Memory queries

---

## Verify

```bash
# Run tests
make test

# Manual test - generate brief now
pilot brief --now

# Check scheduled brief
pilot brief --status
```

---

## Done

Observable outcomes that prove completion:

- [ ] Brief generates at scheduled time
- [ ] Slack receives formatted brief
- [ ] Email delivery works (optional)
- [ ] Brief includes all sections (completed, progress, blocked)
- [ ] Metrics accurate
- [ ] Configurable schedule works
- [ ] Tests pass

---

## References

- [robfig/cron](https://github.com/robfig/cron)
- [Slack Block Kit](https://api.slack.com/block-kit)
- [Go SMTP](https://pkg.go.dev/net/smtp)

---

**Last Updated**: 2026-01-26
