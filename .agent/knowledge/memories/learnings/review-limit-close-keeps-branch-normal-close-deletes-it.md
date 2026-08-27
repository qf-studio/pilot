---
name: review-limit-close-keeps-branch-normal-close-deletes-it
description: In handleReviewRequested the iteration-limit close (~4046) never deletes the branch, while the normal healthy-continuation close (~4128) unconditionally deletes it (~4133) — recovery is EASIER after hitting the cap than after a routine revision hand-off, the reverse of intuition
type: learning
---

# Branch-deletion asymmetry on the review-feedback close paths

**Found during GH-5227 verification (2026-08-27).** The external reporter
assumed "the branch survives, the PR can be reopened" for iteration-limit
closes. Backwards for the review path:

- **Limit-reached close** (`controller.go:4046`, `iteration >= MaxIterations`):
  no `safeDeleteBranch` call — branch intact, PR reopenable.
- **Normal revision-cycle close** (`controller.go:4128`, after
  `spawnReviewIssue` succeeds): `safeDeleteBranch` at ~4133 unconditionally
  deletes the branch. The revision issue owns the continuation; the old PR is
  NOT recoverable by reopening (no branch).

`handleCIFailed`'s three close sites (~3592, ~3683, ~3749) call
`safeDeleteBranch` at none of them.

**Why it matters operationally:** when triaging a closed-unmerged autopilot
PR, the recovery playbook depends on which path closed it — check for a
sibling revision issue (`<!-- autopilot-meta ... iteration:N -->` marker in
the issue body) before attempting reopen. If a revision issue exists, the
work continues there and the branch is likely gone; if none exists, it was a
limit/terminal close and reopen-with-branch works. Also the correct rebuttal
detail when correcting external reports (posted on GH-5227).

Related: [[bug_pilot_ghost_closes]] (close-variant taxonomy),
[[close-rationale-cites-mode-the-component-cannot-see]], TASK-486.
