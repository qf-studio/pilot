# Learning: Navigator PR review = execute the reviewer's Not-verified list + one caller hop; a diff re-read duplicates the Opus reviewer

## Summary
On PR#6003 (2026-11-01) the Opus 5.5 pre-PR shadow and post-merge reviewers found 3 of the 4 Navigator review notes plus 2 the human skipped; the re-read of the diff added nothing. The only non-duplicated value was execution (go test -race + the two spec mutations; the reviewer checkout is read-only and cannot run tests) and one extra caller hop (Result.RunnerMissing -> outcomeRunnerMissing -> the stale exit-127 headline at internal/executor/runner.go:6574, a file outside the diff). Minor defects have no sink: only REQUEST-CHANGES files an issue (post_merge_review.go:330), so minors were hand-filed as #6005.

## Context
TASK-628 / PR#6003 review, 2026-11-01. Founder question: why review again if the reviewer found it; why did the reviewer not file tickets.

## Details
Pre-PR shadow verdict (in the PR body) listed: -f/-C value word taken as target, wrong Makefile under -C, multi-target line reads absent, whole-output scan. Post-merge verdict added: no test on the new headline, evidence gap (no pasted go test output, mutations not run), BSD needed-by gap under Not-verified. Navigator notes: the same four plus the executor headline at runner.go:6574, which the reviewer missed because caller tracing stopped at executor/runner.go:9444/9464 (direct RunnerMissing() consumers) and did not follow outcomeRunnerMissing (9596) to its caller (6574). The reviewer states it cannot run tests in its checkout; the two spec mutations were run by hand and both killed their tests. A COMMENTED or APPROVED review on the PR is never read by the daemon (controller.go:3112-3140 acts on CHANGES_REQUESTED from a trusted human only); autopilot merged on green 2 minutes after the Navigator comment.

## Recommended Approach
Before a Navigator review: read the pre-PR verdict in the PR body and the post-merge comment. Then (1) run every Not-verified item (tests, mutations) in a detached review worktree, (2) trace each new bool helper one caller hop past the direct consumers the reviewer cited, (3) do not re-read the diff for defects the reviewer already lists. Minor defects worth fixing: file one pilot+no-decompose fast-follow issue per PR, since nothing else sinks them. Record the verdict line in the task doc; posting it on the PR is a record only, not a pipeline input. Automation gaps for TASK-620: reviewer test execution on its checkout; caller tracing one hop further.

## Related
- TASK-620
- TASK-628
- TASK-612
- TASK-619
- `internal/autopilot/post_merge_review.go`
- `internal/autopilot/controller.go`
- `internal/executor/post_merge_reviewer.go`

---
**Captured**: 2026-11-01
**Confidence**: 90%
**Concepts**: review, workflow-discipline, quality-gate
