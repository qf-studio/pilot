> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-308: Event-based stall timer (last-event vs global-elapsed)

**Status**: queued
**Created**: 2026-05-26
**Severity**: P1
**Effort**: S (~2h)
**Job (JTD)**: J4 Course-correct
**Source**: Symphony research, Wave 5 / `~/.claude/plans/let-s-plan-that-use-staged-seal.md`

---

## Context

**Problem**: Pilot's current stall detection (where it exists) measures global-elapsed turn time. A live-but-slow agent (large file edit, slow tool call, long thinking turn) can be killed prematurely. Conversely, a genuinely hung agent (deadlocked on a tool call, infinite loop) goes undetected until the global timeout — by which point Pilot has wasted budget.

**Goal**: Stall detection measured against **last meaningful event** (turn output, tool call, tool result), not global elapsed. Threshold (e.g., `stall_timeout_ms=180_000`) kills only sessions with no event activity in that window. Long-but-live runs survive; hung runs die fast.

Borrowed from Symphony's `codex.stall_timeout_ms` (`/tmp/symphony/SPEC.md` lines 783–789).

**Why now**: Adjacent to TASK-302 (PR reconciliation) and the Wave 4 reliability theme. Cheap to implement, big trust win.

---

## Acceptance Criteria

- [ ] Executor tracks a `last_event_at` timestamp per running session, updated on each agent event (output chunk, tool call, tool result).
- [ ] Stall watchdog (configurable interval, default 30s) checks `now - last_event_at > stall_timeout`; if true, terminates session with `stalled` status.
- [ ] Stall threshold configurable per executor profile (default 3 minutes).
- [ ] Session-terminated-as-stalled is distinguishable from session-failed-as-error in `executions.status` and dashboard.
- [ ] Long-but-live sessions (e.g., 5-minute file write with periodic output) are NOT killed by the watchdog.

---

## Implementation

### Phase 1: Event-time tracking
**Tasks**:
- [ ] In `internal/executor/runner.go` (or wherever the Claude Code stream is read), record `lastEventAt time.Time` per session.
- [ ] Update on each chunk/event from the agent stream.

**Files**:
- `internal/executor/runner.go`

### Phase 2: Watchdog goroutine
**Tasks**:
- [ ] Spawn a `safeGo()` watchdog per session (use existing pattern, see TASK-292) that ticks every 30s.
- [ ] On tick: if `now - lastEventAt > stallTimeout`, kill the session (cancel context).
- [ ] Mark execution status `stalled` (new value), distinct from `failed`.

**Files**:
- `internal/executor/runner.go` (or `internal/executor/watchdog.go` if cleaner)

### Phase 3: Config + observability
**Tasks**:
- [ ] Add `executor.stall_timeout_ms` (default 180000) to config schema.
- [ ] Add new `executions.status = 'stalled'` value; backfill safe (new value, no migration needed beyond enum docs).
- [ ] Emit Prometheus counter `pilot_executor_stalled_total` and surface in dashboard HISTORY panel as a distinct icon.

**Files**:
- `internal/config/`
- `internal/memory/store.go` (status enum doc)
- `internal/dashboard/tui.go` (HISTORY icon for stalled)

---

## Out of Scope

- Per-tool stall budgets (e.g., "if bash call hangs for X, kill"). v1 is whole-session granularity.
- Automatic restart on stall — stalled means failed; user/autopilot decides retry.
- Stall heuristics beyond simple time delta.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Event definition | Any byte, only structured events, tool calls only | Any chunk/event from agent stream | Simplest; long file writes still emit chunks |
| Watchdog frequency | Per-event check, periodic tick | 30s tick | Lower overhead; granularity sufficient |
| Default threshold | 60s, 180s, 300s, 600s | 180s | Per Symphony spec default; balances trust vs false-positive |
| Status value | reuse `failed`, new `stalled` | New `stalled` | Distinguishable; supports targeted alerting |

---

## Files Affected (estimate)

- `internal/executor/runner.go`
- `internal/executor/watchdog.go` (new, optional)
- `internal/config/`
- `internal/memory/store.go`
- `internal/dashboard/tui.go`

---

## Verify

```bash
go test ./internal/executor/...

# Manual: spawn a deliberately-hung agent (e.g., infinite-loop tool mock); verify it dies at threshold.
# Manual: spawn a long-but-streaming agent (large file write); verify it survives.
```

---

## Done

- [ ] `lastEventAt` tracked per session
- [ ] Watchdog kills idle sessions at threshold
- [ ] `executions.status = 'stalled'` surfaced in dashboard
- [ ] Long-but-live sessions don't trip the watchdog (verified manually)
- [ ] Prometheus counter emits

---

## Refs

- Master plan: `~/.claude/plans/let-s-plan-that-use-staged-seal.md`
- Symphony evidence: `/tmp/symphony/SPEC.md` lines 783–789 (`stall_timeout_ms`)
- Related: `TASK-292` (`safeGo()` panic-recovery — use that pattern for the watchdog)
- Adjacent: `TASK-302` (PR reconciliation, similar reliability theme)

---

**Last Updated**: 2026-05-26
