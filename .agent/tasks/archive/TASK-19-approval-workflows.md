# TASK-19: Approval Workflows

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-27
**Category**: Safety / Enterprise

---

## Context

**Problem**:
Fully autonomous execution is scary for some teams. No human checkpoint before changes land.

**Goal**:
Optional human approval at key stages for safety and compliance.

---

## Implementation Summary

### What Was Built

Created `internal/approval/` package with:

1. **Core Types** (`types.go`):
   - `Stage` enum: `StagePreExecution`, `StagePreMerge`, `StagePostFailure`
   - `Decision` enum: `DecisionApproved`, `DecisionRejected`, `DecisionTimeout`
   - `Request` and `Response` structs for approval workflow
   - `Handler` interface for channel implementations
   - `Config` and `StageConfig` for YAML configuration

2. **Approval Manager** (`manager.go`):
   - Coordinates approval workflows across channels
   - Handles timeouts with configurable default actions
   - Auto-approves when stages are disabled
   - Tracks pending requests with cancellation support

3. **Telegram Handler** (`telegram.go`):
   - Sends approval requests with inline keyboard buttons
   - Stage-specific button labels (Execute/Cancel, Merge/Reject, Retry/Abort)
   - Handles callbacks from button presses
   - Updates messages with decision results

4. **Configuration Integration** (`config/config.go`):
   - Added `Approval *approval.Config` to main config
   - Defaults: disabled, 1h timeout for pre-execution/post-failure, 24h for pre-merge

### Configuration

```yaml
approval:
  enabled: true
  default_timeout: 1h
  default_action: rejected  # on timeout

  pre_execution:
    enabled: true
    timeout: 1h
    default_action: rejected
    approvers: ["@alice", "@bob"]

  pre_merge:
    enabled: true
    timeout: 24h
    default_action: rejected

  post_failure:
    enabled: false
    timeout: 1h
    default_action: rejected
```

### Files Created/Modified

- `internal/approval/types.go` - Core types and interfaces
- `internal/approval/manager.go` - Approval workflow coordinator
- `internal/approval/telegram.go` - Telegram channel handler
- `internal/approval/manager_test.go` - Unit tests (7 tests)
- `internal/config/config.go` - Config integration

---

## Approval Points

### Pre-Execution
- Review task before Pilot starts
- Useful for: sensitive projects, juniors

### Pre-Merge
- Review PR before auto-merge
- Useful for: production code, compliance

### Post-Failure
- Approve retry or escalate
- Useful for: debugging, cost control

---

## Future Work

### Phase 2: Slack Approval
- Interactive message blocks
- Thread-based discussion
- Approval audit log

### Phase 3: GitHub Integration
- PR review as approval
- Branch protection rules
- Status checks

### Phase 4: Additional Channels
- Email approval links
- Dashboard web UI queue

---

**Monetization**: Enterprise feature - compliance requirement
