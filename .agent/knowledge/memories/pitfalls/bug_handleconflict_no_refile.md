---
name: handleMergeConflict does not re-file (mental model trap)
description: handleMergeConflict only closes PR + strips pilot-in-progress. What looks like an autopilot re-file is poller re-dispatch + decomposer re-decompose. Don't audit it as a single mechanism.
type: feedback
originSessionId: 89fe3897-6bc2-4725-a1f2-8635b79860b3
---
`internal/autopilot/controller.go:1688-1729` (`handleMergeConflict`) only does:

1. Try GitHub auto-update (rebase) first
2. On rebase failure: close the PR
3. Remove `pilot-in-progress` label from the source issue
4. Set `prState.Stage = StageFailed`

**It does not create any new issue.** The promise in the close comment ("closing PR so the issue can be re-executed from updated main") is fulfilled by the poller picking the issue back up — NOT by autopilot spawning a sibling.

**Why:** When a sibling-looking issue appears after a conflict-close (e.g., GH-2777 appearing after PR #2767 was conflict-closed), the chain is:

```
PR conflict-closed → handleMergeConflict strips pilot-in-progress
  → poller re-picks original issue (still `pilot` labeled, no `pilot-done`)
    → runner classifies as epic (because labels reset, body unchanged)
      → CreateSubIssues fires → NEW sub-issue with parent: marker
```

The new issue is a fresh decomposition, not a re-file. Different code path entirely (`internal/executor/epic.go` not `internal/autopilot/controller.go`).

**How to apply:** When debugging "why did Pilot create issue X after PR Y closed":
- Don't grep autopilot/ for issue-creation logic — there isn't any (except feedback-loop iteration in `feedback_loop.go`, which uses a different marker format).
- Grep `internal/executor/epic.go` for `CreateSubIssues`, `createSubIssuesViaAdapter`, `createSubIssuesViaGitHub`.
- Trace whether the "re-filed" issue's body has `<!--autopilot-meta\nparent: GH-...\ninherited-spec: true\n-->` (decomposer) or `<!-- autopilot-meta branch:... iteration:N -->` (feedback loop). Different fixes apply.
- Check the original issue's labels — if `pilot-done` is missing and `pilot` is present, the poller will keep re-dispatching it.

**Confirmed wrong assumption:** the navigator-research agent on 2026-05-07 initially reported `handleMergeConflict` as a re-file path. That report was wrong — verified by re-reading the actual code. The correct mental model is above.
