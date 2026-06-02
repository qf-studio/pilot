# TASK-358: Dashboard "failed" count inflated by misclassified outcomes

**Status:** ✅ SHIPPED & live-verified · 2026-06-02 · released v2.166.10–11
- **#3401** — mechanism: `TerminalStatus()` classifier + 2 buckets (`no_op`, `stalled`) + idempotent `reclassifyLegacyOutcomes()` backfill + widened heal-on-merge scope.
- **#3404** — widen to production reality: 3 more buckets (`infra`, `skipped`, `rate_limited`), validated against the live DB.
- **#3407** — TUI render fix: the wide breakdown suffix overflowed the mini-card and `truncateVisual` (counted ANSI bytes as visible width) blanked the failed line on v2.166.10. Now: append the suffix only when it fits the card; `truncateVisual` made ANSI-aware.
- **Live-verified:** daemon on v2.166.11 → QUEUE card shows `✗ 234 failed` correctly. DB backfilled in place 784→234 (backup `~/.pilot/data/pilot.db.bak-task358-20260602-125509`).

**Refs:** [[TASK-320]] (executor false-negative no-op) · [[TASK-321]] (phantom blocked on already-merged) · [[TASK-355]] (board-sourced no-op false positive) · pitfall [[pitfall_dashboard_failed_count_conflation]] · learning [[learn_restart_vs_rebuild_stale_binary]]

## Operational lesson (cost us 3 restarts)

The daemon ran a **stale binary** built from an un-pulled root `main` (`7658f6b0`, pre-fix) — so "restart" kept showing 784. **A restart only picks up new code if the binary was rebuilt AND the running process was started after the rebuild.** Verify with `pilot version` + the process start time vs the binary mtime, not just "I restarted." Released v2.166.10 then v2.166.11 (render fix) via `make release V=…` (tag-only; goreleaser is sole publisher). See [[learn_restart_vs_rebuild_stale_binary]].

## Symptom

QUEUE card showed `✗ 784 failed` — far higher than real failures. Reported: "many
showed failed but were done correctly."

## Production breakdown (real DB, 2026-06-02)

The 784 "failed" rows, simulated through the final classifier precedence:

| bucket | n | meaning |
|---|---:|---|
| **failed** | **234** | genuine: quality gates, planning, title-refused, unknown exit-1 |
| infra | 305 | OOM/SIGKILL + push/PR/worktree/branch — Pilot couldn't run/land it |
| no_op | 120 | work already on base / no edits (TASK-321 phantom no-ops) |
| skipped | 81 | stale-queued (no worker) + context-canceled |
| rate_limited | 34 | provider quota hit (transient) |
| stalled | 10 | watchdog stall / budget cap |

Conservation verified: 234+305+120+81+34+10 = 784. Product call: infra is its own
bucket (not "failed"); unknown/exit-1 stays "failed" (conservative).

## Root cause

Not a display bug — a **write-path classification bug**.

- `dispatcher.go` collapsed *every* `result.Success == false` outcome into
  `status = "failed"`: declined, no-op ("no new commit produced" / `no_changes`),
  stalled, and budget-capped runs all became "failed".
- `GetLifetimeTaskCounts()` then `SUM(CASE WHEN status='failed')` — so the card
  counted all of them as failures.
- `result.Declined` was never honored by the dispatcher, so even explicit declines
  inflated the count.

## Fix

**Layer A — classify at the write path (forward):**
- `ExecutionResult.Outcome` field; set at terminal points (budget/stalled/declined/no_op).
- `TerminalStatus(result)` in `runner.go`: Success→`completed`, Declined→`declined`,
  explicit Outcome tag, then an **ordered error-signature table** (no_op → rate_limited
  → skipped → stalled → infra), else `failed`. Order = precedence (most "not a failure"
  wins). Signature lists kept in sync with the backfill SQL.
- `dispatcher.go` writes the classified status (+ matching phase label) instead of blanket `failed`.
- `UpdateExecutionStatus` treats all non-failure outcomes as terminal (stamps `completed_at`).
- Heal-on-merge scope (`UpdateExecutionStatusByTaskID`, `SelfHealExecutionAfterMerge`)
  broadened to `status IN ('failed','no_op','stalled','rate_limited','infra','skipped')`
  so reclassified rows still promote to `completed` when their PR lands.

**Layer B — backfill existing rows:**
- `reclassifyLegacyOutcomes()` runs in `migrate()` (idempotent): ordered UPDATEs,
  each guarded by `status='failed'`, reclassify historical rows by deterministic
  error signature. Genuine failures (no signature) stay `failed`.

**Dashboard:** `Failed` counts genuine failures only; the rest show as a muted
breakdown suffix ` (N no-op · M infra · K skipped · …)`, omitting zero buckets and
truncating after the headline on narrow cards. `LifetimeTaskCounts.NonFailure()`
aggregates them.

## Known limitations

- Declined rows can't be backfilled — decline reason was never persisted to
  `executions.error` (only to backend diagnostics). Forward-only.
- `infra` is a judgment bucket: a push/PR failure *might* mean the work never landed.
  The broadened heal-on-merge promotes any such row to `completed` if its PR later merges.
- Pre-existing `declined-preflight` (10) and `canceled` (3) statuses are left as-is
  (already excluded from `failed`); not surfaced in the card breakdown.

## Tests

- `executor/terminal_status_test.go` — `TerminalStatus` / `terminalPhaseLabel` tables.
- `memory/reclassify_outcomes_test.go` — backfill correctness, idempotency, no-op self-heal.
