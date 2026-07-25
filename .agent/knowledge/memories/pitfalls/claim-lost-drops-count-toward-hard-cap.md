---
name: claim-lost-drops-count-toward-hard-cap
description: RESOLVED 2026-07-25 by #4541 (TASK-421). Duplicate poller re-picks generated claim-lost drops that counted toward the repick hard cap — a QUEUED execution that never ran could be terminally stalled by its own duplicates. Recurred three ways on 07-24 before the fix
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

---

## RESOLVED 2026-07-25 — #4541 / TASK-421

Before the fix this class recurred **three distinct ways in 24 hours**, all on
the same counter:

| Task | Drops accrued while… | Reality |
|---|---|---|
| GH-4526 | environment failures (hosted `git_clean` deadlock, CI infra outage) | task fine, box broken |
| GH-4531 | legitimately **running** (poller raced the epic it was executing) | live execution at `consecutive_drops=5` |
| GH-4537 | legitimately **queued** behind GH-4536 | never started; blocked ~10h overnight |

GH-4537 generalised the rule: **any task queued behind a task running longer
than ~37 min was auto-blocked for waiting its turn** — under the project's own
default concurrency of 1, where queueing is normal.

Root cause had two halves: `Dispatcher.IsActive` (`dispatcher.go:797`) existed
to answer "already queued or running?" but was called from only
`cmd/pilot/handler_common.go:102`, never the GitHub poller path — so a
guaranteed-to-be-rejected dispatch was generated every cycle; and that
rejection then grew the same `consecutive_drops` that gates the cap, though
nothing was attempted.

Fixed following the `stall_drops` precedent (`store.go:419-426`, added by
#4455) rather than a third pattern. Verified live on the first dispatch after
the v2.245.2-13-gbad74a7e restart:

```
dispatch re-pick: prior claim was stall-killed — claiming next generation
without counting toward repick hard cap   consecutive_stall_drops=1
```

Shipped in **v2.246.0**.
