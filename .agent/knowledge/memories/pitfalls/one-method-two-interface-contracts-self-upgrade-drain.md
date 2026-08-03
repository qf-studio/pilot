---
name: One concrete method silently satisfying two differently-scoped interfaces
description: executor.Monitor.GetRunningTaskIDs() (running+queued, for the orphan-sweep's TaskMonitor contract) structurally also satisfies upgrade.TaskChecker (should be running-only) — Go's structural typing let the wrong semantics leak into self-upgrade's drain with no compiler signal, causing v2.252.0's drain to wait on a dispatcher-refilled queue and time out twice (GH-4683, 2026-08-03).
type: pitfall
---

## What

`internal/executor/monitor.go`'s `Monitor.GetRunningTaskIDs()` returns task IDs
for both `StatusRunning` **and** `StatusQueued` entries. That's correct for its
original consumer — the `autopilot.TaskMonitor` interface backing the
orphan-running sweep's exclusion set, where "is this task ID live in the
daemon's memory in any way" is exactly the right question.

`internal/upgrade/graceful.go`'s `upgrade.TaskChecker` interface declares a
method with the *same name and signature* —
`GetRunningTaskIDs() []string` — but a different implied contract: "which
tasks are actually executing right now, i.e. must block a drain." Because Go
interfaces are satisfied structurally (no explicit `implements` declaration),
`*executor.Monitor` satisfied `upgrade.TaskChecker` automatically the moment
someone wired it in as the self-upgrade drain's `TaskChecker` — no compiler
error, no lint warning, nothing to flag that the method's actual behavior
(running+queued) didn't match what the interface's name/doc implied it should
do (running only).

## Why this matters

The self-upgrade drain (`HotUpgrader.PerformHotUpgrade` → `WaitForTasks`)
polled `GetRunningTaskIDs()` and waited for it to hit zero. With the dispatcher
still admitting new work mid-drain (pollers re-queuing retries per the GH-4668
heartbeat bug being fixed in the very release being installed), the
running+queued count never dropped below ~5, and the drain timed out twice
(two 30-minute windows) before failing outright and requiring manual
intervention on the v2.252.0 rollout (2026-08-03).

The bug was invisible at the type level: `TaskChecker` compiled fine, tests for
`GetRunningTaskIDs()` itself passed (they correctly assert running+queued,
because that's right for the *other* interface), and nothing about the
`Monitor` struct signaled "this method is dual-purposed with conflicting
semantics for its two callers."

## How to apply

- When a single concrete method backs two different named interfaces, check
  whether both interfaces actually want the *same* semantics — don't assume
  structural satisfaction implies contractual correctness. Read each
  interface's doc comment/name, not just its signature.
- Fix by adding a new, narrowly-scoped method (here:
  `Monitor.GetActivelyRunningTaskIDs()`, `StatusRunning` only) and pointing the
  interface that actually wants that narrower contract (here: an adapter,
  `monitorRunningTaskChecker` in `cmd/pilot/upgrade.go`) at the new method —
  rather than changing the existing method's behavior, which would silently
  break its original (correctly running+queued) consumer and its existing
  tests.
- Prefer a small adapter type over widening/overloading one method when two
  interfaces genuinely need different views of the same underlying state.
- Grep for every interface a type satisfies before changing what a shared
  method returns — `grep -rn "GetRunningTaskIDs" --include=*.go` here would
  have surfaced both call sites (dispatcher's `TaskMonitor` wiring and
  upgrade's `TaskChecker` wiring) immediately.

## Evidence

- `internal/executor/monitor.go` — `GetRunningTaskIDs()` (running+queued,
  unchanged) vs. the new `GetActivelyRunningTaskIDs()` (running only)
- `internal/upgrade/graceful.go` — `TaskChecker` interface definition
- `cmd/pilot/upgrade.go` — `monitorRunningTaskChecker` adapter routing
  `upgrade.TaskChecker` to the narrow method
- `internal/executor/dispatcher.go` — companion fix: `PauseAdmission`/
  `ResumeAdmission` stop the dispatcher from refilling the queue during a
  drain attempt in the first place, so admission and drain-scope are both
  addressed
- `.agent/tasks/gh-4683.md` — incident timeline and acceptance criteria

## Related

- [[bug_hot_upgrade_restarting_ui_trap]] — a different hot-upgrade UX pitfall
  in the same subsystem
