# TASK-419: Epic sub-issue claim-loss self-deadlock — the ProjectWorker waits forever for a child only it can run

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-07-24
**Assignee**: Pilot

---

## Context

**Problem**:
On 2026-07-24 epic GH-4531 shipped sub-issue 1 (GH-4532 → PR #4534, merged)
and then hung forever without ever starting sub-issue 2 (GH-4533). The parent
`executions` row sat `running` for 35+ minutes with **zero claude/node
processes alive**, holding the project's only worker slot and its claim, until
an operator killed it by hand. Nothing reaped it.

This is not a timeout-tuning bug. It is a **structural self-deadlock**: the
goroutine blocked waiting for a queued child is the only goroutine that could
ever run that child.

**Observed sequence** (daemon v2.245.2):
```
18:42:59  executions row for GH-4533 created, status=queued   (claimed by the
          GitHub poller, which then dropped its own dispatch — orphan owner)
18:50:05  merge-wait decision: not waiting   dependency_reason=none
18:50:08  Sub-issue completed  sub_issue=4532  pr=4534
18:50:09  sub-issue dispatch claim lost — another channel already owns this
          execution; polling for its outcome   sub_issue=4533
   (…nothing, forever. "Executing sub-issue order=2" never appears.
    Meanwhile every poll tick: poller dispatches 4533 → "dispatch claim lost
    — task already owned by another dispatch channel" → drops.
    "stale recovery complete, reset N tasks count=0" repeats throughout.)
```

**Root cause** — three individually-reasonable decisions compose into a
guaranteed hang:

1. `epic.go:2566-2579` — on `ErrClaimLost`, the epic does not fail; it calls
   `reconcileChildOutcome(ctx, taskID, path, "", nil, nil, /*externallyOwned=*/true)`
   to wait for the "winning" channel's outcome (GH-4359).
2. `epic.go:2063-2083` + `epic.go:2129-2134` — **GH-4413** deliberately made
   the reconcile timeout bound only time the child spends **running**. While
   the child is `queued` (or no row is visible) it "keeps polling with **no
   ceiling at all**". Its stated safety net is that
   `recoverStaleQueuedTasks` reaps a queued row "whose owning project has no
   live worker", and that "a queued row with a live worker is … just waiting
   its turn and must not be timed out."
3. That safety net cannot fire here. `recoverStaleQueuedTasks`/
   `recoverStaleRunningTasks` skip any row whose project passes
   `hasLiveWorker` (`dispatcher.go:499`, `dispatcher.go:715`), and
   `hasLiveWorker` (`dispatcher.go:777-782`) is **pure map presence** —
   `grep -c 'delete(d\.workers' internal/executor/dispatcher.go` returns
   **0**, so a worker is never removed from the map, alive, wedged, or dead.

The project **does** have a live worker — it is the one blocked in step 2,
inside `Runner.Execute` called synchronously at `dispatcher.go:2037`. Under
the one-worker-per-project model (TASK-393 ProjectWorker), a queued child of
the epic that worker is running can **never** start. The waiter is the runner.

`ctx` cancellation is documented as "the only bound while queued" — and the
epic branch never gets a deadline: `Runner.Execute` builds its watchdog
context at `runner.go:2454-2462`, which is **after** the epic block
(`runner.go:2125-2416`, returning at `2413`). Epics therefore run under the
dispatcher's daemon-lifetime context. There is no bound at all.

Because the parent's terminal status is written only by the shared chokepoint
`w.lifecycle.Persist` at `dispatcher.go:2122` — reached only after
`Execute()` returns — the parent row can never go terminal either.

**Goal**:
Make this class of hang impossible: a queued child that the blocked worker
itself owns must be detected and resolved, not waited on; and no epic may
occupy a worker without a deadline.

---

## Known Pitfalls & Patterns

- **PITFALL** `claim-lost-drops-count-toward-hard-cap`: while the epic hung,
  the poller's rejected re-dispatches incremented the repick counter to 5/5,
  which would have force-stalled a task that was (nominally) running. Do not
  "fix" this by making claim-loss louder in the poller — fix the deadlock.
- **PITFALL** `hard-cap-rearm-in-memory-gate`: recovery from this state
  required 6 manual steps including a DB surgery and a daemon restart. A fix
  that only shortens the hang without producing a terminal status still leaves
  operators doing surgery.
- **DECISION** "Where parallelism lives: ProjectWorker pool — it is the sole
  serialization point": that decision is what makes self-ownership fatal here.
  The fix must respect it, not add a second execution path around it.

---

## Acceptance Criteria

- [ ] An epic whose sub-issue `Begin()` returns `ErrClaimLost`, where the
      child's execution row is `queued` and owned by a channel that is not
      making progress, terminates in **bounded** time rather than polling
      forever.
- [ ] Self-ownership is detected explicitly: when the goroutine executing the
      epic is the project's own worker, a `queued` child of that epic is
      recognized as unrunnable-by-anyone-else and is **not** treated as
      "waiting its turn".
- [ ] On that detection the epic either takes over the child's execution
      (preferred — the work still gets done) or fails the sub-issue with a
      distinct, greppable reason. It must not hang, and must not silently
      succeed.
- [ ] The epic branch of `Runner.Execute` runs under a bounded context, so
      `Execute()` always returns and `dispatcher.go:2122`'s
      `lifecycle.Persist` always writes a terminal parent status.
- [ ] `hasLiveWorker` reflects liveness, not map presence: a worker goroutine
      that exits (normally, by ctx cancellation, or via the `SafeGo` panic
      recover at `dispatcher.go:1618-1620`) no longer counts as live, so the
      stale-recovery sweeps can actually reap rows behind it.
- [ ] No regression to GH-4413's real goal: a child legitimately queued behind
      *other* work on a busy project, with a *different* live worker making
      progress, must still not be timed out.

---

## Implementation

### Phase 1: Detect self-ownership in the claim-lost wait (root cause)
**Goal**: The blocked worker must never wait on a child only it can run.

**Tasks**:
- [ ] In `reconcileChildOutcome` (`epic.go:2084`), the `externallyOwned=true`
      queued-phase poll gains a self-ownership check: if the child's
      `project_path` is the same project whose worker is executing this epic,
      the "just waiting its turn" assumption is false by construction.
- [ ] Plumb whatever identity is needed (project path is already on the
      subtask; if worker identity is required, pass it down from
      `dispatcher.go:2037` / `executeSubIssuesTracked`) — do not infer it from
      globals.
- [ ] On detection: prefer **takeover** — steal/re-claim the orphan claim and
      execute the child inline (it is the same worker that would have run it
      anyway), routing the re-claim through the existing
      `beginWithGenerationRetry` + shared `repick_backoff` store, per the
      warning at `epic.go:2556-2562` about not re-implementing a second
      driftable copy. If takeover is rejected, fail the sub-issue with a
      distinct error naming the deadlock.
- [ ] Add an absolute ceiling on the queued phase as a backstop, so an
      unforeseen variant degrades to a bounded failure instead of a hang.

### Phase 2: Bound the epic branch
**Goal**: `Execute()` always returns, so the parent always goes terminal.

**Tasks**:
- [ ] Hoist the watchdog-context construction (`runner.go:2454-2462`) to
      **before** the epic-detection branch at `runner.go:2125` so the epic
      block runs under it.
- [ ] Budget deliberately: one shared deadline across N sub-issues can starve
      the last sub-issue. Either derive a per-sub-issue timeout inside the
      loop, or size the epic ceiling from the sub-issue count. State the
      choice in a comment.
- [ ] Verify `recordEpicTerminalEvent` (`runner.go:1898-1907`, audit-only) is
      not mistaken for the status write — the status write stays at
      `dispatcher.go:2122`.

### Phase 3: Make `hasLiveWorker` mean live
**Goal**: The stale-recovery sweeps stop being unreachable.

**Tasks**:
- [ ] Remove the worker from `d.workers` when its goroutine exits — a
      `defer` inside `ProjectWorker.Run` guarded by `d.mu`, so it also covers
      the `SafeGo` panic-recover path (`dispatcher.go:1618-1620`), which today
      leaves a project permanently dead-but-"live" until a daemon restart.
- [ ] Alternatively/additionally, a heartbeat timestamp the sweeps can test.
      Whichever is chosen, `dispatcher.go:499` and `dispatcher.go:715` must be
      able to reap behind a wedged worker.
- [ ] Preserve the `dispatcher.go:167` startup case ("nothing alive yet").

**Files**:
- `internal/executor/epic.go` — claim-lost path + reconcile self-ownership
- `internal/executor/runner.go` — epic-branch context bounding
- `internal/executor/dispatcher.go` — worker liveness + sweeps

---

## Out of Scope

- Re-architecting `subIssueMergeWait` into an event-driven "resume epic on
  child-PR-merge" design. It was **not** implicated here
  (`dependency_reason=none`, it explicitly did not wait), and it is a separate
  throughput initiative.
- Changing the TASK-407 claim mechanism. It behaved **correctly** — it
  prevented a double execution. Only the loser's fallback is defective.
- The poller-side defect where a channel claims a sub-issue, creates a
  `queued` row, then abandons it without cleanup (the orphan owner at
  18:42:59). Real, but a separate issue — Phase 1 must survive orphan owners
  regardless of who creates them.
- Alert-engine tuning (`engine.go:657-691` stuck-task watchdog freezing at
  child-start because the parent heartbeat only refreshes per child,
  `epic.go:2496-2507`). File separately.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to break the deadlock | Global timeout only; self-ownership detection; abolish the claim-lost wait | Self-ownership detection **+** timeout backstop | A pure timeout re-breaks GH-4413 (queue-wait erosion) and still wastes the full ceiling on every occurrence; detection fixes the actual invariant and the timeout catches unforeseen variants |
| Response on detection | Fail the sub-issue; take over and run it | Take over, fail only if re-claim is refused | The blocked worker is exactly the worker that should run the child; failing wastes a correct, already-planned sub-issue and leaves the epic half-shipped (which is what happened) |
| Re-claim path | New generation-retry logic; reuse `beginWithGenerationRetry` | Reuse | `epic.go:2556-2562` explicitly warns a second copy will drift from the shared `repick_backoff` store |
| Worker liveness | Heartbeat timestamp; delete-on-exit | Delete-on-exit (heartbeat optional) | Smallest correct change; also fixes the panic-kills-project-forever case in the same stroke |

---

## Verify

```bash
make build
go test ./internal/executor/...
make lint
```

---

## Done

- [ ] A test drives an epic through the **real** `Dispatcher` →
      `ProjectWorker` → `Runner.Execute` path (not `ExecuteSubIssues`
      directly) with a sub-issue whose claim is pre-held by an orphan owner,
      and asserts the run terminates bounded with a terminal parent status.
      Existing coverage cannot catch this: every test in
      `epic_sequential_test.go:667-813` injects an instantly-returning mock,
      and `TestSequentialEpicFlow_ContextDeadline`
      (`epic_sequential_test.go:611-659`) hand-builds a bounded context and
      passes it straight to `ExecuteSubIssues`, bypassing the very code path
      (`Runner.Execute`) that fails to build one.
- [ ] A test asserts a worker that exits or panics is no longer `hasLiveWorker`,
      and that the stale sweeps then reap rows behind it.
- [ ] A test asserts the GH-4413 behaviour is preserved: a child queued behind
      other work on a project with a different, progressing worker is not
      timed out.
- [ ] `make build`, `make lint`, and `go test ./internal/executor/...` green.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4536 (`no-decompose`)
- Incident: epic GH-4531 (2026-07-24). Sub-issue 1 GH-4532 → PR #4534 merged
  18:57:45Z; sub-issue 2 GH-4533 never started; parent zombie `running` for
  35+ min; operator surgery + daemon restart to recover. GH-4533 later shipped
  fine standalone (PR #4535, merged 20:18:59Z) — proving the sub-issue was
  always runnable and only the epic path was broken.
- `epic.go:2063-2083` — GH-4413's own comment stating the no-ceiling queued
  phase and naming the safety net that cannot fire.
- GH-4359 — why claim-loss polls instead of failing.
- GH-2331 — `recoverStaleQueuedTasks` / `StaleQueuedThreshold`.
- TASK-407 — atomic dispatch-admission claim (correct; not the defect).
- TASK-393 — ProjectWorker as the sole serialization point.
- TASK-359 — daemon finalization hardening (the stall-before-push family).

---

**Last Updated**: 2026-07-24
