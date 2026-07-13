# TASK-381: GH-3764 parts (c)/(d) never landed in internal/memory/store.go

**Created:** 2026-07-03
**Status:** 🚀 Dispatched to Pilot → [#4084](https://github.com/qf-studio/pilot/issues/4084) (2026-07-08)

## Problem

GH-3764 ("execution ledger consistency") decomposed into 5 subtasks by
filename (dispatcher.go, internal/executor/runner.go ×2, internal/memory/feedback.go,
internal/gateway/dashboard_ws.go). None of them touched `internal/memory/store.go`,
so two pieces of the parent spec (GH-3759 inherited body, fetched via
`gh issue view 3764`) are still outstanding:

1. **(c)** `store.go:151` still hardcodes
   `` `ALTER TABLE executions ADD COLUMN model_name TEXT DEFAULT 'claude-sonnet-4-5'` ``.
   Spec calls for dropping this default via an additive/idempotent migration
   (leaving existing rows untouched) and updating dashboard/CLI renderers to
   show `unknown` for NULL instead of a fabricated model name.
2. **(d, store.go half)** `reclassifyLegacyOutcomes()` (`store.go:383+`) has
   buckets for no_op/rate_limited/skipped/stalled/infra but is missing the
   `ErrParentDone` ("parent task is already done; refusing to create
   sub-issues") signature that `TerminalStatus` (runner.go, fixed in
   TASK-GH-3764-2) now classifies as `skipped`. Legacy rows with that error
   text and `status='failed'` will not self-correct on next boot.

## Context

Found during TASK-GH-3764-5 while verifying runner.go was fully covered —
that subtask's scope fence is runner.go only, so this is deliberately left
unimplemented here, same pattern as TASK-380 (found during GH-3764-4).

## Acceptance Criteria

- [ ] Idempotent migration in `store.go` drops/changes the `model_name`
      default so future NULL rows aren't backfilled with a guessed model.
- [ ] Dashboard/CLI renderers show `unknown` for NULL `model_name`.
- [ ] `reclassifyLegacyOutcomes()` gains an `ErrParentDone`-pattern bucket
      mapping to `status='skipped'`, mirroring `TerminalStatus`.
- [ ] `make build && make test && make lint` green.
