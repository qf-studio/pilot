# TASK-343: memory/store integrity cluster (D3 + D5 + D6 + D7)

Wave 3 batched issue. Four `internal/memory` integrity findings batched into ONE PR
because D3/D5/D6 all edit `store.go` (parallel issues on the same file → merge conflicts
→ phantom-block churn). D7 also touches `metrics.go`/`metering.go` in the same package.

## Context

Four confirmed-live integrity bugs in the memory package (TASK-322 audit, `memory-store` /
`error-resource` dims):

- **D3 — cross-repo task_id collision.** `UpdateExecutionStatusByTaskID` (`store.go:1080`) and
  `SelfHealExecutionAfterMerge` (`store.go:1096`) `UPDATE ... WHERE task_id = ?` with **no
  `project_path` qualifier**. The autopilot caller derives `taskID := fmt.Sprintf("GH-%d", IssueNumber)`,
  which is only unique per repo. In a multi-repo deployment, merging PR for issue #42 in repo A
  silently promotes a `failed` row for issue #42 in repo B to `completed` and stamps repo A's PR
  URL onto repo B's row. Every other write in the file is keyed by unique `id` or `(task_id, project_path)`.
- **D5 — unbounded `execution_logs`/`executions` growth.** `SaveLogEntry` writes a row per
  assistant/milestone on the hot path (`runner.go:994`), but there is **no `PruneExecutionLogs`**
  and no retention sweep. Pruning exists only for `autopilot_metrics` (`PruneAutopilotMetrics`,
  `store.go:1850`), `approval_pending`, and `memories`. On a long-running daemon the table grows
  without bound, bloating the single SQLite file every read/write serializes through (`MaxOpenConns=1`).
- **D6 — non-atomic `RecordPatternFeedback`.** `store.go:1431` does three independent writes
  (INSERT pattern_feedback, UPDATE cross_patterns.confidence, UPDATE pattern_projects counts), each
  in its own `withRetry`/`Exec`, with the two follow-up updates' errors discarded via `_ = s.withRetry(...)`.
  A crash between steps leaves the feedback row recorded but confidence/counters unadjusted —
  silent learning-signal drift (0 `BeginTx` in store.go today).
- **D7 — missing `rows.Err()` checks.** Almost every `for rows.Next()` loop defers `Close()` but
  never calls `rows.Err()`. `rows.Next()` returns false on both normal completion AND mid-stream
  driver/disk error; without `rows.Err()` a truncated result set is returned as success. Current
  coverage: `store.go` 2/14 loops, `metrics.go` 0/5, `metering.go` 0/3. The codebase knows the
  pattern (`backend_anthropic.go:492` checks `scanner.Err()`) — it just wasn't applied to SQL.

## Approach

- **D3:** Add a `projectPath` parameter to `UpdateExecutionStatusByTaskID` and
  `SelfHealExecutionAfterMerge`, append `AND project_path = ?` to both WHERE clauses, and thread the
  merged PR's repo/project through the autopilot caller. If `project_path` is genuinely unavailable at
  a call site, scope the UPDATE to the single most-recent matching row (`ORDER BY created_at DESC LIMIT 1`
  via subquery) instead of all `failed` rows.
- **D5:** Add `PruneExecutionLogs(olderThan time.Duration) (int64, error)` (and optionally a row-cap
  variant) plus an age/cap prune for terminal `executions`; schedule on the same periodic sweep that
  runs `PruneAutopilotMetrics`. Run `PRAGMA wal_checkpoint(TRUNCATE)` after large prunes.
- **D6:** Wrap all three statements in a single `sql.Tx` (`BeginTx` → Exec×3 → Commit) inside
  `withRetry`, and propagate the update errors instead of discarding them.
- **D7:** After each iteration loop add `if err := rows.Err(); err != nil { return nil, fmt.Errorf("<query> row iteration: %w", err) }` before returning, systematically across
  `store.go`, `metrics.go`, `metering.go` (and `knowledge.go`/`eval.go` if loops exist there).

## Acceptance

- [ ] D3: both methods take `projectPath` and filter `AND project_path = ?`; caller threads repo/project; test asserts a `failed` GH-42 row in repo B is NOT promoted when repo A's #42 merges.
- [ ] D5: `PruneExecutionLogs` exists, is wired into the periodic sweep, and has a test asserting rows older than the cutoff are deleted and recent rows retained.
- [ ] D6: `RecordPatternFeedback` runs in one `sql.Tx`; a forced mid-write failure rolls back the feedback insert; update errors propagate (no `_ =`).
- [ ] D7: every `for rows.Next()` loop in `store.go`/`metrics.go`/`metering.go` checks `rows.Err()`; counts become 14/14, 5/5, 3/3.
- [ ] `make test` green for `internal/memory`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (D3/D5/D6/D7 entries)
- Roadmap: `.agent/tasks/TASK-322-remediation-roadmap.md` § Wave 3
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md` (store.go cluster gate #1)
- Files: `internal/memory/store.go` (1080, 1096, 1431, 1850, +rows.Err loops), `internal/memory/metrics.go`, `internal/memory/metering.go`
