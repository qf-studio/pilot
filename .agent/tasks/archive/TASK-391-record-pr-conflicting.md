# fix(autopilot): pilot_prs_conflicting_total is never incremented — RecordPRConflicting has zero production call sites (TASK-391)

**Status**: 🚀 Dispatched to Pilot → [#4069](https://github.com/qf-studio/pilot/issues/4069)
**Last Updated**: 2026-07-08
**Created**: 2026-07-08
**Priority**: LOW — small, self-contained
**Assignee**: Pilot

## Context

`Metrics.RecordPRConflicting` (`internal/autopilot/metrics.go:169`) backs the `pilot_prs_conflicting_total` Prometheus counter (`internal/gateway/prometheus.go:103-106`), but a repo-wide grep finds **no production call site** — only `metrics_test.go:33`. The counter can never move, regardless of how many merge conflicts autopilot handles (and it handled a storm of them on 2026-07-07: PRs #4004/#4005/#4010/#4014/#4047 all conflict-closed).

## Fix

1. Find the merge-conflict handling path in `internal/autopilot/controller.go` (`handleMergeConflict`, ~line 1836, plus the auto-rebase-failed → close path) and call `c.metrics.RecordPRConflicting()` at the point a PR is definitively classified as conflicting.
2. One increment per PR-conflict event, not per retry tick — guard against double counting if the conflict path is re-entered for the same PR.

## Acceptance Criteria

- [ ] Unit test: PR entering the merge-conflict path increments `pilot_prs_conflicting_total` exactly once.
- [ ] Re-entering the conflict path for the same PR does not double-count.
- [ ] Full short suite + lint green.

## Constraints

- Single PR. Do not decompose. This is a ~10-line change plus a test.
- Note: the counter only becomes *visible* on `/metrics` once TASK-390 (multi-controller export aggregation) lands, but this fix is independent and can merge in any order.

## Refs

- Siblings: TASK-390 (export silo), TASK-392 (success_rate semantics), #4029 (PR-family hydration re-scope)
- Pilot issue: https://github.com/qf-studio/pilot/issues/4069
