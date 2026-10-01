# Pitfall: ci_post_merge fix issues borrow the already-merged PR branch, so the dispatcher short-circuits them as delivered

## Summary
A post-merge CI failure spawns a fix issue whose autopilot-meta footer carries the ORIGINAL PR's branch and number (feedback_loop.go emits branch:/pr: for every failure type). resolveAutopilotFixBranch reuses it unconditionally, the dispatcher's mergedPRPreflightCheck finds that PR merged and marks the execution completed with the original PR URL (0 ms, 0 tokens, Claude never runs). Two outcomes, both wrong: (A) the issue is never closed or labeled and the poller spins on it (#5551); (B) registerPR binds the issue to the merged PR and checkExternalMergeOrClose closes it pilot-done with 'PR merged successfully, time to merge 10s' — a false success (#5560, #5385). Only #5386 ever produced a real fix PR.

## Context
2026-10-01: timing flake in TestController_ProcessAllPRs_EvictsAfterRepeatedNotFound failed post-merge CI on main after PR #5549; autopilot filed #5551 (08:23Z), dispatcher short-circuited at 08:24:13 (box log: 'Queued task's branch already merged; skipping backend invocation' pr_url=/pull/5549). Human-filed #5554 -> PR #5559 fixed the flake. Same day #5560 (from PR #5558) waited 49 min in the serial queue, short-circuited at 11:33:58 and was closed pilot-done 7 s later. Traced by nav-research against main + box daemon.log + GitHub events.

## Details
Chain: handlePostMergeCI classifies the failure as code (flake handling is infra-only, maybeRetryPostMergeInfraFailure) -> spawnFailureIssue with dedup key fix:pr<N>:<type>:<checks> (blind to human-filed issues for the same test) -> generateBody footer includes branch:/pr: regardless of failure type -> resolveAutopilotFixBranch (cmd/pilot/handlers.go) borrows pilot/GH-<orig> -> dispatcher.go merged-PR short-circuit calls store.MarkExecutionCompleted directly, bypassing ExecutionLifecycle.Persist and every GitHub side effect -> HasTerminalCompletion is now true (pr_url set). Variant A vs B depends on whether the original PR's StageFailed PRState is still in activePRs when OnPRCreated fires (registerPR dedups silently at Debug). Variant B's close path has no ownership check: ghPR.Merged alone yields pilot-done + close + release/scope decision.

## Recommended Approach
Fix at the source: for ci_post_merge, omit branch:/pr: from autopilotMetaFields (or ignore them in resolveAutopilotFixBranch) so the fix branches from main as pilot/GH-<fix>. Second: re-run the failed job once before filing and decline as flake if green (extend maybeRetryPostMergeInfraFailure to code-class). Third: the merged-PR short-circuit must not report a delivery for a task that has FromPR set — mark no_op/superseded and comment+close the issue. Fourth: checkExternalMergeOrClose must refuse a PR that merged before the issue was created or whose branch belongs to another issue. Until shipped: close any open 'resolve post-merge CI failure' issue by hand and treat 'Time to merge <1 min' on such an issue as a false success.

## Related
- TASK-460
- `internal/autopilot/feedback_loop.go`
- `internal/autopilot/controller.go`
- `cmd/pilot/handlers.go`
- `internal/executor/dispatcher.go`
- `internal/executor/lifecycle.go`

---
**Captured**: 2026-10-01
**Confidence**: 85%
**Concepts**: autopilot, autopilot-fix, ci_post_merge, false-success, dispatcher
