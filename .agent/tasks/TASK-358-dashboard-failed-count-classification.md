# TASK-358: Dashboard "failed" count inflated by misclassified outcomes

**Status:** 🟢 implemented (PR on `fix/dashboard-failed-classification`) · 2026-06-02
**Refs:** [[TASK-320]] (executor false-negative no-op) · [[TASK-321]] (phantom blocked on already-merged) · [[TASK-355]] (board-sourced no-op false positive)

## Symptom

QUEUE card showed `✗ 784 failed` — far higher than real failures. Reported: "many
showed failed but were done correctly."

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
  Outcome tag → `no_op`/`stalled`, else error-signature fallback, else `failed`.
- `dispatcher.go` writes the classified status (+ phase label) instead of blanket `failed`.
- `UpdateExecutionStatus` treats `no_op` as terminal (stamps `completed_at`).
- Heal-on-merge scope (`UpdateExecutionStatusByTaskID`, `SelfHealExecutionAfterMerge`)
  broadened to `status IN ('failed','no_op','stalled')` so reclassified rows still
  promote to `completed` when their PR lands.

**Layer B — backfill existing rows:**
- `reclassifyLegacyOutcomes()` runs in `migrate()` (idempotent): reclassifies
  historical `failed` rows by deterministic error signature into `no_op`/`stalled`.
  Genuine failures (no signature) stay `failed`.

**Dashboard:** `Failed` now counts genuine failures only; `NoOp`/`Stalled`/`Declined`
shown as a muted ` (N no-op · M stalled · K declined)` suffix so numbers reconcile.

## Known limitations

- Declined rows can't be backfilled — decline reason was never persisted to
  `executions.error` (only to backend diagnostics). Forward-only.
- Rows where work merged but was recorded as a *generic* failure (no no-op signature)
  are healed at merge-time via the broadened self-heal, not by the backfill.

## Tests

- `executor/terminal_status_test.go` — `TerminalStatus` / `terminalPhaseLabel` tables.
- `memory/reclassify_outcomes_test.go` — backfill correctness, idempotency, no-op self-heal.
