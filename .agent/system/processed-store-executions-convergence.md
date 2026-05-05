# ProcessedStore vs executions — Divergent Semantics & Convergence Design

Phase 1 of GH-2591. Phase 2 (implementation) tracking issue: **TODO — create "fix(poller): converge ProcessedStore and executions to single dispatch predicate (Phase 2 of #2591)"**

---

## 1. Current State Map

### 1a. `autopilot_processed` table (GitHub poller's ProcessedStore)

**Location:** `internal/autopilot/state_store.go`

**Schema:**
```sql
CREATE TABLE IF NOT EXISTS autopilot_processed (
    issue_number INTEGER PRIMARY KEY,
    processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    result       TEXT     DEFAULT ''
)
```

**Read sites:**
- `internal/autopilot/state_store.go:268` — `IsIssueProcessed` — SELECT COUNT(*) for a single issue
- `internal/autopilot/state_store.go:277` — `LoadProcessedIssues` — SELECT all; used at poller startup
- `internal/adapters/github/poller.go:287` — calls `LoadProcessedIssues` to hydrate the in-memory `p.processed` map on boot

**Write sites:**
- `internal/autopilot/state_store.go:249` — `MarkIssueProcessed` — INSERT/upsert (called via poller)
- `internal/autopilot/state_store.go:261` — `UnmarkIssueProcessed` — DELETE row
- `internal/autopilot/state_store.go:779` — `PurgeOldProcessed` — DELETE rows older than a cutoff
- `internal/adapters/github/poller.go:1051` — mark on successful dispatch
- `internal/adapters/github/poller.go:675` — unmark to allow retry (failed-label removed)
- `internal/adapters/github/poller.go:1065` — unmark on execution failure (GH-2176)

**Semantics for dispatch decisions:**
A row in `autopilot_processed` means "the poller dispatched (or decided to skip) this issue number." The poller also maintains an in-memory mirror (`p.processed map[int]time.Time`). On each poll cycle, any issue whose number is in the in-memory map is skipped before querying GitHub or SQLite. On restart the map is re-hydrated from this table. The table makes the "skip" durable across restarts.

---

### 1b. `adapter_processed` table (generic multi-adapter ProcessedStore)

**Location:** `internal/autopilot/state_store.go`

**Schema:**
```sql
CREATE TABLE IF NOT EXISTS adapter_processed (
    adapter      TEXT NOT NULL,
    issue_id     TEXT NOT NULL,
    processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    result       TEXT DEFAULT '',
    PRIMARY KEY (adapter, issue_id)
)
```

**Read sites:**
- `internal/autopilot/state_store.go:671` — `IsAdapterProcessed` — SELECT COUNT(*)
- `internal/autopilot/state_store.go:680` — `LoadAdapterProcessed` — SELECT all issue_ids for adapter

**Write sites:**
- `internal/autopilot/state_store.go:653` — `MarkAdapterProcessed` — INSERT/upsert
- `internal/autopilot/state_store.go:664` — `UnmarkAdapterProcessed` — DELETE
- `internal/autopilot/state_store.go:700` — `PurgeOldAdapterProcessed` — DELETE stale rows

**Semantics:** The generic successor to `autopilot_processed` (GH-1838). Covers all adapters (Jira, Linear, Azure DevOps, etc.) with string-typed issue IDs. The GitHub poller still uses `autopilot_processed` (integer keys) via the `ProcessedStore` interface — it does NOT use `adapter_processed` directly. Both tables serve the same conceptual role: "dispatch guard."

---

### 1c. `executions` table (run-time state and outcome store)

**Location:** `internal/memory/store.go`

