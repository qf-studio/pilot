---
name: Observability hooks must precede skip gates
description: In autopilot scanner/reconciler loops, place metric recorders BEFORE skip gates — gates exist for action deduplication, not observability
type: feedback
originSessionId: b76c9f9a-f417-4bb5-81bf-b10dc8a044a4
---
In `internal/autopilot/controller.go::ScanRecentlyMergedPRs`, TASK-59
(v2.146.2) placed `recordMergeSuccess` after three skip gates:
`activePRs` already-tracked → `releasedCommits` for merge SHA →
`stateStore.GetPRState`. Pilot's self-release pipeline tags every merge
within ~1min, so the next scanner tick always hit the
release-already-exists gate and `continue`d before the recorder fired.
Counters stayed at 0 indefinitely.

**Rule:** metric/observability hooks belong **above** action-dedup
gates in scanner/reconciler loops. Action gates protect against
duplicate side effects (re-triggering a release, double-merging);
metrics need to fire on every observation. Conflating them creates
silent observability gaps that look like the metric is "wired but not
firing."

**Why:** the v2.146.2 PR description and test even claimed correctness
because the unit test set up a fresh state-store row and so was on
the "first discovery" branch — the gate's actual production behavior
(release tag exists almost immediately) was not exercised.

**How to apply:** when reviewing scanner/reconciler PRs, ask: "if the
caller already has a release tag / active row / completed state,
will the metric still fire?" If not, the metric is mispositioned.
Idempotency for metrics should be a per-PR-number in-memory set, not
piggy-backed on action-dedup state.

**Reference:** PR #2985 (v2.146.3) hoisted the recorder above all
three gates and switched idempotency to `Controller.recordedMerges`.
Test `TestController_ScanRecentlyMergedPRs_RecordsMetricsDespiteExistingRelease`
specifically reproduces the bug.
