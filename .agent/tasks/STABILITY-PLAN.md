# Pilot Stability Plan — "Leave It Running" Reliability

**Created:** 2026-02-09
**Completed:** 2026-02-11
**Status:** ✅ 11/11 Complete
**Goal:** Raise Pilot's autonomous reliability from ~3/10 to 8/10

## Problem Statement

When Pilot runs autonomously (unattended), it degrades within hours:
- Issues get stuck permanently (`pilot-failed` is a death sentence)
- Decomposed sub-issues cascade-fail (merge conflicts after first PR merges)
- Silent error swallowing hides failures until queue is fully blocked
- No retry on transient errors (rate limits, network blips)
- State lost on restart (all PR tracking is in-memory)
- Circuit breaker is global — one bad PR blocks everything

## Fix Phases

### Phase 1 — Stop the Bleeding (P0)

These fixes address the most common failure modes that cause the queue to stall.

| Issue | Title | Status | What It Fixes |
|-------|-------|--------|---------------|
| GH-718 | Clean stale `pilot-failed` labels in cleanup job | ✅ Done | GH-833 → PR #844 — `cleanup.go` now cleans stale `pilot-failed` labels |
| GH-719 | Per-PR circuit breaker instead of global | ✅ Done | GH-834 → PR #841 — per-PR failure tracking with auto-reset |
| GH-720 | Retry with backoff on GitHub API calls | ✅ Done | GH-835 → PR #843 — `retry.go` with exponential backoff |
| GH-721 | Hard fail on branch switch failure | ✅ Done | GH-836 → PR #842 — `runner.go` returns error immediately |
| GH-722 | Detect rate limits and retry instead of failing | ✅ Done | PR #32 (TASK-40) — `ratelimit.go`, `scheduler.go`, wired in `main.go` and `poller.go` |

**Expected impact:** Queue stops stalling. Transient failures self-heal. One bad PR doesn't kill everything.

### Phase 2 — Prevent Cascading Failures (P1)

These fixes address the decomposition/conflict cascade that causes N-1 PRs to fail after first merge.

| Issue | Title | Status | What It Fixes |
|-------|-------|--------|---------------|
| GH-723 | Sequential sub-issue execution (merge-then-next) | ✅ Done | GH-742/743 — `epic.go:ExecuteSubIssues()` with `SubIssueMergeWaiter` callback |
| GH-724 | Detect merge conflicts before waiting for CI | ✅ Done | PR #740 — `controller.go` checks `MergeableState == "dirty"` |
| GH-725 | Auto-rebase PRs on simple merge conflicts | ✅ Done | Commit `2b81e95` — `git rebase origin/main` + `push --force-with-lease` before closing |

**Expected impact:** No more cascade failures from decomposed issues. Conflicts detected in seconds, not 30 minutes.

### Phase 3 — Resilience (P2)

These fixes add long-term robustness: crash recovery, smarter decomposition, observability.

| Issue | Title | Status | What It Fixes |
|-------|-------|--------|---------------|
| GH-726 | Persist autopilot state to SQLite | ✅ Done | PR #737 merged — `state_store.go` with PR lifecycle + processed tracking |
| GH-727 | LLM complexity classifier (replaces word-count) | ✅ Done | PR #739 merged — `complexity_classifier.go` with Haiku API |
| GH-728 | Failure metrics and alerting dashboard | ✅ Done | Commit `35426c1` — `metrics.go` with counters, gauges, histograms |

**Expected impact:** Crash-safe operation. Smarter task splitting. Visible health metrics.

## Dependency Order

```
Phase 1 (independent, can run in parallel):
  GH-718 ─── stale label cleanup           ✅ DONE (PR #844)
  GH-719 ─── per-PR circuit breaker        ✅ DONE (PR #841)
  GH-720 ─── API retry with backoff        ✅ DONE (PR #843)
  GH-721 ─── branch switch hard fail       ✅ DONE (PR #842)
  GH-722 ─── rate limit retry              ✅ DONE (PR #32, TASK-40)

Phase 2 (after Phase 1, some dependencies):
  GH-724 ─── conflict detection            ✅ DONE (PR #740)
  GH-725 ─── auto-rebase (after GH-724)    ✅ DONE (commit 2b81e95)
  GH-723 ─── sequential sub-issues         ✅ DONE (GH-742/743)

Phase 3 (after Phase 1+2):
  GH-726 ─── SQLite state                  ✅ DONE (PR #737)
  GH-727 ─── LLM classifier                ✅ DONE (PR #739)
  GH-728 ─── metrics + alerts              ✅ DONE (commit 35426c1)
```

## Key Files Affected

| File | Changes | Status |
|------|---------|--------|
| `internal/adapters/github/cleanup.go` | GH-718: `pilot-failed` cleanup | ✅ Done (PR #844) |
| `internal/adapters/github/poller.go` | GH-718: retry logic + ClearProcessed | ✅ Done |
| `internal/autopilot/controller.go` | GH-719: per-PR failure tracking; GH-724/725: conflict+rebase | ✅ Done (PR #841) |
| `internal/adapters/github/retry.go` | GH-720: retry wrapper with backoff | ✅ Done (PR #843) |
| `internal/executor/runner.go` | GH-721: hard fail on branch switch | ✅ Done (PR #842) |
| `internal/executor/ratelimit.go` | GH-722: rate limit detection + parsing | ✅ Done (PR #32) |
| `internal/executor/scheduler.go` | GH-722: rate limit retry scheduler | ✅ Done |
| `internal/executor/epic.go` | GH-723: sequential sub-issue execution | ✅ Done (GH-742/743) |
| `internal/autopilot/state_store.go` | GH-726: SQLite persistence + per-PR state | ✅ Done (PR #737, #841) |
| `internal/executor/complexity_classifier.go` | GH-727: LLM classifier | ✅ Done (PR #739) |
| `internal/autopilot/metrics.go` | GH-728: metrics collection | ✅ Done |

## Related Issues (Existing)

These older issues are superseded or related:
- GH-663 → superseded by GH-722 (rate limit retry)
- GH-664 → partially addressed by GH-727 (`no-decompose` label)
- GH-665 → superseded by GH-727 (LLM classifier)
- GH-671 → superseded by GH-718 (stale `pilot-failed` cleanup)
- GH-531 → partially addressed by GH-720 (API retry) and GH-719 (per-PR breaker)

## Success Criteria

All criteria met as of 2026-02-11:
- [x] Pilot runs 24h+ unattended without human intervention
- [x] Failed issues auto-retry with backoff (GH-722 — PR #32)
- [x] Decomposed issues don't cascade-fail (GH-723 — GH-742/743)
- [x] Merge conflicts detected in <1 minute, not 30 minutes (GH-724 — PR #740)
- [x] Restart recovers full autopilot state from SQLite (GH-726 — PR #737)
- [x] Dashboard shows health metrics and alerts on degradation (GH-728)
- [x] Auto-rebase on simple conflicts (GH-725)
- [x] LLM complexity classification (GH-727 — PR #739)
- [x] Stale `pilot-failed` labels auto-cleaned (GH-718 — PR #844)
- [x] Per-PR circuit breaker (GH-719 — PR #841)
- [x] GitHub API retry with backoff (GH-720 — PR #843)
- [x] Branch switch hard fail (GH-721 — PR #842)
- [x] Target reliability: 8/10 ✅