**Schema (condensed):**
```sql
CREATE TABLE IF NOT EXISTS executions (
    id               TEXT PRIMARY KEY,
    task_id          TEXT NOT NULL,      -- e.g. "GH-2591"
    project_path     TEXT NOT NULL,
    status           TEXT NOT NULL,      -- queued | pending | running | completed | failed
    output           TEXT,
    error            TEXT,
    duration_ms      INTEGER,
    pr_url           TEXT,
    commit_sha       TEXT,
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at     DATETIME,
    -- metrics (added via ALTER TABLE):
    tokens_input     INTEGER,
    tokens_output    INTEGER,
    tokens_total     INTEGER,
    estimated_cost_usd REAL,
    files_changed    INTEGER,
    lines_added      INTEGER,
    lines_removed    INTEGER,
    model_name       TEXT,
    -- task queue snapshot:
    task_title       TEXT,
    task_description TEXT,
    task_branch      TEXT,
    task_base_branch TEXT,
    task_create_pr   BOOLEAN,
    task_verbose     BOOLEAN,
    task_source_adapter  TEXT,
    task_source_issue_id TEXT,
    task_labels      TEXT
)
```

**Read sites:**
- `internal/memory/store.go:486` — `HasCompletedExecution` — SELECT COUNT(*) WHERE status='completed' AND error IS NULL
- `internal/memory/store.go:450` — `GetExecution` — by ID
- `internal/memory/store.go:517` — `GetRecentExecutions` — ORDER BY created_at DESC LIMIT N
- `internal/memory/store.go:725` — `GetExecutionsInPeriod` — time-range query
- `internal/memory/store.go:763` — `GetActiveExecutions` — WHERE status='running'
- `internal/memory/store.go:850` — `GetQueuedTasks` — WHERE status IN ('queued','pending')
- `internal/memory/store.go:882` — `GetQueuedTasksForProject` — per-project queue
- `internal/memory/store.go:1067` — `IsTaskQueued` — SELECT COUNT(*) WHERE task_id=? AND status IN (...)
- `internal/adapters/github/poller.go:917` — `execChecker.HasCompletedExecution(taskID, projectPath)` — GH-2242 guard before dispatch

**Write sites:**
- `internal/memory/store.go:407` — `CreateExecution` — INSERT on dispatch
- `internal/memory/store.go:930` — `UpdateExecutionStatus` — UPDATE to terminal state (sets completed_at)
- `internal/memory/store.go:954` — `UpdateExecutionStatusByTaskID`
- `internal/memory/store.go:970` — `SelfHealExecutionAfterMerge` — UPDATE failed→completed after merge detected
- `internal/memory/store.go:985` — `UpdateExecutionResult` — UPDATE pr_url, commit_sha, duration_ms
- `internal/memory/store.go:502` — `InvalidateCompletion` — DELETE completed rows (allows re-dispatch)
- `internal/memory/store.go:1058` — `DeleteExecution`
- `internal/memory/metrics.go:204` — UPDATE token/cost metrics

**Semantics for dispatch decisions:**
`HasCompletedExecution(taskID, projectPath)` returns true when a row exists with `status='completed'` AND `error=''`. The GH-2242 guard in the poller calls this before dispatching: if true, the issue is marked processed and skipped. This is a defense-in-depth guard for when the `pilot-done` GitHub label failed to apply.

---

## 2. Divergence Catalog

| Question | `autopilot_processed` answer | `executions` answer | Footgun |
|---|---|---|---|
| "Should I dispatch this issue?" | Row absent → dispatch | `HasCompletedExecution` false → dispatch | Either alone can prevent re-dispatch; both must be absent to retry |
| "Is execution currently running?" | No signal (no in-progress column) | `status='running'` | Poller can dispatch a second time if ProcessedStore row was cleared but execution is still running |
| "Did this issue get a PR merged?" | No signal (only "processed") | `pr_url` column, but not authoritative (squash-merge lie) | Neither table reliably knows whether the PR actually merged |
| "Did dispatch succeed?" | Row added after `executor.Run()` is called | Row created at `CreateExecution` before run | Different timing; ProcessedStore row is added even for queued/pending runs |
| "Is this a genuine completion?" | No concept of partial/orphan | `status='completed'` with non-empty `error` field = orphan recovery | `HasCompletedExecution` explicitly excludes error≠'' rows (GH-2315) |
| "Can I retry after pilot-failed label?" | Row deleted via `UnmarkIssueProcessed` | Completed row survives; must be manually deleted | `InvalidateCompletion` must also be called; if only one is cleared, the other re-blocks |
| "Which adapter owns this issue?" | Table is GitHub-specific (integer key) | `task_source_adapter` + `task_source_issue_id` columns | No join possible; cleanup must happen in two separate stores |

