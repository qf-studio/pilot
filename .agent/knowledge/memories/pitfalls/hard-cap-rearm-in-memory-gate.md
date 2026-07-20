---
name: hard-cap-rearm-in-memory-gate
description: Re-arming a hard-cap-stalled task needs FOUR clears — backoff row, stale gen-0 claim (#4372), the stalled executions row itself, AND waiting out the daemon's in-memory next_eligible gate; DB deletes alone do not dispatch
type: pitfall
---

# Hard-cap re-arm: DB surgery alone is not enough — in-memory gate survives

**What happened (2026-07-18, GH-69/pointer):** GH-69 was falsely stalled at
hard cap 5/5 (#4454 class — claim-lost drops accrued while it was legitimately
RUNNING; the only real failure was one infra SIGKILL). The documented one-line
re-arm (`DELETE FROM repick_backoff`) did nothing: the poller kept logging
"Dispatching issue" → "Execution failed without PR, unmarking for retry" every
~40s with no claim attempt, no execution row.

## Complete recipe (proven 11:24Z)

1. **Note the backoff row's `next_eligible` BEFORE deleting it** — the daemon
   holds the same value in an in-memory map that the DB delete does not clear.
2. `DELETE FROM repick_backoff WHERE key='<macOS-era projectPath>|<taskID>'`
3. `DELETE FROM execution_claims WHERE task_id='<id>'` — stale gen-0 claim
   blocks every re-claim (#4372: poller retry path re-claims generation 0).
4. `UPDATE executions SET status='cancelled', error=error||' | [operator …]',
   completed_at=CURRENT_TIMESTAMP` on the stalled row — a stalled row itself
   gates dispatch ("stalled is terminal, no further automatic retries").
5. **Wait for the noted `next_eligible` to pass.** GH-69 dispatched at
   literally 11:24:26Z — the exact expiry second. Nothing you do in the DB
   accelerates this; only a daemon restart clears the map early.

## Diagnostic signature
Dispatch attempts that end at "unmarking for retry" with NO subsequent
"dispatch claim lost" line = dropped before the claim stage = in-memory gate,
not DB state.

## Status (updated 2026-07-20)
#4455 narrowed the false-stall class (restart churn + operator cancels no
longer count) but did NOT close it: **claim-lost drops still count** — see
[[claim-lost-drops-count-toward-hard-cap]] (GH-4469 killed while queued,
v2.242.0 binary). This recipe remains the live workaround until
GH-4469/TASK-413 ships.

**In-memory gate refinement (observed on v2.242.0):** the DB-only 3-step
surgery dispatches immediately **only when the backoff drops predate the
current daemon process** (GH-4391, 2026-07-19: backoff loaded from a prior
boot, delete worked instantly). If drops accrued during the current process,
the in-memory `next_eligible` survives the DB delete — wait it out (~16 min
storm cadence; GH-4469 dispatched at 09:45Z right on schedule) or restart.

Related: [[slack-approval-socket-mode-unroutable]] (same session family),
TASK-407 #4372 note.
