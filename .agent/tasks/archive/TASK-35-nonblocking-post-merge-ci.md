# TASK-35: Non-Blocking handlePostMergeCI (Gap 2)

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

**Problem**:
`handlePostMergeCI` calls `ciMonitor.WaitForCI(ctx, mainSHA)` which polls in a loop for up to 30 minutes (`internal/autopilot/ci_monitor.go:93-121`). This blocks the entire `processAllPRs` sequential loop in `controller.go:2241-2280`. While the post-merge CI of one PR is being awaited, no other PR ticks. If the daemon restarts mid-block, the goroutine is killed and `StagePostMergeCI` is restored — but the new tick uses a fresh `mainSHA` lookup which may point to a different commit (e.g., docs-version-sync ran in between).

`handleWaitingCI` (pre-merge) was already refactored to use non-blocking `CheckCI` and tick-driven polling. `handlePostMergeCI` was missed.

In current production with `ci_checks.mode: auto` + `discovery_grace_period: 1m`, the auto-mode grace path usually resolves in ~2 minutes, masking the architectural issue. But if `required_checks` are configured or CI takes longer, this can deadlock the controller.

**Goal**:
Port `handlePostMergeCI` to the same non-blocking pattern as `handleWaitingCI`: each tick calls `CheckCI` and either advances the stage or returns to wait for the next tick.

**Success Criteria**:
- [ ] `handlePostMergeCI` does not block the tick loop for more than a few hundred ms
- [ ] Stage transitions: `StagePostMergeCI` → `StageReleasing` (success) | `StageFailed` (CI fail) | stays in `StagePostMergeCI` (pending, wait next tick)
- [ ] Restart mid-poll resumes correctly from persisted prState
- [ ] Existing post-merge CI tests pass; new test covers the non-blocking transition

---

## Implementation Plan

### Phase 1: Refactor handlePostMergeCI
**Tasks**:
- [ ] Replace `WaitForCI` call with `CheckCI`
- [ ] Map result to stage transition: success → release path; pending → return nil (next tick); failure → StageFailed
- [ ] Use prState fields to track post-merge SHA / first-seen timestamp to enforce grace period across ticks (mirror handleWaitingCI's approach)

**Files**:
- `internal/autopilot/controller.go` (handlePostMergeCI ~line 1487)
- `internal/autopilot/state_store.go` if new prState fields needed

### Phase 2: Tests
**Tasks**:
- [ ] Table-driven tests for each transition (success, failure, pending, grace-period expiry, daemon restart mid-poll)

**Files**:
- `internal/autopilot/controller_test.go`

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Grace period tracking | new prState field vs in-memory only | new prState field | survives restart; matches existing pre-merge pattern |
| Backoff between ticks | rely on tick interval (existing) | rely on tick interval | simpler; tick interval is already configurable |

---

## Verify

```bash
make test ./internal/autopilot/...
make lint
```

---

## Done

- [ ] handlePostMergeCI is non-blocking (no internal poll loop)
- [ ] Tests pass including restart scenarios
- [ ] Other PRs' ticks not starved during a long post-merge CI wait
EOF
