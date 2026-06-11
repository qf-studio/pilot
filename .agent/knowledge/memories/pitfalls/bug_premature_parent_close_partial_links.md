---
name: Premature parent close — open-count from partial native sub-issue links
description: maybeCloseParentIssue trusted GetOpenSubIssueCount alone; partial LinkSubIssue coverage makes native open-count hit 0 while unlinked siblings are open, closing the parent pilot-done with zero PR-merge verification.
type: pitfall
originTask: TASK-361
---

`LinkSubIssue` is **non-fatal** at sub-issue creation (`internal/executor/epic.go`
~:1306, warn-and-continue). When it fails for some children, the parent's native
GitHub sub-issue link set covers only a **subset** of its real children.

`maybeCloseParentIssue` (`internal/autopilot/controller.go` ~:1636) keyed the
"all children done" decision on `GetOpenSubIssueCount`: `hasNativeLinks=true`
(totalCount > 0) made the native count authoritative and **skipped the text-search
fallback**. With only the merged child linked, open-count = 0 → `closeParentNow`
→ parent closed + `pilot-done`, even though unlinked siblings were open with
unmerged PRs. `recoverStaleParentIssues` (~:1702) had the same flaw.

**Nothing in the close path verified a PR ever merged** — completion was an
issue-state count, labels were proof of nothing.

**Incident (GH-3513, 2026-06-09):** parent epic closed `pilot-done` after one
13-line fragment PR (#3520) merged; the three real children were later
superseded; the feature never reached `main` while every issue read done.

**Fix (PR #3527):** shared `openSubIssueCount()` helper — a native count of 0
is never trusted alone; it must be confirmed by the `"Parent: GH-N"` text
search; max of both tiers wins. Regression test:
`TestRecoverStaleParentIssues/"native 0 vetoed by text search..."`.

**Rule:** any "all children complete" predicate must cross-check BOTH link
tiers, and ideally verify merged PRs, before closing a parent.

Related: [[bug_false_supersession_label_trust]], [[bug_inherited_spec_full_reimplement]]
