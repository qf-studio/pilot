---
name: claim-lost-drops-count-toward-hard-cap
description: Duplicate poller re-picks generate claim-lost drops that count toward the repick hard cap — a QUEUED execution that never ran can be terminally stalled by its own duplicates (#4455 excluded restart churn and operator cancels, but not claim-lost)
type: pitfall
---

# Claim-lost drops count toward the hard cap — queued executions get murdered by their own duplicates

**What happened (2026-07-19→20, GH-4469):** the task dispatched cleanly at
19:42Z and **queued** behind GH-4391 (position 1). Five minutes later a
status-label refresh let the poller re-dispatch a duplicate; the dispatcher
correctly dropped it ("dispatch claim lost — task already owned"), but that
drop — and every subsequent one, every ~16 min all night ("repick storm"
WARN, consecutive_drops 6→55) — **counted toward the repick hard cap**. At
cap the still-queued, never-executed row 3948 was marked terminally stalled
("54 consecutive failed re-picks (cap=5)"). The fix ticket for this exact
loop class was killed by the loop class.

## Why it matters

- A healthy queued task can be terminally stalled with zero executions —
  the cap is meant to stop failing tasks, not queued ones.
- #4455 excluded restart churn + operator cancels from the cap; claim-lost
  drops are the remaining uncounted-churn hole.
- The `repick storm` WARN fired 50+ times with no escalation — silent for
  14 hours.

## Fix direction

Owned by GH-4469/TASK-413 (addendum posted on the issue): exclude
claim-lost/duplicate-pickup drops from the cap counter; gate the poller
before dispatch; escalate repick-storm WARNs into the loop-breaker alert.

Related: [[hard-cap-rearm-in-memory-gate]] (manual re-arm recipe when this
bites), TASK-407 #4372 (gen-0 re-claim).
