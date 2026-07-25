# TASK-421: Repick hard cap counts non-failures — a task queued behind a long task is auto-blocked for waiting its turn

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-07-25
**Assignee**: Pilot

---

## Context

**Problem**:
`consecutive_drops` increments on any dispatch attempt that fails to produce a
new execution — including attempts the dispatcher **correctly refused because
the task was already queued or already running**. At
`dispatcherRepickHardCap = 5` (`dispatcher.go:950`) the task is marked
`stalled` and labelled `pilot-blocked`, requiring manual 6-step recovery.

The result: **any task queued behind a task that runs longer than ~37 minutes
is auto-blocked for waiting its turn.** It never failed. It never started.

**Reproduction — GH-4537, 2026-07-24 (verbatim):**
```
22:19:57.985  task queued behind GH-4536 (position 1 in pilot queue)   ← correct
22:25:28      Dispatching issue 4537 → dispatch claim lost — task already
              owned by another dispatch channel → unmarking for retry
22:31:26      (same, repeating every ~6 min)
22:57:04      consecutive_drops=6 → hard cap → stalled + pilot-blocked
```
GH-4536 ran ~40 min, so GH-4537 was doomed the moment it was dispatched. It sat
blocked overnight (~10h) until an operator re-armed it.

**Three distinct false-stall variants in 24 hours, same counter:**

| Task | Drops accrued while… | Reality |
|---|---|---|
| GH-4526 | environment failures (hosted `git_clean` preflight deadlock, CI infra outage) | task was fine; the box was broken |
| GH-4531 | legitimately **running** (poller raced the epic it was executing) | live execution, `consecutive_drops=5` |
| GH-4537 | legitimately **queued** behind GH-4536 | never started |

**Root cause, two halves:**

1. **The poller re-dispatches work it already owns.** `Dispatcher.IsActive`
   (`dispatcher.go:797`) exists precisely to answer *"is this taskID already
   queued or running in this project?"* — and is called from exactly one place,
   `cmd/pilot/handler_common.go:102`. The GitHub SDK poller path does **not**
   consult it, so it re-dispatches an already-queued task every poll cycle,
   generating a guaranteed rejection each time.
2. **The rejection is counted as a failed re-pick.** A claim refused because
   the task is already active is not a failure — nothing was attempted. It
   still grows the same `consecutive_drops` counter that gates the hard cap.

Prior art shows this is the third pass at the class and each pass narrowed it
without closing it. `store.go:419-426` already carries a **separate**
`stall_drops` counter added so stall-kills would "not grow the same
`consecutive_drops` counter a genuine failure does" (#4455 — restart churn and
operator cancels no longer count). Claim-lost drops never received the same
treatment; memory `claim-lost-drops-count-toward-hard-cap` records exactly that
gap, and it has now cost three tasks in one day.

**Goal**:
`consecutive_drops` counts genuine failed re-picks only. Being queued, being
already-running, and environment faults must never consume a task's retry
budget.

---

## Known Pitfalls & Patterns

- **PITFALL** `claim-lost-drops-count-toward-hard-cap`: the exact known gap
  this task closes. #4455 narrowed the class but explicitly did not close it.
- **PITFALL** `hard-cap-rearm-in-memory-gate`: recovery costs 6 manual steps
  including DB surgery, a label edit, and often a daemon restart. Every false
  stall is expensive — this is why prevention matters more than faster
  recovery.
- **DECISION** "Concurrency default: opt-in (`default 1`)": with one worker
  per project, queueing behind a long task is the **normal** state, not an
  anomaly. The counter must be safe under the project's own default config.
