# TASK-19: Approval Workflows

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Safety / Enterprise

---

## Context

**Problem**:
Fully autonomous execution is scary for some teams. No human checkpoint before changes land.

**Goal**:
Optional human approval at key stages for safety and compliance.

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

## Approval Channels

1. **Telegram** - Inline buttons (existing)
2. **Slack** - Interactive messages
3. **GitHub** - PR review requirement
4. **Email** - Approval links
5. **Dashboard** - Web UI queue

---

## Configuration

```yaml
workflows:
  approval:
    pre_execution:
      enabled: true
      approvers: ["@alice", "@bob"]
      timeout: 1h
      default_action: reject

    pre_merge:
      enabled: true
      require_tests: true
      require_review: 1
```

---

## Implementation

### Phase 1: Telegram Approval (exists partially)
- Extend confirmation flow
- Add pre-merge approval
- Timeout handling

### Phase 2: Slack Approval
- Interactive message blocks
- Thread-based discussion
- Approval audit log

### Phase 3: GitHub Integration
- PR review as approval
- Branch protection rules
- Status checks

---

**Monetization**: Enterprise feature - compliance requirement
