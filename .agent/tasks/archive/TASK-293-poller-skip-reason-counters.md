# TASK-293: Poller skip-by-reason counters (GitHub/GitLab/Azure)

**Wave:** 2 (S) · **⚠️ Merge BEFORE TASK-292** (both touch `poller.go`) · **Audit ref:** §2 Action #3, §3.4 P1 (CS-4), §3.4 P2

---

## Problem

2026-05-21 GH-21/22/26 silent dispatch loss took 5+ hours to root-cause because no operator-visible signal existed for "issues seen vs dispatched per repo per tick". Every dispatch-skip branch in `internal/adapters/github/poller.go:919-1040` (9 branches: in-progress, done, blocked, needs-clarification, superseded, failed-skip, retry-ready-skip, processed-grace, completed-execution) logs at Info/Debug only — no metric. Scope-deferral at `1054-1060` is also silent. GitLab and Azure DevOps pollers have identical gaps.

## Approach

### Step 1 — Register counters (XS, ~20 min)

`internal/gateway/prometheus.go` — add:
- `pilot_poller_skipped_total{repo, reason}` — counter
- `pilot_poller_dispatched_total{repo}` — counter
- `pilot_poller_deferred_scope_overlap_total{repo}` — counter

Define `reason` enum constants (e.g., `ReasonInProgress`, `ReasonDone`, `ReasonBlocked`, `ReasonNeedsClarification`, `ReasonSuperseded`, `ReasonFailedSkip`, `ReasonRetryReadySkip`, `ReasonProcessedGrace`, `ReasonCompletedExecution`) in `internal/adapters/skipreason/skipreason.go` (new) so all three pollers share the same label values.

### Step 2 — Wire GitHub poller (S, ~45 min)

`internal/adapters/github/poller.go:919-1040` — at each skip branch, increment `pilot_poller_skipped_total.WithLabelValues(repo, ReasonX).Inc()` before `continue`.

`internal/adapters/github/poller.go:1054-1060` — increment `pilot_poller_deferred_scope_overlap_total.WithLabelValues(repo).Inc()`.

At dispatch path (the non-skip branch in the loop), increment `pilot_poller_dispatched_total.WithLabelValues(repo).Inc()`.

### Step 3 — Wire GitLab + Azure pollers (S, ~30 min)

`internal/adapters/gitlab/poller.go` and `internal/adapters/azuredevops/poller.go` — apply the same pattern, reusing the shared `skipreason` constants. Some branches may not exist in those pollers — that's fine; only wire what exists.

### Step 4 — Test (S, ~30 min)

- `internal/adapters/github/poller_test.go`: `TestPoller_SkipMetric_IncrementsByReason` table-driven over each skip path (mock store/judge/etc. to force each branch)
- Same pattern for `gitlab/poller_test.go`, `azuredevops/poller_test.go`

## Files to modify

- New: `internal/adapters/skipreason/skipreason.go` (shared constants)
- `internal/gateway/prometheus.go` (counter registration)
- `internal/adapters/github/poller.go` (~10 increment sites)
- `internal/adapters/gitlab/poller.go` (same pattern, fewer sites)
- `internal/adapters/azuredevops/poller.go` (same)
- Test files for each

## Test Strategy

- Unit: table-driven coverage of each skip branch firing the correct counter
- Manual: run `pilot start` against a repo with mixed issue states for 30 min; query Prometheus, confirm distribution across `reason` labels

## Effort

S (~2.5h total). One PR.

## Out of Scope

- Grafana panel for the new counters — operator config
- Alerting rule on skip-rate spikes — separate task once baseline collected
- Per-adapter triplication fix (audit §3.2 P2 — generic `Poller[T]`) — Wave 4+
