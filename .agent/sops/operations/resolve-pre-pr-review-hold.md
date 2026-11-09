# SOP: Resolve a Pre-PR Review Hold

**Category:** operations
**Created:** 2026-11-06
**Trigger:** A run ended `needs_human` with the reason "pre-PR review: blocking defects not fixed" (`executor.pre_pr_review.on_unfixed: hold`).

## What happened

The pre-PR reviewer found at least one blocking defect, the fix round did not clear it (fix reverted, re-gate failed or timed out, fix failed, or no fix round ran), and the run parked instead of opening a PR. The branch is pushed; no PR exists. The issue carries the label `pilot-needs-human` and a comment with the defects, the evidence block, the branch name and the HEAD SHA. The run is terminal: it counts in `needs_human` in the lifetime and windowed task counts, not in failed, and it is not billed (no completed status, no PR).

## Steps

1. Open the issue and read the hold comment: the blocking defects (location, claim, evidence), the branch, the HEAD SHA, the fix outcome.
2. Decide which of the three paths fits:
   a. The defects are right and the branch is close: check out the pushed branch in your own worktree, fix it by hand, push, then remove `pilot-needs-human` from the issue. The re-arm runs the task again on the existing branch (`cmd/pilot/rearm_needs_human.go`: the label is gone and the trigger label `pilot` is still present).
   b. The defects are right because the issue is vague or wrong: edit the issue body, then remove `pilot-needs-human`. The re-arm runs the task again with the new body.
   c. The task is no longer wanted, or the review is wrong and you will take the branch as it is: close the issue. To ship the branch as it is, open the PR yourself from the pushed branch.
3. Do not remove `pilot-needs-human` before the edit or the push lands. The re-arm fires on the label removal and picks the issue up at once.

## What the Run Story card shows

`GET /api/v1/executions/{id}` and `GET /api/v1/tasks/{taskId}` return `run_story.outcome = "needs_human"` with:

- `hold.reason`: the hold reason text
- `hold.branch`: the pushed branch
- `hold.commit_sha`: the pushed HEAD
- `next_step`: "Edit the issue or the branch, then remove pilot-needs-human"

A hold has no `failure_class` and no PR fields. The console card for it is a separate pilot-console-ui leg.

## Where it shows in metrics

- `pilot_pre_pr_holds_total{project}` (Prometheus) counts holds per project. It resets on restart.
- `needsHumanTasks` (lifetime) and `window.needsHuman` on `/api/v1/metrics`, and the "Needs human" line of `pilot metrics summary`, carry the same rows. They stay out of the attempt success rate (completed / (completed + failed)).

## What shows nothing

`pilot billing-mode status` and `pilot status` say nothing about a hold. A hold is not a billing event and not a daemon fault. Look at the issue label `pilot-needs-human`, the Run Story card or the counters above.

## Related

- `.agent/tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md` § Rollout and exit criteria (stage 3 reads the hold count)
- `.agent/tasks/archive/TASK-641-pre-pr-review-unfixed-hold.md`
