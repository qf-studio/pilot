# SOP: Alert Deduplication — Per-Rule vs Per-Source

**Created:** 2026-04-06 (after GH-2204, v2.90.1)
**Applies to:** `internal/alerts/engine.go` and any rule that fans out over a collection of sources (tasks, PRs, projects, files).

## The trap

When a single alert rule evaluates many candidate sources (tasks, PRs, files…), gating with a **global per-rule cooldown** silently drops alerts from all-but-one source per cooldown window.

The classic symptom: alerts appear at exactly `ticker_interval + cooldown` intervals, rotating through different source IDs forever (Go map iteration is randomized, so the "rotation" looks arbitrary).

This is what GH-2204 looked like in Slack:

```
1:07 Task GH-273 stuck for 23m
1:23 Task GH-273 stuck for 39m   ← 16 min later (1m ticker + 15m cooldown)
1:39 Task GH-275 stuck for 36m
1:55 Task GH-41  stuck for 40m
...
```

## The pattern

When a rule fans out over N sources, dedup must be **per source**, not per rule:

```go
// WRONG — per-rule cooldown gates per-source iteration
for _, src := range sources {
    if shouldFire(rule) {        // global key: rule.Name
        fireAlert(rule, src)
    }
}

// RIGHT — per-source dedup
for _, src := range sources {
    if now.Sub(src.LastAlertedAt) >= rule.Cooldown {
        fireAlert(rule, src)
        src.LastAlertedAt = now
    }
}
```

The per-rule `shouldFire` is fine for **scalar rules** that fire on one global event (`task_failed`, `daily_spend`, `circuit_breaker_trip`). It is wrong for any rule that loops over sources.

## When you need both

If you also want a global ceiling (e.g., "never more than 1 alert/min total to avoid Slack rate limits"), keep per-source dedup as the primary gate and add a **secondary global rate limiter**:

```go
if !rateLimiter.Allow() { return }   // global ceiling
for _, src := range sources {
    if now.Sub(src.LastAlertedAt) >= rule.Cooldown {
        ...
    }
}
```

Or aggregate: emit one summary alert per cycle (`"5 tasks stuck >10min: A, B, C, D, E"`) instead of N individual alerts. Configurable via `rule.Condition.AggregateAlerts`.

## Reset on progress

For "stuck" rules, **clear `LastAlertedAt` whenever the source advances**, not just on the cooldown timer. Otherwise a task that gets stuck → unstuck → stuck again won't re-alert until the cooldown elapses.

```go
func handleTaskProgress(event Event) {
    state := taskLastProgress[event.TaskID]
    if event.Progress > state.Progress {
        state.LastAlertedAt = time.Time{}   // reset dedup
        state.Progress = event.Progress
        state.UpdatedAt = event.Timestamp
    }
}
```

## Orphan eviction

Maps keyed by source ID need explicit cleanup. Completion/failure events are not guaranteed (process killed mid-run, hot upgrade, dispatcher path drops the event, panic). Always evict entries older than `N × threshold` to prevent permanent zombies.

```go
const orphanMultiplier = 4
for id, state := range taskLastProgress {
    if now.Sub(state.UpdatedAt) > orphanMultiplier*threshold {
        delete(taskLastProgress, id)
        log.Info("evicted orphan task", "task_id", id, "age", ...)
    }
}
```

## Wiring check before adding any "stuck" rule

Before merging a rule that depends on "no progress for X", verify the progress events actually flow:

```bash
# Find emit sites
rg 'EventType<RuleSubject>.*ProcessEvent|Process.*EventType<RuleSubject>' internal/ cmd/

# Should return at least one CALL site, not just the constant declaration
```

If only the constant exists (and a test that enumerates constants), the rule is wired to nothing — every source will appear stuck after the threshold elapses. **This is the GH-2204 bug.** Catch it in code review.

## Checklist

When adding or reviewing a fan-out alert rule:

- [ ] Dedup is per source ID, not per rule name
- [ ] `LastAlertedAt` (or equivalent) is reset when the source advances
- [ ] Map entries get evicted on a TTL, not just on completion events
- [ ] The trigger event (`progress`, `update`, `heartbeat`) is actually emitted somewhere — not just the constant declared
- [ ] Tests cover: N stuck sources fire N alerts (or 1 aggregated), same source within cooldown does not re-fire, source that advances resets dedup, orphan TTL evicts stale entries
- [ ] If aggregation is wanted, `rule.Condition.AggregateAlerts: true` is supported and tested

## Key files

- `internal/alerts/engine.go` — `evaluateStuckTasks`, `handleTaskProgress`, `progressState.LastAlertedAt`
- `internal/alerts/types.go` — `RuleCondition`, default rule definitions
- `internal/executor/runner.go` — `reportProgress` emit site for `AlertEventTypeTaskProgress`
- `internal/executor/signal.go` — `SignalParser.GetLatestProgress` (parses `pilot-signal` JSON blocks)

## History

- GH-2204 / v2.90.1 — Fixed all three bugs in one commit. Filed as a Slack flood: 13 alerts in ~3.5 hours, every task at 0%.
