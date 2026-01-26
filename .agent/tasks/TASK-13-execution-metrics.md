# TASK-13: Execution Metrics & Analytics

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Monitoring

---

## Context

**Problem**:
No visibility into Pilot's performance - success rates, execution times, token costs, or trends.

**Goal**:
Track and visualize execution metrics for operational awareness and cost optimization.

**Value for Paid Product**:
- Usage-based billing foundation
- Customer insights dashboard
- SLA compliance proof

---

## Metrics to Track

### Per Execution
- Task ID, project, timestamp
- Duration (total, per phase)
- Token usage (input/output)
- Estimated cost ($)
- Success/failure status
- Files changed count
- Lines added/removed

### Aggregated
- Success rate (daily/weekly/monthly)
- Average execution time
- Total tokens/cost
- Tasks by project
- Peak usage times
- Failure reasons breakdown

---

## Implementation

### Phase 1: Data Collection
- Add metrics fields to ExecutionResult
- Store in SQLite (extend memory/store.go)
- Track tokens via Claude Code stream-json

### Phase 2: CLI Dashboard
- `pilot metrics` command
- Show last 7 days summary
- Success rate, avg duration, total cost

### Phase 3: Export & Integration
- JSON/CSV export
- Prometheus metrics endpoint
- Webhook for external analytics

---

## Files

- `internal/memory/metrics.go` - Metrics storage
- `internal/executor/runner.go` - Token tracking
- `cmd/pilot/metrics.go` - CLI command

---

**Monetization**: Foundation for usage-based billing