---

## 3. Failure Modes That Motivated This Ticket

### 3a. Ghost-close DB lockout (GH-2382, fixed GH-2476)
`executions` row with `status='completed'` was written by orphan-recovery even though no PR was created. The `HasCompletedExecution` guard at `poller.go:917` then silently skipped the issue forever. The fix (GH-2315) added the `error IS NULL` condition to `HasCompletedExecution`, but the root cause — `executions` carrying two different meanings of "completed" — remained.

Memory cross-reference: `bug_ghost_close_db_lockout.md`

### 3b. Cascade #2 ghost rows (2026-05-04)
The cascade-2 executor prompt leak caused multiple issues to be dispatched and written as `completed` in `executions` without actual PRs being merged. The `autopilot_processed` rows were cleaned by the `pilot-failed` label removal flow, but `executions` rows were not, leaving a split state where ProcessedStore said "retry OK" but `executions` said "already done."

Memory cross-reference: `incident_oauth_cascade_series.md`

### 3c. Stale `pilot-in-progress` labels (GH-2589)
Labels were used as an additional "is this in flight?" signal outside both tables. When labels became stale (e.g., process crash), neither table could be used to recover the correct state because the poller's dispatch check path consults ProcessedStore first and only reaches `HasCompletedExecution` if ProcessedStore is empty — there is no "running" predicate in the path.

### 3d. Squash-merge `mergedAt: null` confusion
`gh pr view` returns `mergedAt: null` after squash merge. The `executions.pr_url` field is populated from the executor's output, but whether the PR was actually merged cannot be determined from either table without a separate GitHub API call.

Memory cross-reference: `pattern_squash_merge_mergedat_null.md`, `feedback_verify_pr_state_not_labels.md`

---

## 4. Convergence Proposal

**Chosen: Option C — define a single canonical predicate `IsIssueDispatched(issueID string)`**

### Rationale for Option C over A/B

- **Option A** (executions-only): Migration risk is high. `executions` lives in `~/.pilot/data/pilot.db` (memory store), but `autopilot_processed` lives in the autopilot state store. Moving all dispatch-guard logic to `executions` requires merging two SQLite files or changing the open path in production. Existing `executions` rows in user databases have no `adapter` column — backfill is needed before the guard can work correctly for multi-adapter deployments. High blast radius.

- **Option B** (ProcessedStore-only): `executions` is also a task queue. `status IN ('queued','pending','running')` is how the executor knows what to run. Demoting `executions` to runtime-only requires either duplicating queue state in ProcessedStore or removing the queue feature entirely.

- **Option C** minimizes migration scope: the two tables keep their existing roles but the poller never reads either directly. A single function encapsulates both checks and can be evolved without changing callers.

### Canonical predicates

```
IsIssueDispatched(issueID, projectPath) → bool
    := autopilot_processed row exists (for GitHub: issue_number)
       OR executions row with status='completed' AND error='' exists for (task_id, project_path)

IsExecutionInFlight(issueID, projectPath) → bool
    := executions row with status IN ('running','queued','pending') exists for (task_id, project_path)
    NOTE: ProcessedStore has no running signal — this predicate uses executions exclusively.

ShouldRedispatch(issueID, projectPath) → bool
    := NOT IsIssueDispatched(issueID, projectPath)
       AND NOT IsExecutionInFlight(issueID, projectPath)
```

### Impact on poller dispatch path

Current path in `poller.go` (two separate checks, different code paths):
1. `if p.processed[number]` (in-memory ProcessedStore mirror) → skip
2. `if execChecker.HasCompletedExecution(taskID, projectPath)` (GH-2242 guard) → skip + mark

Proposed path (one predicate, same skip logic):
1. `if store.IsIssueDispatched(issueID, projectPath)` → skip
2. `if store.IsExecutionInFlight(issueID, projectPath)` → skip (currently missing — closes the double-dispatch window on process restart)

### Impact on retry path

Current retry path requires callers to clear BOTH stores:
- `UnmarkIssueProcessed(number)` (ProcessedStore)
- `InvalidateCompletion(taskID, projectPath)` (executions)

