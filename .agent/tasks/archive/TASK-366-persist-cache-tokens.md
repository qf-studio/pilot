---
status: completed
priority: P3
created: 2026-06-18
execution: queued
github_issue: 3567
labels: [memory, dashboard, metrics]
---

# TASK-366: Persist cache token counts per execution + surface on TOKENS card

**Status**: ✅ Completed — shipped across #3567 sub-issues (GH-3617/3618/3619) + populate fix #3634; verified on main @ v2.192.0
**Assignee**: Pilot
**GitHub Issue**: https://github.com/qf-studio/pilot/issues/3567

---

## Context

**Problem**:
The dashboard COST card is correct — `estimateCostWithCache` already factors cache
read/write tokens into the $-figure. But the `executions` table persists only
`tokens_input` / `tokens_output` (uncached input, observed 8–2060 per row). The cache
token counts — which dominate real throughput — are computed at runtime
(`ExecutionResult.CacheCreationInputTokens` / `CacheReadInputTokens`, set in
`runner.go:2185-2186, 2233-2234, 2637-2638`) but **never written to the DB**. The TOKENS
card therefore under-reports actual token throughput by orders of magnitude.

The values already exist in memory at write time — they're passed to
`estimateCostWithCache` (`runner.go:2191`) but dropped before the row is inserted. This
task plumbs them through to storage and the TOKENS card.

**Goal**:
Persist `tokens_cache_read` + `tokens_cache_write` per execution and surface them on the
TOKENS card, leaving the COST card unchanged.

---

## Acceptance Criteria

- [ ] New executions persist cache read + cache write token counts (additive schema
      migration; old rows default 0).
- [ ] TOKENS card shows cache-inclusive totals (or a cached / uncached split).
- [ ] COST card output is unchanged (already correct via `estimateCostWithCache`).
- [ ] `go build ./...`, `make test`, `make lint` all green.

---

## Implementation

### Phase 1: Schema + struct
**Goal**: Add the two columns and the Go fields.

**Tasks**:
- [ ] Append two `ALTER TABLE executions ADD COLUMN` migrations after the existing token
      columns (`store.go:144-147`):
      `tokens_cache_read INTEGER DEFAULT 0`, `tokens_cache_write INTEGER DEFAULT 0`.
      Migrations are additive + idempotent — DO NOT renumber or reorder existing entries;
      append only (old DBs upgrade in place, old rows default 0).
- [ ] Add `TokensCacheRead int64` + `TokensCacheWrite int64` to the `Execution` struct
      (`store.go:483`, next to `TokensInput`/`TokensOutput`).

**Files**: `internal/memory/store.go`

### Phase 2: Write + read plumbing
**Goal**: Persist and load the new columns.

**Tasks**:
- [ ] Extend the INSERT (`store.go:525-532`) column list + value args with the two new
      fields.
- [ ] Extend the SELECT scan (`store.go:572, 591`) with `COALESCE(tokens_cache_read,0)`,
      `COALESCE(tokens_cache_write,0)` → `&exec.TokensCacheRead`, `&exec.TokensCacheWrite`.
- [ ] Populate the `Execution` from `ExecutionResult.CacheCreationInputTokens` (→ write)
      and `.CacheReadInputTokens` (→ read) at the call site that builds the memory
      `Execution` from a runner result. (Grep for where `TokensInput`/`TokensOutput` are
      copied from the result into the `Execution` struct and mirror it.)

**Files**: `internal/memory/store.go`, the result→Execution mapping site (executor/autopilot).

### Phase 3: Metrics + TOKENS card
**Goal**: Aggregate and display.

**Tasks**:
- [ ] Extend the metrics aggregation query (`metrics.go:548` token block) to SUM the two
      new columns; expose on the metrics struct it returns.
- [ ] Update `renderTokenCard` (`tui.go:2047`) to show cache-inclusive totals or a
      cached/uncached split. Keep COST card untouched.
- [ ] Update/extend `tui_test.go` for the new card content.

**Files**: `internal/memory/metrics.go`, `internal/dashboard/tui.go`, `internal/dashboard/tui_test.go`

---

## Out of Scope

- Backfilling cache tokens for historical rows (default 0 is acceptable).
- Changing `estimateCostWithCache` or any COST card behavior.
- Per-model cache-token breakdowns.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Column naming | `tokens_cache_read/write` vs `cache_creation/read_input_tokens` | `tokens_cache_read` / `tokens_cache_write` | Matches issue proposal + existing `tokens_*` naming; `write` = cache *creation* |
| Migration style | new numbered migration vs append ALTER | append `ALTER TABLE … ADD COLUMN` | Matches existing idempotent additive pattern in `store.go` |
| Card display | total-only vs cached/uncached split | split (cached vs uncached) | More diagnostic; the whole point is cache reads dominate |

---

## Verify

```bash
go build ./...
go test ./internal/memory/... ./internal/dashboard/... -v
make lint
# fresh DB migrates clean; old DB gains columns with 0 defaults
```

---

## Done

- [ ] Two new columns exist; fresh + existing DBs migrate clean (old rows = 0).
- [ ] An execution writes non-zero cache read/write counts; SELECT round-trips them.
- [ ] TOKENS card reflects cache tokens; COST card byte-identical.
- [ ] build / test / lint green.

---

## Refs

- GitHub issue: https://github.com/qf-studio/pilot/issues/3567
- Cache token source: `runner.go:181-182, 362-365, 2185-2191, 2233-2239, 2637-2650`
- Schema/write path: `store.go:73,144-160,483,525-532,572-591`
- Metrics agg: `metrics.go:548` · TOKENS card: `tui.go:2047`
- Origin: TASK-361 wave-2 cost-card investigation (the $36.64 figure verified correct)

---

**Last Updated**: 2026-06-18
