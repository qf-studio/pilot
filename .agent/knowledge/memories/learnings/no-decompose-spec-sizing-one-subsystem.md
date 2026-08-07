---
name: no-decompose-spec-sizing-one-subsystem
description: A no-decompose issue must fit one subsystem + its tests — #4780 timed out twice at 1h as a single unit; the same content split as #4791/#4792 dispatched cleanly
type: learning
---

# no-decompose spec sizing: one subsystem + its tests per issue

**What happened (2026-08-07):** #4780 (platform-outage circuit breaker) was
authored as a single `no-decompose` issue covering cross-PR failure
correlation, destructive-action suppression, a platform status probe,
admission pause, and held-PR re-drive. The executor timed out at the 1h
ceiling twice — the spec was implementable but not landable in one
execution window. Split into #4791 (correlation + suppression) and #4792
(status probe + admission pause + re-drive, gated on #4791), each a clean
single-subsystem unit, dispatch proceeded normally.

**The sizing rule:** a `no-decompose` issue gets exactly one execution
window — its scope must be one subsystem plus its tests. If the spec's
task list spans more than one subsystem, or has two independent "goals",
pre-split it into chained issues (later legs reference earlier ones and
are labeled only after the earlier PR merges) instead of shipping one big
spec. TASK-459's phase legs are sized by this rule.

**Open question (deliberately undecided):** whether to always split
proactively or let a first timeout be the signal. Two timeouts on #4780
cost ~2h wall-clock and two executor runs; a proactive split costs
authoring time on specs that might have fit. Current bias: split when the
task list names >1 subsystem; otherwise dispatch and watch the first run.

Related: [[unwired-config-field-validated-but-dead]] shipped as a clean
single-subsystem no-decompose leg the same day (#4784 → PR#4793).
