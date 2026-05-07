# TASK-33: Persist Approval Decision to executions Table

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

**Problem**:
The `executions` table has columns for approval persistence (added v2.124.0):
- `approval_request_id` (line 437 of `internal/memory/store.go` — bound during INSERT only)
- `approval_decision`, `approval_decision_at`, `approval_decision_by` (updated by `Store.SetApprovalDecision`, line 553)

**These columns are never populated for any execution.** Live verification on 2026-05-06:

```sql
SELECT count(*) FROM executions
WHERE approval_request_id != '' OR approval_decision != '';
-- 0 rows (out of all-time executions)
```

Two missing wires cause this:

1. **`approval_request_id` is never written to the execution row.** The execution row is INSERTed when Pilot picks up the issue, *before* the approval flow runs. Nothing UPDATEs `executions.approval_request_id` after `SubmitApprovalRequest` returns its ID. So even if the writer fired, `Store.SetApprovalDecision`'s `WHERE approval_request_id = ?` matches zero rows.

2. **`Controller.SetApprovalDecision` (the `PRStateWriter` impl, `controller.go:1119-1143`) does not call `Store.SetApprovalDecision`.** It updates the in-memory `pr.ApprovalDecision` and saves PRState via `stateStore.SavePRState(pr)`, but never touches the `executions` table.

**Concrete impact (verified 2026-05-06):**
- Smoke-test PRs #2702 (GH-2700) and #2704 (GH-2703) both merged successfully via the async approval flow. Both `executions.approval_decision` columns remain empty.
- Releases for both did NOT auto-fire — v2.128.1 had to be hand-tagged. Likely root cause: any release-trigger consumer keying off persisted decision sees nothing was decided. (To confirm during implementation.)
- Dashboards / audits / restart-resilience scenarios that re-read SQLite see no record of approvals.

**Goal**:
Wire both missing connections so that:
1. After `SubmitApprovalRequest` succeeds, the request ID is persisted to the corresponding `executions.approval_request_id` row.
2. `Controller.SetApprovalDecision` (and `MultiControllerStateWriter.SetApprovalDecision`) also calls `Store.SetApprovalDecision` so `approval_decision`, `approval_decision_at`, `approval_decision_by` get populated.

**Success Criteria**:
- [ ] After a smoke-test PR completes the approval flow, `SELECT * FROM executions WHERE task_id = '<id>'` shows non-empty `approval_request_id`, `approval_decision`, `approval_decision_at`, `approval_decision_by`.
- [ ] Existing tests still pass (`make test`).
- [ ] No regression in approval flow timing (still non-blocking).
- [ ] No new cross-package import cycles.

---

## Implementation Plan

### Phase 1: Persist `approval_request_id` after SubmitApprovalRequest
**Goal**: Make `executions.approval_request_id` discoverable for later UPDATEs.

**Tasks**:
- [ ] Add `Store.SetApprovalRequestID(ctx, taskID, requestID string) error` in `internal/memory/store.go`. Uses `withRetry` pattern. Updates the most recent non-completed (or most recent overall — TBD) `executions` row matching `task_id = ?` and `approval_request_id = ''`.
- [ ] In `Controller` or `approval.Manager` (decide which), call `SetApprovalRequestID(taskID, requestID)` immediately after `SubmitApprovalRequest` returns successfully. The task ID is already in `req.TaskID`. Plumbing: prefer the call site in `controller.go:1038` (`requestID, err := c.approvalMgr.SubmitApprovalRequest(ctx, req)` — `c` already owns or can own the memory store).

**Files**:
- `internal/memory/store.go` — new `SetApprovalRequestID` method
- `internal/autopilot/controller.go` — call site after line 1038
- (Optional) `internal/autopilot/controller.go` — Controller may need a new optional `memoryStore` field; thread it via a `WithMemoryStore` `ControllerOption` to keep the constructor stable.
- `internal/memory/store_test.go` — unit test for the new method (matching existing patterns)
- `cmd/pilot/...` — wire memory store into the controller at startup (verify no new circular deps)

### Phase 2: Persist decision in Controller.SetApprovalDecision
**Goal**: When the writer fires, also UPDATE the executions table.

