# Pitfall: Autopilot CI-fix continuation rebuilds the branch from main when the failed PR branch was deleted — original commits dropped, original issue falsely superseded

## Correction (nav-research 2026-09-07)
Branch deletion was NOT the decisive step. The executor's `CreateWorktreeWithBranch` is called with an empty base (runner.go ~2760) and runs `git worktree add -B <branch> … origin/main` (worktree.go ~537-573): the fix branch is force-reset to main by NAME, so the original commits are discarded even when the remote branch still exists. The recorded `SHA:` in the fix body is prose that nothing parses. Second defect: the controller reads its own `handleCIFailed` close as an external close three polls later → `notifyExternalClose` labels the original `pilot-superseded` and deletes the branch. Shared cause across this week's incidents: continuation state as prose, git state re-derived by name, terminal labels on process events instead of delivery evidence. Fix = pilot#5348 (rewritten with this analysis).

## Summary
pilot-console #275 → PR#276 failed on an unrelated flake; autopilot closed the PR (branch deleted) and spawned fix #277 recording SHA 597710b. The fix run created worktree branch=pilot/GH-275 fresh from main, fixed only the flake (PR#278 merged), #277 done, #275 labelled pilot-superseded — with zero lines of the original change on main. TASK-460 false-success class; caught only by post-merge review. Filed as a pilot bug 2026-09-07.

## Context
Post-merge review of PR#278 on 2026-09-07 compared origin/main against #275's spec: every criterion FAIL; daemon.log 09:21:33Z shows the worktree created by branch name with no SHA.

## Details
Signature: a fix PR whose diff shares no files with the original PR, followed by the original issue getting pilot-superseded. Never accept a 'superseded' close from the fix flow without checking file-set overlap. Recovery: the original commit usually still exists as a dangling object — fetch by sha and cherry-pick.

## Recommended Approach
When triaging a closed-on-CI PR: (1) note the head sha before anything else; (2) after the fix PR merges, diff its file set against the original PR's; (3) if disjoint, reopen the original with a recovery note pointing at the sha. Operator rule: never close an original as superseded yourself on the strength of a fix issue existing.

## Related
- TASK-495
- `internal/autopilot/feedback_loop.go`
- `internal/executor/worktree.go`

---
**Captured**: 2026-09-07
**Confidence**: 95%
**Concepts**: autopilot, feedback-loop, false-success, worktree, review
