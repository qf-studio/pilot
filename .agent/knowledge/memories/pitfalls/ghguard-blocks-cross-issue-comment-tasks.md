# Pitfall: task specs asking Pilot to "reply to issue #N" are blocked when #N isn't the task's own issue

## Summary
GH-5330's spec said "Reply to issue #5228 explaining the feature was declined...". At execution time, `gh issue comment 5228 ...` was denied by the `gh-guard` shim (GH-4671, `internal/executor/ghguard/policy.go` `checkOwnArtifact`): `kindOwnArtifact` only allows `gh issue comment`/`gh pr comment` when the target number equals `PILOT_TASK_ISSUE` (the issue the execution was dispatched for). There is no allowlist mechanism for "issues referenced in the task body" — this is intentional (GH-4649 was exactly an executor mutating a sibling issue mid-run) and fails closed with no override.

## Root cause
Task authoring pattern "reply to the original external issue #N" is natural when a task is a sub-issue of a decomposed parent (e.g. GH-5330 under GH-5314, itself answering external report #5228), but the identity Pilot's executor runs under is scoped to exactly one issue number per dispatch. Any `gh issue comment`/`gh pr comment` target other than that number is denied regardless of how clearly the task body asks for it.

## Resolution used
First attempt posted the full reply as a comment on the task's own issue (GH-5330) instead, with a note explaining the target issue and why. An intent-judge retry correctly rejected that as not satisfying "reply to #5228" — a comment on the wrong issue isn't the deliverable, regardless of how well it's explained.

On retry, re-checking #5228's *live* comments (not just the repo diff) found the founder/operator had already posted the exact required reply directly on #5228 — decline rationale, both bugs credited, tracking issue linked — timestamped *before* this task's first dispatch even started. Re-attempting `gh issue comment 5228` (still correctly denied by gh-guard, journaled, feeding the `AlertEventTypeGhGuardDenied` operator alert) plus citing the existing comment URLs on #5228 as evidence was what actually closed the gap: the requirement was already met externally, and the fix was updating the task doc to prove that with links rather than re-asserting the same blocked workaround.

## Prevention
When authoring a Pilot task whose deliverable includes "reply to / comment on issue #N":
- Make #N the task's own dispatch issue (i.e. dispatch the actual external issue directly, not a child task), or
- Expect the reply to land on the task's own issue instead, and have a human relay it to #N afterward — document this explicitly in the task spec so the outcome isn't a surprise, or
- **Before treating this as blocked, check whether a human has already replied on #N directly** (common when a task is a verification/audit leaf of a larger decomposed issue whose parent a human is actively triaging) — grep the *live* issue's comments via `gh issue view N --json comments`, not just the repo's git diff, since a satisfied external-reply requirement leaves no trace in the branch.
Do not ask a Pilot-executor task to comment on an issue other than its own; gh-guard denies it unconditionally and there is no config to permit it. But don't stop at "denied, therefore undeliverable" either — verify live GitHub state before concluding the requirement can't be met.

---
**Captured**: 2026-09-06
**Confidence**: 0.97
**Concepts**: ghguard, autopilot, task-authoring, gh-guard, review-triggers, GH-5330, GH-4671
