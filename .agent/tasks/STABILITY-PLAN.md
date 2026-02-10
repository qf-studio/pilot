# Pilot Stability Plan — "Leave It Running" Reliability

**Created:** 2026-02-09
**Status:** Active
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

| Issue | Title | What It Fixes |
|-------|-------|---------------|
| [GH-718](https://github.com/alekspetrov/pilot/issues/718) | Clean stale `pilot-failed` labels in cleanup job | Issues stuck forever after one failure. Cleanup job only handles `pilot-in-progress`, ignores `pilot-failed`. Also clears in-memory processed map so poller re-discovers issues. |
| [GH-719](https://github.com/alekspetrov/pilot/issues/719) | Per-PR circuit breaker instead of global | One bad PR trips global `consecutiveFailures` counter, blocks ALL PR processing. Fix: per-PR failure tracking with auto-reset after timeout. |
| [GH-720](https://github.com/alekspetrov/pilot/issues/720) | Retry with backoff on GitHub API calls | Label operations, PR checks, issue creation fail silently. No retry → stale labels, orphaned issues. Fix: exponential backoff wrapper for all GitHub API calls, respect 429 Retry-After. |
| [GH-721](https://github.com/alekspetrov/pilot/issues/721) | Hard fail on branch switch failure | `runner.go` ~line 730: if git checkout fails, execution CONTINUES from wrong branch → corrupted PRs. Fix: return error immediately, abort execution. |
| [GH-722](https://github.com/alekspetrov/pilot/issues/722) | Detect rate limits and retry instead of failing | Anthropic API rate limits → immediate `pilot-failed`. Fix: detect rate limit patterns in Claude Code output, typed `RateLimitError`, backoff retry (2m, 5m, 15m), max 3 attempts before failing. |

**Expected impact:** Queue stops stalling. Transient failures self-heal. One bad PR doesn't kill everything.

### Phase 2 — Prevent Cascading Failures (P1)

These fixes address the decomposition/conflict cascade that causes N-1 PRs to fail after first merge.

| Issue | Title | What It Fixes |
|-------|-------|---------------|
| [GH-723](https://github.com/alekspetrov/pilot/issues/723) | Sequential sub-issue execution (merge-then-next) | Epic decomposer creates N sub-issues, all branch from same base. After PR #1 merges, PRs #2-N all conflict. Fix: execute one sub-issue at a time, wait for merge, pull latest main, then next. |
| [GH-724](https://github.com/alekspetrov/pilot/issues/724) | Detect merge conflicts before waiting for CI | PR #682 had conflicts → CI never ran → autopilot waited 30m timeout. Fix: check `mergeable` state before CI wait, close conflicting PRs immediately, return issue to queue. |
| [GH-725](https://github.com/alekspetrov/pilot/issues/725) | Auto-rebase PRs on simple merge conflicts | Concurrent PRs touching different files get CONFLICTING status when main moves. Fix: attempt `git rebase origin/main` + `push --force-with-lease` before closing. Only close if rebase fails. |

**Expected impact:** No more cascade failures from decomposed issues. Conflicts detected in seconds, not 30 minutes.

### Phase 3 — Resilience (P2)

These fixes add long-term robustness: crash recovery, smarter decomposition, observability.

| Issue | Title | What It Fixes |
|-------|-------|---------------|
| [GH-726](https://github.com/alekspetrov/pilot/issues/726) | Persist autopilot state to SQLite | All state (PR tracking, circuit breaker, failure counts, processed map) is in-memory. Restart loses everything. Fix: SQLite tables for `autopilot_pr_state` and `autopilot_processed`, load on startup. |
| [GH-727](https://github.com/alekspetrov/pilot/issues/727) | LLM complexity classifier (replaces word-count) | Word count >50 = ComplexityComplex → decomposer fires on every detailed issue. Fix: Haiku API call for classification, `no-decompose` label support, fallback to heuristic on API failure. |
| [GH-728](https://github.com/alekspetrov/pilot/issues/728) | Failure metrics and alerting dashboard | No visibility into autopilot health. Silent failures accumulate. Fix: counters (processed, failed, rate-limited), gauges (queue depth, failed depth), alerts on circuit breaker trip and stuck PRs. |

**Expected impact:** Crash-safe operation. Smarter task splitting. Visible health metrics.

## Dependency Order

```
Phase 1 (independent, can run in parallel):
  GH-718 ─── stale label cleanup
  GH-719 ─── per-PR circuit breaker
  GH-720 ─── API retry with backoff
  GH-721 ─── branch switch hard fail
  GH-722 ─── rate limit retry

Phase 2 (after Phase 1, some dependencies):
  GH-724 ─── conflict detection (independent)
  GH-725 ─── auto-rebase (after GH-724)
  GH-723 ─── sequential sub-issues (independent, largest change)

Phase 3 (after Phase 1+2):
  GH-726 ─── SQLite state (independent)
  GH-727 ─── LLM classifier (independent, supersedes GH-665)
  GH-728 ─── metrics + alerts (after GH-726 for persistence)
```

## Key Files Affected

| File | Changes |
|------|---------|
| `internal/adapters/github/cleanup.go` | GH-718: add `pilot-failed` to stale label cleaner |
| `internal/adapters/github/poller.go` | GH-718: add `ClearProcessed()` method |
| `internal/autopilot/controller.go` | GH-719: per-PR failure tracking; GH-724: conflict detection; GH-725: auto-rebase |
| `internal/adapters/github/retry.go` | GH-720: new file, retry wrapper |
| `internal/executor/runner.go` | GH-721: hard fail on branch switch; GH-722: rate limit detection |
| `internal/executor/epic.go` | GH-723: sequential sub-issue execution |
| `internal/autopilot/state_store.go` | GH-726: new file, SQLite persistence |
| `internal/executor/decomposer.go` | GH-727: LLM classifier |
| `internal/autopilot/metrics.go` | GH-728: new file, metrics collection |
| `cmd/pilot/main.go` | GH-722: rate limit error handling; GH-726: state store wiring |

## Related Issues (Existing)

These older issues are superseded or related:
- GH-663 → superseded by GH-722 (rate limit retry)
- GH-664 → partially addressed by GH-727 (`no-decompose` label)
- GH-665 → superseded by GH-727 (LLM classifier)
- GH-671 → superseded by GH-718 (stale `pilot-failed` cleanup)
- GH-531 → partially addressed by GH-720 (API retry) and GH-719 (per-PR breaker)

## Success Criteria

After all 3 phases:
- [ ] Pilot runs 24h+ unattended without human intervention
- [ ] Failed issues auto-retry (up to 3 times with backoff)
- [ ] Decomposed issues don't cascade-fail
- [ ] Merge conflicts detected in <1 minute, not 30 minutes
- [ ] Restart recovers full autopilot state from SQLite
- [ ] Dashboard shows health metrics and alerts on degradation
- [ ] Target reliability: 8/10 (from current 3/10)