Proposed: single `ClearDispatchState(issueID, projectPath)` that wraps both deletes atomically in a transaction.

---

## 5. Migration Plan (high-level)

### Estimated diff size
~150–250 lines across 3–4 files:
- `internal/adapters/github/poller.go` — replace two dispatch checks with `IsIssueDispatched` call; replace `unmarkProcessed` calls with `ClearDispatchState`
- `internal/autopilot/state_store.go` or a new `internal/memory/dispatch_store.go` — add `IsIssueDispatched`, `IsExecutionInFlight`, `ClearDispatchState` (cross-store transactions require both DB handles)
- `internal/adapters/github/poller_integration_test.go` — update `mockProcessedStore` interface

### Backward compatibility
- Both `autopilot_processed` and `executions` tables remain; no DROP TABLE.
- Existing rows in user databases continue to be read by `IsIssueDispatched` — no data migration needed.
- The in-memory `p.processed` map in the poller can be removed (it mirrors `autopilot_processed`), but can also be kept as a hot-path cache. Recommend keeping in Phase 2 to reduce risk.

### Rollback path
Option C is additive — the new predicate functions wrap existing queries. Rolling back means reverting the poller to call the two existing functions directly. No schema changes to roll back.

### Test strategy

**Existing tests that already cover relevant paths:**
- `internal/autopilot/state_store_test.go:188–216` — `MarkIssueProcessed` / `LoadProcessedIssues` round-trip
- `internal/autopilot/state_store_test.go:735–745` — processed state persistence
- `internal/adapters/github/poller_integration_test.go:544` — `mockProcessedStore` used in poller dispatch tests

**New tests needed for Phase 2:**
- `TestIsIssueDispatched_ProcessedStoreOnly` — row in `autopilot_processed`, no `executions` row → true
- `TestIsIssueDispatched_ExecutionsOnly` — no ProcessedStore row, completed execution row → true
- `TestIsIssueDispatched_Neither` — both absent → false
- `TestIsExecutionInFlight_Running` — `status='running'` row → true
- `TestIsExecutionInFlight_Queued` — `status='queued'` row → true
- `TestClearDispatchState_ClearsBoth` — both rows deleted atomically; verify re-dispatch unblocked
- `TestDoubleDispatchPrevented` — poller integration test: two poll cycles after crash, `IsExecutionInFlight` prevents second dispatch during first run

---

## 6. Open Questions

1. **DB file boundary.** `autopilot_processed` is in the autopilot StateStore (`~/.pilot/data/autopilot.db` or the same file?); `executions` is in `memory.Store`. If they use separate SQLite files, `ClearDispatchState` cannot use a single SQLite transaction. Needs confirmation before Phase 2 starts. If separate, the predicate must accept eventual consistency between the two stores.

2. **In-memory cache coherence.** The poller's `p.processed map[int]time.Time` is a hot-path cache that skips the DB read entirely. After introducing `IsIssueDispatched`, should this cache be removed, retained, or replaced with a TTL cache that covers both stores? Removing it simplifies the code but adds one DB read per issue per poll cycle.

3. **`autopilot_processed` purge semantics.** `PurgeOldProcessed` deletes rows older than a configurable cutoff (default unknown — see `state_store.go:779`). After purge, a long-lived issue would return to "not dispatched" state. This is the current behavior; Option C inherits it. Is this intentional? If so, the purge interval needs to be longer than the longest expected execution time.

4. **Multi-adapter scope.** `adapter_processed` (generic) uses string issue IDs and an `adapter` column. `IsIssueDispatched` as designed above only covers GitHub (integer issue numbers) and `executions` (task_id = "GH-N"). Should Phase 2 also converge `adapter_processed` for Jira/Linear/etc., or keep that as a separate Phase 3?

5. **`SelfHealExecutionAfterMerge`.** `store.go:970` updates a `failed` execution to `completed` after autopilot detects a merged PR. If `IsIssueDispatched` treats `completed` as "dispatched," this self-heal path implicitly blocks re-dispatch. Is this the desired behavior, or should self-healed rows carry a different status?
