# TASK-37: Observability for Approval Persistence Misses (Gap 5)

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

**Problem**:
`Store.SetApprovalRequestID` (`internal/memory/store.go:583-609`) returns `sql.ErrNoRows` when the UPDATE matches zero rows. The caller treats it as a warning + continue — correct for resilience, but it leaves no observable signal if the audit trail is silently lost.

Same pattern in `Store.SetApprovalDecision` (`store.go:553-577`).

Verified live on 2026-05-06: the happy path works (TASK-33 / v2.128.2). The zero-row case is rare but unmonitored. If a future change introduces a race or task_id format drift, we'd discover it via missing audit rows after the fact — exactly the postmortem cost we want to avoid.

**Goal**:
Add minimal observability so a zero-row persist failure is visible without failing the merge flow.

**Success Criteria**:
- [ ] Prometheus counter (or existing metrics infra) increments on zero-row persist for both `SetApprovalRequestID` and `SetApprovalDecision`
- [ ] Log line includes `task_id` AND `request_id` so an operator can correlate
- [ ] No regression in approval flow latency or merge correctness
- [ ] Existing tests pass; one new test asserts the counter increments on the zero-row path

---

## Implementation Plan

### Phase 1: Add metric
**Tasks**:
- [ ] Add a Prometheus counter (or whatever metrics package the codebase uses — check `internal/metrics/` or `internal/gateway/prometheus*.go`) named `pilot_approval_persist_misses_total` with a `kind` label (`request_id` | `decision`).
- [ ] Wire the increment from the call sites: when the caller logs the warning for zero-row case, also bump the counter.

**Files**:
- `internal/metrics/...` or wherever the existing counter conventions live
- `internal/autopilot/controller.go` (the call sites for both Set methods)

### Phase 2: Improve log structure
**Tasks**:
- [ ] Ensure both warning logs include structured fields: `task_id`, `request_id`, the operation name, and decision (where applicable).
- [ ] Match the slog patterns used elsewhere in the file.

**Files**:
- `internal/autopilot/controller.go`

### Phase 3: Test
**Tasks**:
- [ ] Unit test: feed a controller a memory store with no matching execution row, call the writer, assert metric incremented and log emitted.

**Files**:
- `internal/autopilot/controller_test.go`

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Metric name | `pilot_approval_persist_misses_total` vs split per method | single counter w/ label | Lower cardinality, easier to alert on |
| Severity of zero-row | Warning vs Error | Warning + counter | Audit trail miss does not block merge; alerting is via metric, not log level |
| Where to increment | Inside Store methods vs at caller | At caller | Keeps Store package free of metrics deps; matches existing conventions |

---

## Verify

```bash
make test ./internal/autopilot/...
make lint
make build

# Operationally (after deploy): confirm metric appears
curl -s localhost:<metrics-port>/metrics | grep pilot_approval_persist_misses
```

---

## Done

- [ ] Counter added and wired
- [ ] Log lines structured with task_id + request_id
- [ ] Test passes
- [ ] No regression

---

## Notes

- Low priority polish; not a release/merge blocker.
- Closes the observability gap identified in the v2.128.2 audit (Gap 5 of 5).
- The other 4 gaps are now closed: Gap 1 (TASK-34/v2.128.3), Gap 2 (TASK-35/v2.128.4), Gap 3 (TASK-33/v2.128.2 verified live), Gap 4 (resolved by #2694/v2.128.0 zero-timestamp fix).

---

**Last Updated**: 2026-05-06
