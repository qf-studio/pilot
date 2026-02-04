# GH-334: Fix Telegram Notifications in Polling Mode

**Status**: 🚧 Ready for Pilot
**Created**: 2026-02-02
**Priority**: P1 (Core functionality broken)

---

## Context

**Problem**:
`--telegram` flag enables inbound messages (receiving tasks) but NOT outbound notifications. Users don't get notified when tasks complete or fail in polling mode.

**Root Cause**:
Alerts engine is only initialized in `pilot task` command, not in `runPollingMode()`. No `ProcessEvent()` calls in polling mode = no notifications.

**Impact**:
- Users running `pilot start --telegram --github` get no completion notifications
- Have to manually check GitHub for PR status
- Core UX broken for polling workflow

---

## Investigation Summary

| Mode | Inbound (receive) | Outbound (notify) |
|------|-------------------|-------------------|
| `pilot task` | N/A | ✅ Works |
| `pilot start --telegram` | ✅ Works | ❌ Missing |

**Key Files**:
- `cmd/pilot/main.go:441-1034` — `runPollingMode()` (missing alerts)
- `cmd/pilot/main.go:1692-1744` — alerts engine init in `newTaskCmd()`
- `internal/alerts/engine.go` — Event processing
- `internal/adapters/telegram/notifier.go` — Telegram notification methods

---

## Implementation Plan

### Phase 1: Wire Alerts Engine into Polling Mode

**Goal**: Initialize alerts engine in `runPollingMode()`

**Tasks**:
- [ ] Create alerts engine with Telegram channel in `runPollingMode()`
- [ ] Pass alerts engine to dispatcher/runner
- [ ] Mirror initialization pattern from `newTaskCmd()` (lines 1692-1744)

**Files**:
- `cmd/pilot/main.go` — Add alerts engine setup in runPollingMode()

### Phase 2: Emit Events from Dispatcher

**Goal**: Emit task lifecycle events during queue execution

**Tasks**:
- [ ] Emit `EventTypeTaskStarted` when task begins
- [ ] Emit `EventTypeTaskCompleted` on success
- [ ] Emit `EventTypeTaskFailed` on failure
- [ ] Include task details (ID, title, duration, PR link)

**Files**:
- `cmd/pilot/main.go` — Add ProcessEvent calls in dispatcher callback
- `internal/executor/runner.go` — Ensure events bubble up correctly

### Phase 3: Test & Verify

**Tasks**:
- [ ] Test with `pilot start --telegram --github`
- [ ] Verify notifications arrive on task start
- [ ] Verify notifications arrive on task complete (with PR link)
- [ ] Verify notifications arrive on task failure (with error)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Where to init alerts | runPollingMode / dispatcher | runPollingMode | Mirrors task command pattern, single init point |
| Event emission | In dispatcher callback / in runner | Dispatcher callback | Cleaner separation, dispatcher owns task lifecycle |

---

## Verify

```bash
# Start polling with Telegram
pilot start --telegram --github --autopilot=dev

# Create test issue with pilot label
gh issue create --title "Test notification" --label pilot --body "Test task"

# Expected: Telegram notifications for start, progress, completion
```

---

## Done

- [ ] Alerts engine initialized in `runPollingMode()`
- [ ] `TaskStarted` notification sent when task picked up
- [ ] `TaskCompleted` notification sent with PR link
- [ ] `TaskFailed` notification sent with error details
- [ ] Works with `--autopilot=dev/stage/prod` modes

---

**Last Updated**: 2026-02-02
