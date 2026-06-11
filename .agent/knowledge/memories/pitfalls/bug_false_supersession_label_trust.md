---
name: False supersession — pilot-done label trusted as proof of shipped work
description: skipSupersededByParent auto-closed live children as "parent already shipped this work" based only on parent.State==closed && HasLabel(pilot-done) — no PR-merge verification. Orphaned complete green PRs.
type: pitfall
originTask: TASK-361
---

`skipSupersededByParent` (`internal/adapters/github/poller.go` ~:2008) decided
"this sub-issue is redundant" from the parent's **labels alone**:

```go
if parent.State != StateClosed || !HasLabel(parent, LabelDone) { return false }
// → supersede: comment "already shipped this work", label, close
```

A `pilot-done` label proves only that *something* closed the parent issue —
not that the work merged. When the parent close is premature (see
[[bug_premature_parent_close_partial_links]]), this path **discards the live
implementations**: in GH-3513 it auto-closed children whose CI-green,
MERGEABLE PRs (#3519, #3523) contained the entire feature, with the comment
"parent epic #3513 already shipped this work."

**Fix (PR #3527):** before superseding, check `FindOpenPRByBranch(pilot/GH-N)`
for the child. An open PR vetoes supersession (work is in flight); lookup
errors fail open to normal dispatch. Test:
`TestPoller_CheckForNewIssues_DoesNotSupersedeWithOpenPR`.

**Rule:** labels are claims. Any path that *destroys or abandons* work
(supersede, skip, auto-close) must verify the underlying artifact (merged PR,
code on main) — never a label — before acting.

Related: [[bug_premature_parent_close_partial_links]], [[learn_verify_artifact_not_status]]
