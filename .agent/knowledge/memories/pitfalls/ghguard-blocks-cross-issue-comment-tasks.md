# Pitfall: task specs asking Pilot to "reply to issue #N" are blocked when #N isn't the task's own issue

## Summary
GH-5330's spec said "Reply to issue #5228 explaining the feature was declined...". At execution time, `gh issue comment 5228 ...` was denied by the `gh-guard` shim (GH-4671, `internal/executor/ghguard/policy.go` `checkOwnArtifact`): `kindOwnArtifact` only allows `gh issue comment`/`gh pr comment` when the target number equals `PILOT_TASK_ISSUE` (the issue the execution was dispatched for). There is no allowlist mechanism for "issues referenced in the task body" — this is intentional (GH-4649 was exactly an executor mutating a sibling issue mid-run) and fails closed with no override.

## Root cause
Task authoring pattern "reply to the original external issue #N" is natural when a task is a sub-issue of a decomposed parent (e.g. GH-5330 under GH-5314, itself answering external report #5228), but the identity Pilot's executor runs under is scoped to exactly one issue number per dispatch. Any `gh issue comment`/`gh pr comment` target other than that number is denied regardless of how clearly the task body asks for it.

## Resolution used
Posted the full reply as a comment on the task's own issue (GH-5330) instead, with a note explaining the target issue and why. A human/Navigator session (not gh-guard-restricted) can then cross-post or link it into #5228 if a comment there is still wanted.

## Prevention
When authoring a Pilot task whose deliverable includes "reply to / comment on issue #N", either:
- Make #N the task's own dispatch issue (i.e. dispatch the actual external issue directly, not a child task), or
- Expect the reply to land on the task's own issue instead, and have a human relay it to #N afterward — document this explicitly in the task spec so the outcome isn't a surprise.
Do not ask a Pilot-executor task to comment on an issue other than its own; gh-guard denies it unconditionally and there is no config to permit it.

---
**Captured**: 2026-09-06
**Confidence**: 0.97
**Concepts**: ghguard, autopilot, task-authoring, gh-guard, review-triggers, GH-5330, GH-4671
