# TASK-407: Atomic dispatch-admission claim — make duplicate execution structurally impossible

**Status**: ✅ SHIPPED 2026-07-16 (#4349 → #4361 execution_claims+generation / #4362 ErrClaimLost CLI funnel / #4363 all entry points routed + inventory test; live on daemon since 09:37Z restart, released in v2.241.0). **Proof pending**: epic-lifecycle canary green on `duplicate-pr` → then close #4265 and archive this doc. **Follow-up**: #4372 — poller retry path re-claims generation 0 (once-failed tasks blocked from retry; workaround = delete stale `execution_claims` row; fix needs next daemon restart).
**Created**: 2026-07-16
**Assignee**: Pilot

---

## Context

**Problem**:
10 duplicate-execution incidents in 12 days (Jul 04 → Jul 15), ~185 excess executions (~$115 upper bound) plus duplicate PRs/issues. Every fix guarded ONE entry point with its OWN key (PR number @ autopilot register; merged-PR check @ finalize; 5-min poller lease; bare task_id ledger rows; `(task_id, project_path)` scoping #4297; parent-children guard TASK-401; CI-fix spawn claim #4319) — and every one is **check-then-act**, never an atomic claim. The proving incident: sandbox GH-82 executed **6× concurrently** (2026-07-15) on a daemon running v2.240.0 — with the flock lock and ALL prior fixes live. Even the fix tasks themselves ran multiple times (GH-4216 ×5, GH-4218 ×4, GH-4300 ×4).

**Root mechanism** (nav-research, 0.8 confidence): dispatch has two structurally independent channels — (1) poller → `Dispatcher.QueueTask` → ProjectWorker (the only channel with pre-checks) and (2) the epic decomposer's sequential loop `executeSubIssuesTracked` → `Begin` (`internal/executor/epic.go:2008`) → `executeWithOptions` (`epic.go:2035`), which **never calls `IsTaskQueued`/`QueueTask`**. Channel 2's children are protected only by the poller's pre-emptive mark, which decays after `retryGracePeriod` (~5 min; children queue 10–40+ min behind slow siblings) and is **per-process in-memory** (wiped by restart — a 19:17 restart landed mid-cycle of the GH-82 epic). `IsTaskQueued` then correctly answers false for a not-yet-started child (no ledger row exists) — the right answer to the wrong question. Successful concurrent duplicates are never reconciled: `reconcileChildOutcome` (`epic.go:1674`) short-circuits unless `hasFailureSignal` (`epic.go:~1749`).

**Goal**:
One invariant, enforced by the database: *an execution for (project, task, generation) may begin only by winning one atomic, durable claim.* After this task, a second dispatch of the same task — from any entry point, any goroutine, any process — physically cannot create a duplicate run.

---

## Known Pitfalls & Patterns

- **PATTERN** (#4319 `ClaimSpawnedFix`, `internal/autopilot/state_store.go:1062`): the codebase's proven atomic-claim idiom — `INSERT OR IGNORE` into a `(repo, dedup_key)` PRIMARY KEY table + `RowsAffected()==1`. Generalize exactly this; SQLite serializes it correctly across processes.
- **PITFALL** (mem-019 / #4297): `executions.project_path` is an absolute FS path; discriminator-form mismatches have silently split keys before. The claim key must use a canonicalized project identity (resolve symlinks/trailing slashes once, or use the config project name).
- **PITFALL** (TASK-404): `ExecutionLifecycle` was built as a *record* chokepoint ("one place that can't skip a step"), not an *admission gate* — `Begin` (`internal/executor/lifecycle.go:69-97`) is a bare `INSERT`. This task upgrades it; do not build a parallel mechanism beside it.
- **PITFALL** (incident history I3→I4): leases/grace-periods are not claims. Do NOT fix this by lengthening `retryGracePeriod`.

---

## Acceptance Criteria

- [ ] New `execution_claims` table: `(task_id, project_path, generation)` PRIMARY KEY + `execution_id`, `created_at` (migration in `internal/memory`).
- [ ] `ExecutionLifecycle.Begin` claims via `INSERT OR IGNORE` + `RowsAffected()==1` BEFORE `SaveExecution`; on loss returns typed `ErrClaimLost`.
- [ ] Every `Begin` call site handles `ErrClaimLost` as "already claimed — abort before backend invocation, log, no error state": dispatcher create sites, **epic sub-issue loop `epic.go:2008`** (the unguarded channel), CLI `recordCLITaskStart` (`cmd/pilot/commands.go:895`), and any others found by grep.
- [ ] `generation` increments only where a legitimate retry is decided (retry-after-terminal-failure, conflict close-and-reexecute, `shouldRetryFailedIssue`) — retries must not deadlock on their own prior claim; concurrent dispatches of the SAME generation must lose.
- [ ] Claim released/superseded on terminal states so heal paths and legitimate re-runs work (define: claim rows are permanent per generation; new attempt = new generation — simpler than delete-on-terminal, immune to crash windows).
- [ ] **Entry-point inventory test**: table-driven test enumerating every dispatch path (poller, epic-direct, CI-fix, conflict-retry, restart-reap, CLI) asserting each funnels through the claim — two concurrent `Begin` calls for the same (project, task, generation) → exactly one row, one `ErrClaimLost`, across goroutines (use `t.Parallel` + barrier).
- [ ] Multi-process test (or documented manual verification): two store handles on one SQLite file, concurrent claims → one winner.
- [ ] Canary: next scheduled epic-lifecycle run passes `duplicate-pr` (closes #4265's gate; #4347 becomes a consumer of this fix).

---

## Implementation

### Phase 1: Claim primitive
- [ ] Migration: `execution_claims` table (above). Canonicalize `project_path` at write (one helper, reused by `IsTaskQueued`).
- [ ] `Store.ClaimExecution(taskID, projectPath string, generation int, executionID string) (bool, error)` — the `ClaimSpawnedFix` idiom.
- [ ] `ExecutionLifecycle.Begin`: claim → on win `SaveExecution`; on loss `ErrClaimLost`. Thread `generation` through `Begin`'s signature (default 0).

**Files**: `internal/memory/store.go` (+migration), `internal/executor/lifecycle.go`, `internal/executor/lifecycle_test.go`

### Phase 2: Call-site adoption
- [ ] `internal/executor/epic.go:2008` — epic child loop: on `ErrClaimLost` skip the child (another channel owns it), log + ledger event `dispatch_claim_lost`, and treat the child as externally-owned for close-out accounting (poll its outcome like `reconcileChildOutcome` does on failure — but for the success path too).
- [ ] `internal/executor/dispatcher.go` create sites (`queueSingleTask`/`queueDecomposedTask`): `ErrClaimLost` → drop task silently (idempotent pickup).
- [ ] `cmd/pilot/commands.go:895` CLI: `ErrClaimLost` → user-facing "task already active".
- [ ] Retry deciders thread `generation+1`.

**Files**: `internal/executor/epic.go`, `internal/executor/dispatcher.go`, `cmd/pilot/commands.go`

### Phase 3: Inventory test + canary verification
- [ ] The entry-point inventory test (AC above).
- [ ] Keep all existing guards (they're now optimizations that avoid wasted GitHub API work, not correctness mechanisms) — do not remove in this task.

**Files**: `internal/executor/dispatch_claim_test.go`

---

## Out of Scope

- Removing the existing point guards (follow-up simplification once claims are proven in prod).
- The success-path sibling reconciliation UX beyond claim-loss accounting.
- The non-compiling WIP in the repo root (`resolveChildTerminalOutcome` projectPath threading) — separate owner; do not absorb or conflict with it.
- Poller lease tuning.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Claim location | new table · unique index on `executions` · in-memory registry | dedicated `execution_claims` table | avoids migrating the wide `executions` schema; SQLite-serialized across processes; proven idiom (#4319) |
| Retry semantics | delete claim on terminal · generation column | generation column | permanent rows immune to crash windows; retries claim generation+1 |
| Key | bare task_id · (task, project) · (task, project, generation) | (task, project, generation), canonicalized path | project scoping per #4297 lesson; generation prevents retry deadlock |
| Seam | new dispatcher gate · ExecutionLifecycle.Begin | Begin | ALL entry points already funnel through it (nav-research verified); TASK-404 built it for exactly this kind of consolidation |

---

## Verify

```bash
go test ./internal/executor/ -run 'Claim|Lifecycle' -race -count=5 -v
go test ./internal/memory/ -race
make test
make lint
```

---

## Done

- [ ] Two concurrent dispatches of one task = one execution, provably, under `-race`.
- [ ] Epic children claim before executing; poller loses gracefully (or vice versa).
- [ ] Scheduled epic-lifecycle canary green on `duplicate-pr`.
- [ ] make test / make lint green.

---

## Refs

- Research: nav-research gap matrix + incident history (2026-07-16, this session) — 10 incidents I1–I10, 7 entry points, 9 guards, all check-then-act.
- Incidents: #3828, #4020/#4022, #4110, #4140/TASK-394, #4216/TASK-401, #4217, #4276→#4297, incident-duplicate-cifix-2026-07-14 (#4309→#4319/#4321/#4322/#4323), #4300→#4327, **#4347 (open — becomes a consumer of this task)**.
- Canary tracker: #4265 (closes when this lands + scheduled run green).
- Pilot issue: https://github.com/qf-studio/pilot/issues/4349

---

**Last Updated**: 2026-07-16