- TASK-419 (#4536, PR #4538 merged) fixed the epic self-deadlock that produced
  the GH-4531 variant, but did **not** touch this counter.

---

## Acceptance Criteria

- [ ] A dispatch attempt refused because the task is already `queued` or
      `running` does **not** increment `consecutive_drops`.
- [ ] A task that sits queued behind another task for an arbitrarily long time
      is never marked `stalled` or labelled `pilot-blocked` on that basis
      alone.
- [ ] The poller consults `Dispatcher.IsActive` (or equivalent) before
      dispatching, so the redundant re-dispatch stops being generated at the
      source rather than only being forgiven downstream.
- [ ] Genuine failed re-picks still reach the cap: a task whose execution is
      actually attempted and fails repeatedly still stalls at
      `dispatcherRepickHardCap`.
- [ ] Environment/infra-classified failures do not consume the budget the same
      way code failures do — reuse the `failure_class` vocabulary shipped in
      TASK-418 (PR #4535) rather than inventing a parallel taxonomy.
- [ ] The distinction is observable: the `repick storm` WARN
      (`dispatcher.go:1250`) names *why* a drop was counted or forgiven, so the
      next false stall is diagnosable from the log alone.

---

## Implementation

### Phase 1: Stop generating the redundant dispatch
**Goal**: The poller does not re-dispatch what it already owns.

**Tasks**:
- [ ] Have the GitHub SDK poller path check `Dispatcher.IsActive(taskID,
      projectPath)` (`dispatcher.go:797`) before dispatching, matching the
      existing `cmd/pilot/handler_common.go:102` usage.
- [ ] When already active, log at debug/info and skip — do not route through
      the drop path at all.

### Phase 2: Classify the drop before counting it
**Goal**: Only genuine failed re-picks grow `consecutive_drops`.

**Tasks**:
- [ ] At the drop site feeding `dispatcher.go:1250`/`1305`, distinguish
      "refused because already active" from "re-picked and failed". Do not
      increment `consecutive_drops` for the former.
- [ ] Follow the established precedent: `store.go:419-426`'s `stall_drops`
      column shows the accepted shape for a non-gating counter. Either reuse
      that mechanism or add a sibling — do not overload `consecutive_drops`.
- [ ] Apply the same forgiveness to environment/infra-classified failures via
      TASK-418's `failure_class` (PR #4535, `internal/autopilot`), so the
      GH-4526 variant is covered too.

### Phase 3: Make it diagnosable
**Goal**: A future false stall is readable from the log.

**Tasks**:
- [ ] Extend the `repick storm` WARN (`dispatcher.go:1250`) and the stall
      reason (`dispatcher.go:1305`) to carry the drop classification and the
      counted-vs-forgiven decision.

**Files**:
- `internal/executor/dispatcher.go` — drop classification, cap, `IsActive` use
- `internal/memory/store.go` — counter columns (`repick_backoff`)
- the GitHub SDK poller dispatch path — pre-dispatch `IsActive` check

---

## Out of Scope

- Raising or removing `dispatcherRepickHardCap` (`dispatcher.go:950`). The cap
  is correct; what it counts is not.
- Changing the TASK-407 claim mechanism. Refusing a duplicate claim is right —
  only the bookkeeping that follows is wrong.
- The dashboard's rendering of stalled/queued tasks (TASK-420 / #4537).
- Auto-recovery of already-blocked issues. This prevents new false stalls; it
  does not un-block historical ones.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to fix | Poller only; counter only; both | **Both** | Poller-only leaves other dispatch channels able to generate the same drops; counter-only leaves a pointless re-dispatch every 6 min burning API quota |
| Counter design | Reset on queue; separate non-gating counter; forgive at increment | Forgive at increment + separate counter, following `stall_drops` | `store.go:419-426` already established this shape for #4455; a third pattern invites the drift that produced this bug |
| Environment failures | Separate task; fold in | Fold in | TASK-418's `failure_class` shipped yesterday (PR #4535) and makes this a small addition; leaving it out means a 4th false-stall variant remains live |
| Cap value | Raise to buy headroom; leave at 5 | Leave at 5 | Raising it only delays a false stall and weakens the real protection |

---

## Verify

```bash
make build
go test ./internal/executor/... ./internal/memory/...
make lint
```

---

## Done

- [ ] A test reproduces GH-4537: task B queued behind long-running task A,
      poller attempts dispatch N > cap times, and B is still queued —
      not `stalled`, not `pilot-blocked`.
- [ ] A test asserts a genuinely failing task still stalls at the cap
      (no regression in the real protection).
- [ ] A test asserts the poller skips dispatch when `IsActive` is true.
- [ ] `make build`, `make lint`, `go test ./internal/executor/... ./internal/memory/...`
      green.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4540 (`no-decompose`)
- GH-4537 (2026-07-24 22:19:57→22:57:04Z) — queued behind GH-4536, 6 drops,
  false stall, ~10h blocked overnight.
- GH-4531 (18:14→18:22Z) — drops while legitimately running; `consecutive_drops=5`
  against a live execution.
- GH-4526 (→16:00Z) — drops from environment failures (hosted `git_clean`
  deadlock + CI infra outage).
- #4455 — narrowed the class (restart churn, operator cancels); `stall_drops`
  precedent at `store.go:419-426`.
- Memory `claim-lost-drops-count-toward-hard-cap` — the documented open gap.
- TASK-418 / PR #4535 — `failure_class` vocabulary to reuse.
- TASK-419 / PR #4538 — fixed the epic deadlock behind the GH-4531 variant,
  not this counter.

---

**Last Updated**: 2026-07-25
