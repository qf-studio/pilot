# GH-4683

**Created:** 2026-08-03

## Problem

GitHub Issue GH-4683: fix(autopilot): self-upgrade drain deadlocks on a saturated queue — pause admission, wait only for running executions

## Context

2026-08-03 v2.252.0 rollout: the release train published at 14:27Z and self-upgrade started at 14:28Z ("Waiting for 4 task(s) to complete..."). The drain waits for **all active executions (running + queued)** while the dispatcher **keeps admitting new work mid-drain** — #4677 was queued at 14:42Z, #4656 re-queued at 15:07Z, #4669 re-queued at 15:37Z, all after upgrade start. Two 30-minute drain windows timed out against a queue that never dropped below ~5; `self-upgrade failed` ERROR at 15:28Z. Operator had to manually install the binary and restart the daemon.

The failure compounded itself: the old binary's heartbeat bug (GH-4668, fixed by the very release being installed) kept churning tasks back into the queue with `infra` outcomes, guaranteeing the queue never emptied. A busy box structurally cannot self-upgrade.

## Acceptance

- While a self-upgrade is pending, the dispatcher stops starting new executions: queued rows stay queued, no new picks, pollers may continue enqueuing.
- The drain waits only for currently **running** executions (bounded by the existing timeout window); queued work survives the restart and resumes on the new binary.
- With 1 running + 5 queued tasks, the upgrade completes shortly after the running task finishes instead of timing out.
- The drain-timeout alert distinguishes "waiting on running task(s)" from the now-impossible "blocked by queue depth".

## Resolution (2026-08-03)

Root cause: `executor.Monitor.GetRunningTaskIDs()` returns task IDs for both
`StatusRunning` and `StatusQueued` — correct for its original consumer, the
orphan-running sweep's `autopilot.TaskMonitor` exclusion set ("is this task ID
live in the daemon's memory at all"). But `*executor.Monitor` was also wired
in as `upgrade.TaskChecker`'s implementation for the self-upgrade drain, and
Go's structural interface satisfaction let the same method serve that
interface too — even though `TaskChecker` needs "is this task *actually
running right now*," a strictly narrower question. With the dispatcher still
admitting new work mid-drain (pollers re-queuing retries per the GH-4668
heartbeat bug), the running+queued count never hit zero and the drain timed
out twice before failing outright.

**Fix** — two independent, complementary changes:

1. **Narrow the drain's view of "running"**: added
   `Monitor.GetActivelyRunningTaskIDs()` (`internal/executor/monitor.go`),
   `StatusRunning` only, reusing the existing `ReconcileDeadOwners` call so a
   dead owner's zombie "running" entry still can't block a drain forever.
   `Monitor.WaitForTasks` now polls this new method instead of
   `GetRunningTaskIDs`, and its timeout error now reads `"waiting on %d
   running task(s): %v"` (previously implied "blocked by queue depth" was
   possible — it no longer is, satisfying the alert-wording acceptance
   criterion directly, since `reportUpgradeFailure` surfaces this error text
   verbatim). `GetRunningTaskIDs()` itself is unchanged — its running+queued
   contract is still correct for the orphan-sweep's `TaskMonitor` interface,
   and changing it would have broken that existing, intentional behavior
   (and its tests).
2. **Stop the queue refilling during a drain attempt**: added
   `Dispatcher.PauseAdmission()` / `ResumeAdmission()` / `AdmissionPaused()`
   (`internal/executor/dispatcher.go`), backed by a shared `atomic.Bool`
   wired into every `ProjectWorker` via a new `setAdmissionGate` setter
   (mirrors the existing `SetLiveWorkerChecker` pattern; nil-safe default
   preserves today's always-admit behavior for the ~18 test call sites that
   construct a `ProjectWorker` directly without wiring the gate).
   `ProjectWorker.processQueue`'s main loop checks the gate *before* fetching
   the next queued row — a task already mid-execution when the pause begins
   is left completely alone and runs to completion normally; queued rows
   simply stay queued in the store, and `ResumeAdmission` re-signals every
   worker so anything queued during the pause is picked up immediately.
   `cmd/pilot/main.go`'s self-upgrade goroutine now wraps each upgrade
   attempt in `dispatcher.PauseAdmission()` / `defer
   dispatcher.ResumeAdmission()`, and routes the drain through a new
   `monitorRunningTaskChecker` adapter (`cmd/pilot/upgrade.go`) that
   implements `upgrade.TaskChecker` via the narrow
   `GetActivelyRunningTaskIDs()` method instead of the dual-purpose one.

Regression tests: `internal/executor/monitor_test.go`
(`TestMonitorGetActivelyRunningTaskIDs_ExcludesQueued`,
`TestMonitorWaitForTasks_QueuedTasksDoNotBlockDrain`,
`TestMonitorWaitForTasks_RunningPlusQueued_ResolvesWhenRunningCompletes` — the
last one is the literal "1 running + 5 queued" acceptance scenario);
`internal/executor/dispatcher_test.go`
(`TestDispatcher_PauseAdmission_QueuedTasksStayQueuedUntilResumed`,
`TestDispatcher_PauseAdmission_RunningTaskUnaffected`). `go build ./...` and
`go test ./internal/executor/... ./cmd/pilot/... ./internal/upgrade/...` all
green; `gofmt`/`go vet` clean. `golangci-lint` is not installed on this box
(no lint-only changes were made beyond what `gofmt`/`go vet` already cover).

A pitfall memory documenting the "one method, two interface contracts" root
cause was recorded at
`.agent/knowledge/memories/pitfalls/one-method-two-interface-contracts-self-upgrade-drain.md`.

## Refs

- Incident: v2.252.0 rollout 2026-08-03, `daemon.log` 14:28–15:28Z on the founder box
- Compounding factor: GH-4668 heartbeat churn (its fix was aboard the blocked release)

## Acceptance Criteria

