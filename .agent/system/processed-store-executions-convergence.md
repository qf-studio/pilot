# ProcessedStore vs executions — Divergent Semantics & Convergence Design

**Superseded 2026-08-04 (TASK-441 Leg 8 / GH-4731).** This doc predates the TASK-441
seam-hardening pass and its "Current State Map" (§1) is now actively misleading: it
was written before the `executions`/`execution_claims`/`execution_events` schema and
the `ExecutionLifecycle` chokepoint (TASK-404) existed in their current form.

For the current, source-verified DB schema (including `executions`,
`execution_claims`, `execution_events`, `autopilot_pr_state`, `autopilot_scope_release`,
and the full 30-table inventory) and the load-bearing/frozen table list, see
**`.agent/system/ARCHITECTURE.md`** → "Database Schema (SQLite)" and "External
Contract Freeze".

For the dispatch-guard/dedup convergence problem this doc was designed to solve, the
relevant current infrastructure is the `ExecutionLifecycle` post-terminal tripwire
sweep (TASK-441 Leg 5, `internal/executor/lifecycle.go:349`) — see
`.agent/system/ARCHITECTURE.md` → "TASK-441 Seam Infrastructure". If Phase 2 of the
original convergence proposal (GH-2591) is still wanted, re-derive it against current
source rather than resuming from this doc's §4–§6, which reference table shapes and
call sites that have since moved.

This file is kept for historical reference (GH-2591 phase-1 design rationale) and is
not maintained further. Do not treat any schema, line number, or code path cited below
this line as current.

---

*(Original phase-1 design doc preserved in git history — see the commit that added
this pointer for the last full version, or `git log -p -- .agent/system/
processed-store-executions-convergence.md`.)*
