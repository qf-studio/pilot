---
status: completed
priority: P2
created: 2026-06-18
execution: queued
labels: [memory, metrics, bug]
---

# TASK-367: Wire cache tokens into the completion UPDATE (finish #3567)

**Status**: ✅ Completed — populate fix shipped via GH-3633 → PR #3634 (`07a69b40`), verified on main @ v2.192.0. Stuck epics #3632/#3636 closed (phantom-subtask retry loop, empty diffs).
**Assignee**: Pilot
**Supersedes**: GH-3616 (failed twice — PRs #3623, #3629 both closed unmerged; autopilot-fix #3630 stuck on `pilot-needs-clarification`)

---

## Context

**Problem (regression / half-shipped feature):**
TASK-366 / #3567 added `tokens_cache_read` + `tokens_cache_write` columns, the
`Execution` struct fields, the queued-row INSERT, the metrics SUM, and the TOKENS-card
cached/uncached split. **But the values are never written**, so the columns stay `0` and
the TOKENS card's "cached" line is permanently `0`. The feature is inert.

**Root cause (exact):**
The `executions` row's token counts are NOT set at INSERT time (the row is inserted with
status=`queued` and zero tokens at `dispatcher.go:399`). They are written later, on
completion, by **`Store.SaveExecutionMetrics`** via an `UPDATE executions SET …`
(`internal/memory/metrics.go:206-228`). That UPDATE lists `tokens_input … final_rss_mb`
but **omits `tokens_cache_read` / `tokens_cache_write`**. The `ExecutionMetrics` struct
(`metrics.go:11`) has no cache fields, and the caller (`dispatcher.go:737`) never passes
them. So the cache columns are never updated off their `0` default.

The runtime values already exist on the result:
`ExecutionResult.CacheReadInputTokens` and `.CacheCreationInputTokens`
(`runner.go:363-365`, populated by the backends).

> ⚠️ **GH-3616 failed twice.** Do NOT patch the INSERT (`store.go:531` already has the
> columns) or the read-path scan (`metrics.go:594`) — those are already correct. The bug
> is the **completion UPDATE** in `SaveExecutionMetrics`. Watch the SQL placeholder
> ordering: the `?` args must line up 1:1 with the `SET` columns or tests/round-trips fail.

---

## Acceptance Criteria

- [ ] `ExecutionMetrics` carries cache read/write token counts.
- [ ] `SaveExecutionMetrics`'s UPDATE writes `tokens_cache_read` + `tokens_cache_write`.
- [ ] The dispatcher passes the result's cache tokens into `ExecutionMetrics`.
- [ ] A completed execution with non-zero cache tokens round-trips: the `executions` row
      has non-zero `tokens_cache_read`/`write`, and `GetMetricsSummary` /
      `TotalTokensCacheRead`/`Write` reflect them.
- [ ] COST card behavior unchanged.
- [ ] `go build ./...`, `make test`, `make lint` green.

---

## Implementation (3 sites — all in scope)

### 1. `internal/memory/metrics.go` — struct (`type ExecutionMetrics struct`, ~line 11)
Add next to `TokensTotal`:
```go
TokensCacheRead  int64
TokensCacheWrite int64
```

### 2. `internal/memory/metrics.go` — UPDATE in `SaveExecutionMetrics` (~line 208-228)
Add the two columns to the `SET` list AND the matching args, keeping placeholder order
exact:
```go
UPDATE executions SET
    tokens_input = ?,
    tokens_output = ?,
    tokens_total = ?,
    tokens_cache_read = ?,     // NEW
    tokens_cache_write = ?,    // NEW
    estimated_cost_usd = ?,
    ... (rest unchanged) ...
WHERE id = ?
```
…and insert `metrics.TokensCacheRead, metrics.TokensCacheWrite` into the args slice in
the SAME position (right after `metrics.TokensTotal`).

### 3. `internal/executor/dispatcher.go` — caller (~line 737)
In the `SaveExecutionMetrics(&memory.ExecutionMetrics{…})` literal, add:
```go
TokensCacheRead:  result.CacheReadInputTokens,
TokensCacheWrite: result.CacheCreationInputTokens,
```

### Tests
- [ ] Unit/integration: build an `ExecutionMetrics` with non-zero cache tokens →
      `SaveExecutionMetrics` → re-read the row (`GetExecution` / metrics scan) → assert
      both cache fields non-zero. Existing `metrics_cache_tokens_test.go` already covers
      the SUM aggregation given populated rows; this closes the write gap.

---

## Out of Scope

- The learning-loop `memory.Execution` at `runner.go:3825` (separate path; does not feed
  the TOKENS card). Leave unless trivially consistent.
- Backfilling historical rows (0 is fine).
- Any COST card / `estimateCostWithCache` change.

---

## Verify

```bash
go build ./...
go test ./internal/memory/... ./internal/executor/... -run 'Metric|CacheToken|Execution' -v
make lint
```

---

## Refs

- Parent: #3567 (TASK-366) — added everything EXCEPT this write.
- Failed predecessors: GH-3616 (#3623, #3629 closed unmerged), autopilot-fix #3630 (stuck).
- Write site: `metrics.go:206` `SaveExecutionMetrics` UPDATE · caller `dispatcher.go:737`.
- Cache source: `runner.go:363-365` (`ExecutionResult.Cache{Read,Creation}InputTokens`).
- Read path (already correct): `store.go:531` INSERT, `metrics.go:266,594` SUM/scan.

---

**Last Updated**: 2026-06-18
