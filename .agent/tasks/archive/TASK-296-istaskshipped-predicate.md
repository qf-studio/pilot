# TASK-296: `IsTaskShipped` predicate + cross-site invariant test

**Wave:** 3 (M) · **⚠️ Must merge BEFORE TASK-298** (both touch `state_store.go` schema) · **Closes TASK-288 Steps 1+3** · **Audit ref:** §2 Action #1, §3.4 CS-1, §3.5 recurring bug class #1

---

## Problem

Six call sites compute "is this task completed?" against different fields. v2.149.3 hardened the post-flight notifier (`!result.IsEpic && CommitSHA=="" && PRUrl==""`) but the pre-dispatch poller (`HasCompletedExecution` SQL at `internal/memory/store.go:540-551`) was not updated symmetrically. TASK-288 documents this: a freshly-installed v2.149.3 can still silently refuse to dispatch indefinitely because a legacy false-positive row passes the poller's check.

Root cause: no single predicate. Each site composes its own predicate; v2.149.3 fixed one, the others rotted.

## Approach

### Step 1 — Define the predicate (M, ~30 min)

- New: `internal/executor/task_shipped.go`:
  ```go
  // IsTaskShipped returns true iff this execution row represents real shipped work
  // (status=completed AND at least one of commit_sha or pr_url is set).
  func IsTaskShipped(row Execution) bool {
      if row.Status != "completed" {
          return false
      }
      return row.CommitSHA != "" || row.PRUrl != ""
  }
  ```
- Mirror in SQL for `HasCompletedExecution`:
  ```sql
  SELECT COUNT(*) FROM executions
   WHERE issue_number = ? AND project_path = ?
     AND status = 'completed' AND error = ''
     AND (commit_sha != '' OR pr_url != '')
  ```

### Step 2 — Reroute all call sites (M, ~90 min)

- `internal/memory/store.go:540-551` — `HasCompletedExecution` SQL gets the deliverable filter
- `cmd/pilot/handlers.go` — post-flight failure decision: refactor v2.149.3's inline guard to call `IsTaskShipped(execRow)`
- `cmd/pilot/commands.go:1165` — same
- `internal/adapters/github/poller.go:1022-1037` — the skip path already calls `HasCompletedExecution`; now it's correct by construction. Verify no other inline predicate exists in this path.
- `internal/executor/dispatcher.go:208,249` — `completed, _ := d.store.HasCompletedExecution(...)` drops the error silently (audit §3.4 P2). Change to log + counter on error, fall through to safer default.

### Step 3 — Cross-site invariant test (M, ~90 min)

- New: `internal/executor/task_shipped_test.go` — table-driven over 4 combinations of (CommitSHA empty/full, PRUrl empty/full):
  - `(empty, empty)` → `false`
  - `(full, empty)` → `true`
  - `(empty, full)` → `true`
  - `(full, full)` → `true`
  - Plus `status != "completed"` → `false`
  - Plus `error != ""` → `false`
- New: `internal/integration/task_completion_invariant_test.go` — for each test row, assert:
  - `IsTaskShipped(row)` and
  - `HasCompletedExecution(row.IssueNumber, row.ProjectPath)` (with that row in the DB)
  - agree on the result
- Specifically reproduce the GitNation GH-21/22/26 row (status=completed, error='', commit_sha='', pr_url='', is_epic=true) and assert:
  - `IsTaskShipped` returns false
  - poller now dispatches when called

### Step 4 — Manual smoke test (~30 min)

- Backup `~/.pilot/pilot.db`
- Insert a synthetic false-complete row for an issue
- Run `pilot start`; observe poller dispatches the issue (vs the old behavior of skipping)

## Files to modify

- New: `internal/executor/task_shipped.go`
- New: `internal/executor/task_shipped_test.go`
- New: `internal/integration/task_completion_invariant_test.go`
- `internal/memory/store.go`
- `cmd/pilot/handlers.go`
- `cmd/pilot/commands.go`
- `internal/executor/dispatcher.go`
- `internal/adapters/github/poller.go` (verify only; should already be correct after Step 2.1)

## Test Strategy

- Unit: predicate table-driven
- Integration: cross-site invariant test (CRITICAL — this is what prevents the bug class from recurring)
- Manual: GitNation reproduction case

## Effort

M (~4h total). One PR.

## Closes / blocks

- **Closes TASK-288 Step 1** (`HasCompletedExecution` deliverables guard)
- **Closes TASK-288 Step 3** (regression tests)
- **Does NOT close TASK-288 Step 2** (repo-namespace `autopilot_processed`) — that ships with TASK-298
- **Blocks TASK-298** — TASK-298 generalizes the schema change to all 7 adapter tables; landing in dependency order avoids merge conflicts

## Out of Scope

- Replacing `*memory.Store` with narrower `approvalPersister` in `controller.go` (audit §3.1 P3) — separate task
- Adding `pilot_dispatcher_completion_check_errors_total` counter explicitly — covered implicitly by Step 2.5's "log + counter on error"; if the existing autopilot metrics package doesn't have a suitable counter, create one in this task