**Tasks**:
- [ ] In `Controller.SetApprovalDecision` (`controller.go:1119`), after the in-memory update succeeds, call `c.memoryStore.SetApprovalDecision(ctx, requestID, decision, by)` if `memoryStore != nil`. Treat `sql.ErrNoRows` as a warning (log + continue) rather than a hard error — keeps the in-memory path resilient if Phase 1 missed a row.
- [ ] Same wiring in `MultiControllerStateWriter.SetApprovalDecision` (`controller.go:2395-2401`) IF that path is the one Manager uses; otherwise just the per-Controller path is fine.

**Files**:
- `internal/autopilot/controller.go` — modify `SetApprovalDecision`
- `internal/autopilot/controller_test.go` — extend or add test asserting Store call

### Phase 3: Verification & Tests
**Goal**: Lock in the fix with tests; verify against running daemon.

**Tasks**:
- [ ] Unit test: feed a fake `*memory.Store` to a Controller, call `SetApprovalDecision`, assert the row in executions is updated.
- [ ] Integration test (if existing patterns support it): full path from `SubmitApprovalRequest` → simulated tap → `RecordDecision` → both in-memory PRState and SQLite row updated.
- [ ] After merge, repeat live smoke-test (file a tiny Pilot issue, watch SQLite columns populate after approval tap).

**Files**:
- `internal/autopilot/controller_test.go`
- `internal/memory/store_test.go`

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to call `SetApprovalRequestID` | (A) Inside `approval.Manager.SubmitApprovalRequest`; (B) at the controller call site after the function returns | **B** | Manager package shouldn't depend on `memory` (clean layering). Controller already owns autopilot orchestration and is the natural seam. |
| Where `Controller` gets `memoryStore` | (A) New field + ControllerOption; (B) Pass on every call; (C) Re-use existing `stateStore` as composite | **A** | Keeps PRState persistence (`stateStore`) and execution persistence (`memoryStore`) separate concerns. Optional via Option pattern preserves backward compat. |
| Failure behavior on no matching row | Hard error vs warning | Warning + continue | In-memory path is the source of truth for the merge decision. SQLite persistence is for audits and downstream consumers; a missed row shouldn't deadlock the merge. |
| Update `request_id` for: most-recent row vs. all matching rows | most-recent vs broad | Most recent non-completed match | Multiple historic execution rows for the same `task_id` exist (retries, reruns); only the active one should be tagged. |

---

## Dependencies

**Requires**:
- [x] v2.124.0 columns already in schema
- [x] v2.121–v2.128 async dispatch chain working in-memory

**Blocks**:
- Reliable post-merge auto-release (likely depends on persisted decision; will validate during implementation)
- Restart-resilience audits / dashboards reading approval state

---

## Verify

```bash
# Unit + integration tests
make test ./internal/autopilot/... ./internal/memory/...

# Lint
make lint

# Build
make build

# After deploy: live verify
# 1. File a tiny Pilot issue
# 2. Tap Approve in TG when prompted
# 3. sqlite3 ~/.pilot/data/pilot.db \
#      "SELECT task_id, approval_request_id, approval_decision, approval_decision_by \
#       FROM executions WHERE task_id = 'GH-NNN';"
#    Expect: all four columns populated
```

---

## Done

- [ ] `executions.approval_request_id` is populated after `SubmitApprovalRequest` for the active execution row
- [ ] `executions.approval_decision`, `_at`, `_by` are populated after `RecordDecision` fires
- [ ] Unit tests cover both wires
- [ ] No regression in approval flow latency or merge correctness
- [ ] PR opened, CI green, approved, merged, released

---

## Notes

- v2.128.1 was the third hand-tagged release this week (after v2.126/127/128). If this fix unblocks auto-release, it eliminates a recurring operational burden.
- Per memory `pattern_hot_upgrade_bootstrap.md`: the persistence fixes via hot upgrade trigger their own bug once. Be aware that the first PR merging this fix may itself need hand-tagging because the running daemon predates the fix.
- This task is the natural follow-up to TASK-29/30/31 (the trifecta). Together, they make the async approval pipeline fully observable.

---

**Last Updated**: 2026-05-06
