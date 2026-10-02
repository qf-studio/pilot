# TASK-508: Post-merge CI fix issues borrow the merged PR branch and short-circuit as delivered

**Status**: 🚀 DISPATCHED 2026-10-01 → [#5564](https://github.com/qf-studio/pilot/issues/5564) (`pilot`, `no-decompose`, `bug`). One PR, three changes (A emitter, B dispatcher safety net, C host-checker reason). **Post-merge review of PR#5569 DONE 2026-10-02: APPROVE-w-notes** (legs A/B/C verified on main; notes: two other merged-PR short-circuits in dispatcher.go unguarded for a borrowed origin · close/label failures only logged · supersede path GitHub-only · borrow is in-memory, lost on restart).
**Created**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes · **Related**: TASK-460 (false-success class), TASK-507 (Jev gate 3 reads post-merge failures)

## Why
Six `fix(ci): resolve post-merge CI failure from PR #N` issues in 60 days; one real fix PR. #5551 (10-01) left open and polled every 5.5 min forever; #5560 (10-01) and #5385 (09-08) closed `pilot-done` with the ORIGINAL merged PR as "delivery" ("Time to merge 10s"). Claude never ran on any of them. RCA 2026-10-01 via nav-research + box daemon.log + GitHub events; pitfalls `ci-post-merge-fix-issue-borrows-merged-branch-short-circuits`, `completed-row-without-live-execution-polled-forever-as-stalled`.

## Root cause chain
1. `generateBody` emits `branch:/pr:/sha:` in the autopilot-meta footer for every failure type, incl. `ci_post_merge` (PR merged, branch deleted).
2. `resolveAutopilotFixBranch` keys on `branch:` only → borrows the merged branch, `RecordBorrowedBranch(fromPR=original)`.
3. Dispatcher merged-PR short-circuit → `MarkExecutionCompleted(originalPRURL)` directly, bypassing lifecycle; `buildTaskFromExecution` never sets `FromPR` so the GH-5400 `isBorrowedOriginPR` rule cannot protect it.
4a. Startup cleaner strips `pilot-in-progress`; poller re-admits; `HasCompletedExecutionReason` falls through all re-arm probes to `recordClaimLostDrop` + "stalled: awaiting re-arm evidence" (variant A, #5551).
4b. `registerPR` binds the issue to the merged PR; `checkExternalMergeOrClose` closes it `pilot-done` with no ownership check (variant B, #5560/#5385).

## Fix (in #5564)
- **A** post-merge footer = `iteration:N source:M` only (`iteration:`/`source:` regexes are independent of `branch:`); pre-merge + review footers byte-identical.
- **B** short-circuit: if `BorrowedBranch(taskID).fromPR == extractPRNumberFromURL(mergedURL)` → `Finish(ExecStatusSuperseded)`, empty pr_url, comment + `pilot-superseded` + close; no `recordExternalMerge`. Known limit: in-memory borrow registry, silent after restart (A removes the source).
- **C** genuine completed/no_op row → `"completed execution exists"`, no claim-lost drop.

## Same-day review pass (10-01 pm)
PR#5558/#5559/#5561/#5562/#5563 all reviewed APPROVE-w-notes with verdict comments; follow-ups filed: #5565 (gate-3 line at DEBUG), #5566 (redaction slash leak), #5567 (test pins), #5568 (evidence capture).

## Follow-ups (not in #5564)
- Flake guard: re-run the failed job once before filing a post-merge fix issue; decline as flake if green (extend `maybeRetryPostMergeInfraFailure` to code-class). Unfiled.
- Ownership check in `checkExternalMergeOrClose`: refuse a PR that merged before the issue was created (needs one `GetIssue` for `CreatedAt`). Unfiled.
- Dedup against human-filed issues for the same failing test (`findExistingFixIssue` is exact-title only). Unfiled.
- Operator: close #5551 by hand.

## Refs
- `.agent/knowledge/memories/pitfalls/ci-post-merge-fix-issue-borrows-merged-branch-short-circuits.md`
- `.agent/knowledge/memories/pitfalls/completed-row-without-live-execution-polled-forever-as-stalled.md`
- Seams: `internal/autopilot/feedback_loop.go`, `cmd/pilot/handlers.go`, `internal/executor/dispatcher.go`, `cmd/pilot/main.go`
