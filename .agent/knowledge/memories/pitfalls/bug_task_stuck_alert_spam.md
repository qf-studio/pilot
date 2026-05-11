---
name: task_stuck alert spam (Slack flood)
description: task_stuck alerts spam Slack because progress events are never emitted, per-rule cooldown rotates through stuck tasks, and orphans are never cleaned up
type: project
---

## The bug

`task_stuck` alerts flood Slack with "Task GH-XXX stuck at 0% for Nm0s" messages every ~16 minutes, rotating through different task IDs forever.

## Root cause (three bugs compound)

1. **`task_progress` event is never emitted.** `AlertEventTypeTaskProgress` is defined in `internal/executor/alerts.go:32` but no code path ever calls `ProcessEvent` with `EventTypeTaskProgress`. `SignalParser.GetLatestProgress` exists (`internal/executor/signal.go`) but `runner.go` never reads it to emit events. So `handleTaskStarted` writes `Progress=0, UpdatedAt=now` and `state.UpdatedAt` never moves. Every task that runs >10 min is "stuck at 0%" by definition.

2. **Per-rule cooldown rotates through stuck tasks.** `evaluateStuckTasks` (`internal/alerts/engine.go:368`) loops over all stuck tasks but `shouldFire(rule)` keys cooldown by `rule.Name`. With N stuck tasks, 1st fires → cooldown locks rule → others skipped → 16 min later (1 min ticker + 15 min cooldown) next pass picks one (random Go map iteration) → repeats forever.

3. **No cleanup of orphans in `taskLastProgress`.** If `TaskCompleted/TaskFailed` is missed (crash, hot upgrade, dispatcher drops event), entry sits forever.

## Why: design oversight

The alert was added with the intent that runner.go would emit progress events from `pilot-signal` JSON blocks, but the wiring was never built. Per-rule cooldown was OK when there's 0-1 stuck tasks; falls apart at scale.

## How to apply

- Before re-enabling task_stuck after the fix, verify progress events are flowing: grep `runner.go` for `EventTypeTaskProgress` emission.
- Per-task dedup is essential: same task should not re-alert without making progress.
- Workaround for users hit by this: `alerts.rules[task_stuck].enabled: false` in `~/.pilot/config.yaml`.
- Config defaults: `Cooldown: 15m`, `ProgressUnchangedFor: 10m`, ticker at 1m → expected drip rate is 1 alert per ~16 min when stuck tasks exist.

## Key files
- `internal/alerts/engine.go:368` `evaluateStuckTasks`
- `internal/alerts/engine.go:407` `shouldFire` (per-rule, not per-task)
- `internal/alerts/types.go:209` default rule definition
- `internal/executor/alerts.go:32` unused `AlertEventTypeTaskProgress` constant
- `internal/executor/signal.go` `GetLatestProgress` (built but never called from runner)
- `cmd/pilot/handler_common.go:88` only `TaskStarted` is emitted; `TaskProgress` never
