> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-309: Autopilot `releasing` stage stuck forever — purge predicate + scan re-registration + handler retry

**Wave:** 5 (M) · **Severity:** P1 (cosmetic now, structural state leak) · **Source:** observation 2026-05-26 (PR #3130, plus historical residue from PR #3107, #3101)

---

## Problem

`autopilot_pr_state` rows stay at `stage='releasing'` indefinitely after the PR is merged on GitHub. Observed for PR #3130 (TASK-294-redo): merged 2026-05-26 ~14:58 UTC, row still at `stage='releasing', updated_at=14:58:52` 20+ minutes later. Same pattern on historical PRs #3107 and #3101.

Side effect: `autopilot_metrics.active_prs` stays at `1` indefinitely (the `activePRs` map isn't pruned because `handleReleasing`/`removePR` never runs to completion).

## Root cause (single root, four interacting bugs)

Researched via navigator agent. The releasing stage has *four* compounding problems, any one of which would create the stuck state; together they make recovery impossible without manual SQL.

### B1 — `checkExternalMergeOrClose` ejects before `handleReleasing` runs

`internal/autopilot/controller.go:2425-2473`. Every tick, `processAllPRs` calls `checkExternalMergeOrClose` (line 2394) BEFORE `ProcessPR` (line 2414). When `ghPR.Merged=true` AND `prState.Stage == StageReleasing`, the condition at line 2461 falls through to `c.removePR(prState.PRNumber)` (line 2472) and returns `true`. `ProcessPR` is never called; `handleReleasing` never runs. The PR is in this state from tick 2 onward forever.

### B2 — `handleReleasing` returns error → state frozen, no retry budget

`internal/autopilot/controller.go:1681-1683`. `GetPRCommits` (or `GetTagForSHA`/`CreateTagForRepo`) error → `handleReleasing` returns the error. `recordPRFailure` fires. Stage stays `StageReleasing` (the function never advanced). `persistPRState` writes the frozen row. There's no `handleReleasing`-specific retry — only the global per-PR circuit breaker (`MaxFailures=3`, types.go:317) which only stops `ProcessPR` from running, not the stage transition.

### B3 — `ScanRecentlyMergedPRs` re-registers the PR after cleanup

`internal/autopilot/controller.go:2236-2279`. The scan skip gate (line 2238) only checks the in-memory `activePRs` map. After `removePR` deletes from `activePRs` (B1 path) but DB still has `stage='releasing'`, the next scan re-registers the PR at `stage=StageReleasing` (line 2277) and writes a new DB row. Then on the next tick, B1 fires again. Net effect: row deleted and re-created every `mergedScanInterval` (default 5 min), always at `releasing`.

### B4 — `PurgeTerminalPRStates` only purges `failed`

`internal/autopilot/state_store.go:451`. The cleanup query only filters on `stage='failed'`. `stage='releasing'` rows are never auto-purged — they're immortal until an explicit `RemovePRState` succeeds. Combined with B3's silent failures, the row never clears.

## Approach

### Step 1 — B1 fix (highest impact, ~30 min)

`internal/autopilot/controller.go:2461` — change the ejection logic so that when `prState.Stage == StageReleasing`, we DO NOT call `removePR`. Instead return `false` (or `continue`) so `ProcessPR` proceeds to `handleReleasing` for completion.

```go
// Before (releasing PR ejected without finishing):
if ghPR.Merged && prState.Stage != StageMerging {
    c.removePR(prState.PRNumber)
    return true
}

// After (let releasing PRs reach their handler):
if ghPR.Merged && prState.Stage != StageMerging && prState.Stage != StageReleasing {
    c.removePR(prState.PRNumber)
    return true
}
```

### Step 2 — B2 fix (~60 min)

Add `handleReleasing`-specific retry budget separate from the global circuit breaker:

- Up to 3 attempts with 30s backoff between
- If exhausted, log structured error AND call `removePR` to free the slot (don't leave the row at `releasing` forever)
- New counter: `pilot_autopilot_releasing_retry_total{outcome="success"|"exhausted"}`

### Step 3 — B3 fix (~30 min)

`internal/autopilot/controller.go:2236-2241` — extend the skip gate to also query `stateStore.GetPRState(pr.Number)`. Skip re-registration if a row exists at `stage='releasing'` AND `updated_at` is recent (within `2 × mergedScanInterval`). Otherwise the scan correctly catches abandoned-tracker cases.

### Step 4 — B4 fix (~15 min)

`internal/autopilot/state_store.go:451` — broaden `PurgeTerminalPRStates` to also purge `stage='releasing'` rows older than 30 minutes (configurable). This is a safety net for any future bug that still leaves orphans.

### Step 5 — Manual cleanup for existing stuck rows (~15 min)

One-off SQL or `pilot autopilot cleanup-releasing` admin command:

```sql
DELETE FROM autopilot_pr_state
WHERE stage = 'releasing'
  AND updated_at < datetime('now', '-30 minutes');
```

### Step 6 — Tests (~90 min)

- Unit (`controller_test.go`): merged PR with stage=releasing → assert `handleReleasing` runs, not `removePR`
- Unit: `handleReleasing` returns error 3x → assert `removePR` called, stage cleared from DB
- Unit: scan re-registration → assert PR with existing `releasing` row is skipped
- Unit (`state_store_test.go`): `PurgeTerminalPRStates` removes a 31-min-old `releasing` row

### Step 7 — Manual smoke (~30 min)

- Stop daemon, manually insert a `releasing` row for a real merged PR
- Start daemon, observe row transitions to `done`/deleted within 1-2 ticks
- Verify metric `active_prs` returns to 0 if no other active PRs

## Files

- `internal/autopilot/controller.go` (B1 fix at line 2461; B2 retry budget around line 1681; B3 skip gate at line 2238)
- `internal/autopilot/state_store.go` (B4 purge predicate at line 451)
- `internal/autopilot/controller_test.go` (extend)
- `internal/autopilot/state_store_test.go` (extend)
- `internal/autopilot/metrics.go` (new `ReleasingRetry` counter)
- Optional: new `cmd/pilot/autopilot_cleanup.go` admin command

## Out of scope

- **Observing release-workflow completion** (GoReleaser CI / Docker / Desktop). Current behavior: `handleReleasing` considers itself done as soon as `CreateTagForRepo` returns success. Whether to extend autopilot to also watch the downstream CI workflows is a separate (larger) decision; this task only ensures stage transitions clear properly.
- The duplicated TASK-302 commit pattern (`bd392b11` shipped 13 min after PR #3119 squash-merge) — separate cleanup task if recurring.

## Related memories

- `bug_autopilot_pr_discovery_orphans.md` — sister bug, TASK-302 fixed it for PR-discovery; this task fixes it for PR-transition.
- `bug_pilot_ghost_closes.md` — same theme: subsystems with inconsistent notions of "this work is alive".

## Wave 5 status

This is the third autopilot reliability gap surfaced this week (TASK-302 = orphan-PR registration; TASK-301 = pilot-done timing; TASK-309 = releasing-stage drain). All three share the same architectural root: the autopilot state machine has fragmented invariants. After TASK-309 lands, the autopilot side is structurally consistent; the executor side still has TASK-300 + TASK-301 pending.
