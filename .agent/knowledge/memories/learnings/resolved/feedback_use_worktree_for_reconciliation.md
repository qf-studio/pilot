> **RESOLVED/SUPERSEDED (2026-07-05):** Codified in CLAUDE.md Git & Worktree Discipline + sops/git/reconcile-scattered-uncommitted-work.md

---
name: use-worktree-for-multi-branch-work
description: For reconciliation, branch splits, or any multi-branch work in the Pilot repo, create a fresh git worktree under .claude/worktrees/ — don't operate on a checked-out feature branch in the main repo
type: feedback
---

For reconciliation, branch splits, or any work that touches multiple branches in the Pilot repo, **work in a fresh git worktree under `.claude/worktrees/`** — don't operate from the main repo's checked-out feature branch.

**Why:** The main repo's working tree may be hosting another agent's in-flight work, or be on a feature branch you'd disturb by switching/stashing. The Pilot project uses worktrees as the standard isolation pattern — `git worktree list` typically shows `.claude/worktrees/<name>` worktrees for active Claude sessions, and the executor itself runs in worktrees per `pilot/GH-*` branches.

**How to apply:**
- Before any non-trivial multi-branch session, run `git worktree list`
- Create one with `git worktree add -b <branch-name> .claude/worktrees/<slug> <base-ref>`
- Move work-in-progress into the new worktree (stash + pop, or copy files via patch)
- Leave the main repo's working tree alone
