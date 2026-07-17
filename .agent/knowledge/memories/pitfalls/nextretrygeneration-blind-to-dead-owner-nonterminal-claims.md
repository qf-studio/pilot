# Pitfall: nextRetryGeneration treats a dead owner's non-terminal claim as live forever

## Summary
`nextRetryGeneration` (#4373) only advances the retry generation when the
claimed execution it finds is in a **terminal** status — a claim held by a
row still `queued` or `running` reads as "live owner" unconditionally, even
if the daemon that claimed it is dead. A daemon restart/cutover that kills a
process mid-flight leaves exactly this: `execution_claims` rows pinned at
generation 0 whose owning execution never transitioned out of `queued`.
Every subsequent dispatch attempt for that task drops silently with
"dispatch claim lost", forever — the periodic stale-recovery sweep does not
save you here because (pre-fix) it only ever looked at `running` rows, never
`queued`.

## Context
Root-causing the TASK-409 AWS-cutover incident (GH-4392, 2026-07-17): the
pre-cutover daemon was killed while 5 tasks sat `queued` with a held
generation-0 claim. The new daemon logged 21 claim-lost drops and "stale
recovery … reset 0 tasks" for ~40 minutes until a human manually surgery'd
the rows to `stalled`.

## Details
`nextRetryGeneration`'s three-way branch (dead-and-done / dead-and-not-done /
live) is correct *given* a claimed execution can only be "live" while its
owning process is alive. That invariant silently breaks across a process
restart: nothing automatically transitions a dead process's in-flight rows
to a terminal status, so the next process's dispatch logic sees the same
non-terminal claim and treats it as a legitimate, currently-running
duplicate-pickup guard (`ErrClaimLost`) rather than what it actually is — an
orphan.

## Recommended Approach
On daemon boot, **before any worker exists for the new process** (first line
of `Dispatcher.Start`, ahead of `adoptQueuedProjects`/GH-3732), fetch every
execution that is both non-terminal (`queued`/`running`) AND holds an
`execution_claims` row (`GetClaimedNonTerminalExecutions` — inner-join,
no time filter) and transition each to `stalled` via `ExecutionLifecycle`
before anything else runs. Under the single-daemon invariant (H7/#4311) this
set is *exactly* "left behind by a prior, now-dead process" at that instant
— no threshold or ownership stamp is needed. Mirror the existing
`recoverStaleRunningTasks` guards (decomposed-parent, `HasCompletedExecution`,
GH-4092 merged-PR heal) so a boot orphan whose real work already shipped
heals/deletes instead of getting marked `stalled` needlessly.

**Scope strictly to claimed rows** (the JOIN), not every non-terminal row —
GH-3732's restart-adoption fixtures (and its regression tests,
`TestDispatcher_AdoptQueuedProjectsOnRestart` /
`TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap`) rely on bare,
unclaimed `queued` rows surviving `Start()` untouched so they can be
re-adopted and FIFO-drained normally. A blanket "all non-terminal rows are
orphans" reconciliation silently breaks that continuity path.

## Related
- `internal/executor/dispatcher.go` — `reconcileOrphanedExecutions`,
  `nextRetryGeneration`, `beginWithGenerationRetry`
- `internal/memory/store.go` — `GetClaimedNonTerminalExecutions`
- Pattern: `.agent/knowledge/memories/patterns/timestamp-hardening-parse-then-compare-in-go.md`
- Prior art: #4373 (`nextRetryGeneration`), #4345/GH-4332 (timestamp formats), H7/#4311 (single-daemon invariant), #4393 (the sibling split-brain incident from the same cutover)

---
**Captured**: 2026-07-17
**Confidence**: 90%
**Concepts**: executor, dispatcher, execution-claims, daemon-restart, orphan-reconciliation, TASK-409
