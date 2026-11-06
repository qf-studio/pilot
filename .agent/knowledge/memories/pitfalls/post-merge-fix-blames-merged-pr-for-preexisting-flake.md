---
name: post-merge-fix-blames-merged-pr-for-preexisting-flake
description: Autopilot's post-merge CI fix flow attributes any red on a merge commit to the merged PR: GH-5784 'resolve post-merge CI failure from PR #5782' was a pre-existing race in runEpicParentPass (TestEpicParentPass_DeadlineLogsAndCounts), unrelated to #5782's log-level diff. The fix (PR#5786) was real but titled fix(ci) while changing production controller.go. A blind rerun-and-decline flake guard would have hidden it.
type: pitfall
---

# Post-merge fix flow blames the merged PR for a pre-existing flake

**Case (2026-10-20):** PR#5782 (log fields + levels only) merged; main `test` went red in `TestEpicParentPass_DeadlineLogsAndCounts` (no deadline WARN logged). Autopilot filed #5784 "resolve post-merge CI failure from PR #5782" (`ci_post_merge`) → PR#5786 merged 19:03 CEST: `sync.OnceFunc` recordTimeout called from both the AfterFunc watcher and the pass-exit defer — a real race where a ctx-honoring reconciler returned before the watcher goroutine started. Post-merge review APPROVE-w-notes; the new 50-pass test catches a revert only under `-race -cpu=2` (2/10), not with defaults (0/20).

## Rules
- When reviewing a `fix(ci): resolve post-merge CI failure from PR #N`, first check whether the failing test covers PR #N's diff. If not, it is a pre-existing flake exposed by timing — review the fix as a production change.
- For the TASK-508 flake-guard follow-up: classify "failing test outside the merged diff" as its own issue type; do not decline it as flake and do not attribute it to the PR.
- Concurrency regression tests that pass 0/20 locally may still pin the bug under CI scheduling; try `-race -cpu=2` before calling a mutation survivor.

## Update 2026-11-06
- The attribution gap had a second root cause: `handlePostMergeCI` pinned the main head instead of the PR's merge commit (mem-290). Fixed by #6125 → PR#6132 (merge-commit SHA, one code-class re-run before a fix issue, dedup by run permalink) and #6137 → PR#6140 (the re-run restarts the CI timer). The "failing test outside the merged diff" classification above is still unfiled.
