# TASK-394: Epic Sub-Issue Execution Ledger — kill double-intake, FK failures, starved success metrics

**Status**: ✅ SHIPPED 2026-07-09 — [#4140](https://github.com/qf-studio/pilot/issues/4140) closed, PRs [#4144](https://github.com/qf-studio/pilot/pull/4144)/[#4145](https://github.com/qf-studio/pilot/pull/4145) merged; daemon runs it since 20:11 UTC. Verify FK-silence + duplicate-free intake on the next live epic. Note: the reconciler false-positive (TASK-395) reproduced on #4140's own close — stale `pilot-needs-clarification` on the closed parent, parent cited as own child.
**Created**: 2026-07-09
**Assignee**: Pilot

---

## Context

**Problem** (incident 2026-07-09, epic GH-4127 → sub-issues #4128–#4131):

Epic executors run their sub-issues **in-process** (`internal/executor/epic.go:1924`
"Executing sub-issue") without ever creating an `executions` row. Three defects
cascade from that one omission:

1. **Double intake.** At sub-issue creation the epic calls `subIssuePollerSkip`
   → `Poller.MarkProcessed` (GH-3240/GH-4110). But that mark is only a
   **5-minute lease** (`retryGracePeriod`, poller.go:428). Sub-issues run
   10–30 min each, sequentially, so the lease always expires mid-epic. The
   poller's retry path (poller.go:1394–1447, parallel mode; 978–1028
   sequential) then re-evaluates: `IsTaskQueued("GH-N")` queries
   `executions WHERE status IN ('queued','pending','running')` (store.go:1599)
   → **no row exists** → false; the issue carries no status label (epic never
   labels children); no PR exists yet during the execution window, so
   `hasMergedWork`/`hasOpenPRAwaitingMerge` miss too → "Issue was processed
   but status labels removed, allowing retry" → duplicate dispatch. Live
   timeline: #4128 marked 18:47:56, re-dispatched 18:53:51 (5m42s later);
   PR #4133 appeared 18:59 — 6 min too late for the PR guards.
2. **Execution-event FK failures.** Every stage event for a sub-issue logs
   `Failed to record execution event execution_id=GH-4128 ... FOREIGN KEY
   constraint failed (787)` — events are keyed to a parent `executions` row
   that was never inserted (and pass the task ID where the schema expects the
   execution UUID). Started 2026-07-06; exclusive to sub-issue runs.
3. **Starved success metrics.** Shipped sub-issue work is never recorded
   `completed`. The duplicate queue entries later re-run, hit "harvested SHA
   is already on base branch — no new commit", and overwrite the rows with
   `no_op`. 2026-07-09 ledger: 4 completed + 4 no_op for ~7 shipped tasks
   (#4133/#4136/#4137 merged, #4138 in CI). Dashboard queue-depth sparkline
   (`GetDailyMetrics` counts `status='completed'` per day) flatlines; each
   duplicate re-run burns a full Claude invocation.

**Goal**: make in-process sub-issue executions first-class ledger citizens —
one `executions` row per sub-issue run — so poller ownership guards, execution
events, and daily success metrics all see them. Add a pre-execute merged-PR
short-circuit as defense in depth for queued duplicates.

---

## Known Pitfalls & Patterns

- **LEARNING** (95%, mem-027): TASK-359 Layer 1 spec Step 7 (tighten
  `HasCompletedExecution`) was **REFUTED** — it breaks direct-commit rows and
  `TestTaskCompletionInvariant`. Do NOT touch `HasCompletedExecution`
  semantics here; ownership must come from the new `running` row via
  `IsTaskQueued`. Reuse the atomic `MarkExecutionCompleted` in store.go.
- **LEARNING** (95%, mem-026): the epic path historically handles errors
  warn-only while the direct path is fatal. Ledger writes here must not abort
  a sub-issue run (log WARN and continue), but completion marking must follow
  the direct-path ordering established by `finalizeEpicBranchPR`.
- **PITFALL** (90%, mem-058): gate every dispatch/decompose entry point on
  parent state — the merged-PR short-circuit (Phase 3) is this pattern applied
  to the queue-drain entry point.
- **PITFALL** (90%, mem-065): five variants of "done without shipped code"
  exist; this task is the inverse (shipped code without "done"). The
  short-circuit must verify a **merged** PR for the exact `pilot/GH-N` branch
  before marking completed — never mark completed on branch existence alone.
- **PITFALL** (project memory): `executions.project_path` stores an ABSOLUTE
  path and scoped dashboard queries filter on it. Sub-issue rows must carry
  the parent project's absolute `project_path`, not the worktree path.

---

## Acceptance Criteria

- [ ] Executing a sub-issue in-process creates an `executions` row (UUID id,
      `task_id=GH-N`, `status='running'`, `project_path` = parent project's
      absolute path) **before** the backend starts, and marks it
      `completed`/`failed` on finish via the existing atomic
      `MarkExecutionCompleted` (with `pr_url`/`commit_sha` when available).
- [ ] Execution events recorded during a sub-issue run persist with zero
      `FOREIGN KEY constraint failed` warnings — events reference the new
      row's execution UUID, not the task ID.
- [ ] While a sub-issue is running in-process, `IsTaskQueued("GH-N")` returns
      true and the poller retry path skips it even after `retryGracePeriod`
      expiry. Test reproduces the live timeline: mark-processed at T, poller
      evaluation at T+5m42s, no status labels, no PR → **no dispatch**.
- [ ] Pre-execute merged-PR short-circuit: a queued task whose `pilot/GH-N`
      branch already has a **merged** PR completes as `completed` (with the
      merged PR URL) without invoking the backend — no more `no_op` re-runs
      of shipped work.
- [ ] `GetDailyMetrics` SuccessCount credits sub-issue completions (test:
      epic runs N sub-issues → N+1 completed rows counted for the day).
- [ ] `TestTaskCompletionInvariant` and the full existing suite pass;
      `HasCompletedExecution` behavior unchanged.

---

## Implementation

### Phase 1: Ledger row for in-process sub-issue runs
**Goal**: one `executions` row per sub-issue run, created before execution,
finalized after.

**Tasks**:
- [ ] In the epic sub-issue execution path (`epic.go` ~1924), insert an
      `executions` row (UUID, `task_id`, `status='running'`, absolute
      `project_path`, `created_at`) before invoking the backend. WARN-and-
      continue on insert failure (mem-026), but thread the UUID through.
- [ ] Mark the row `completed`/`failed`/`no_op` at sub-issue finalization
      using `MarkExecutionCompleted`, including tokens/cost/pr_url/commit_sha
      already tracked by the epic runner.
- [ ] Route execution-event recording for sub-issues through the new UUID
      (fixes the `execution_id=GH-N` FK failures at dispatcher.go:451,
      runner.go:1299).

**Files**:
- `internal/executor/epic.go` — sub-issue exec loop, finalization
- `internal/executor/runner.go`, `internal/executor/dispatcher.go` — event
  recording key
- `internal/memory/store.go` — reuse existing insert/complete helpers (add a
  narrow helper only if none fits)

### Phase 2: Poller ownership regression test
**Goal**: prove the retry path can no longer re-dispatch an epic-owned child.

**Tasks**:
- [ ] Table-driven test in `internal/adapters/github/poller_test.go`
      (parallel + sequential retry blocks): processed-mark older than grace,
      no status labels, no PR, but `IsTaskQueued` true → skip with
      `ReasonTaskQueued`; same scenario with no running row → dispatch
      (documents the old bug).

**Files**:
- `internal/adapters/github/poller_test.go`

### Phase 3: Pre-execute merged-PR short-circuit (defense in depth)
**Goal**: a stale duplicate that still reaches the queue must not burn a
Claude invocation and must not overwrite history with `no_op`.

**Tasks**:
- [ ] Before backend invocation in the dispatcher/runner execute path, check
      for a **merged** PR on `pilot/GH-N` (reuse `FindMergedPRByBranch` from
      TASK-359). If found: mark execution `completed` with that PR URL, skip
      execution entirely, log INFO.
- [ ] Unit test: queued task + merged PR → completed without backend call;
      open-but-unmerged PR → unchanged current behavior.

**Files**:
- `internal/executor/dispatcher.go` (or runner pre-flight)
- `internal/executor/dispatcher_test.go`

---

## Out of Scope

- Epic parent timeout semantics (1h task timeout is structurally wrong for a
  parent running N sub-issues sequentially — parent GH-4127 hit it and was
  briefly `infra`). Separate follow-up.
- Applying `pilot-in-progress` labels to sub-issues (restart-safe, human-
  visible ownership signal) — nice-to-have follow-up, not needed once the
  ledger row exists.
- Intent-judge false positives from truncated diffs (`max_files: 8` /
  `max_bytes: 24000`) — unrelated to intake; separate issue.
- Any change to `HasCompletedExecution` (refuted in TASK-359).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Ownership signal for in-process sub-issue runs | (a) executions row → `IsTaskQueued`; (b) `pilot-in-progress` label on child; (c) extend `retryGracePeriod` | (a) executions row | Works for every poller mode via existing guard (poller.go:1408); simultaneously fixes event FK failures and metrics starvation; DB-persisted so restart-safe. (b) deferred as hardening; (c) treats symptom, still races long epics. |
| Duplicate handling at queue drain | (a) merged-PR short-circuit → `completed`; (b) keep `no_op` re-run | (a) | Re-runs burn a full Claude invocation to discover nothing to do, and `no_op` overwrites truthful history. Merged PR on the exact branch is unambiguous evidence of shipped work (mem-065 gate). |
| Ledger write failure during epic run | (a) abort sub-issue; (b) WARN and continue | (b) | Epic path error contract is warn-only (mem-026); a ledger write must never block shipping code. |

---

## Verify

```bash
make test          # full suite incl. new poller + dispatcher + epic tests
make lint
go test ./internal/adapters/github/ ./internal/executor/ ./internal/memory/ -run 'TaskQueued|SubIssue|MergedPR|CompletionInvariant' -v
```

Live validation (post-merge, next epic run): daemon log shows zero
`FOREIGN KEY constraint failed (787)` warnings; `sqlite3 ~/.pilot/data/pilot.db
"SELECT task_id,status FROM executions WHERE date(created_at)=date('now')"`
shows sub-issues as `completed`; no "allowing retry" line for epic children;
dashboard queue-depth sparkline credits the day.

---

## Done

- [ ] Epic sub-issue runs produce `running`→`completed` executions rows with
      correct absolute `project_path`
- [ ] Zero execution-event FK warnings on an epic run
- [ ] Poller regression tests pass (retry path blocked by running row)
- [ ] Merged-PR short-circuit test passes; no backend call on shipped dups
- [ ] Full suite + lint green; `TestTaskCompletionInvariant` untouched and green

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4140
- Incident analysis: interactive session 2026-07-09 (queue-depth graph flatline)
- GH-4110 / commit 379c274b — poller registry fix that surfaced this (mark now
  reaches SDK poller but expires after 5 min)
- GH-3240 (sub-issue skip-mark), GH-2201 (retry grace period), GH-2242/GH-3269
  (completed-execution guards)
- TASK-359 — `finalizeEpicBranchPR`, `FindMergedPRByBranch`, atomic
  `MarkExecutionCompleted` (mem-026/mem-027)
- Evidence: `~/.pilot/logs/daemon.log` 2026-07-09 18:47–20:02 CEST; executions
  rows GH-4127…GH-4131 dated 2026-07-09

---

**Last Updated**: 2026-07-09
