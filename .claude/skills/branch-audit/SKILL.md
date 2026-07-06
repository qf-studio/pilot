# Branch Audit — safe stale-branch investigation and pruning

---
name: branch-audit
description: Audit local git branches for safe deletion with content-level verification (PR state + git cherry), flag-don't-delete discipline, and a mandatory user approval gate. Auto-invoke when user says "audit branches", "prune branches", "clean up branches", "stale branches", or "which branches can I delete".
---

## Why this exists

This repo accumulates local branches fast: interactive sessions, the Pilot
daemon (`pilot/GH-*`), recovery work, backups. A 2026-07-05 audit found 29
local branches; 21 were safely deleted — but only after content-level
verification caught, among others, **15 unpushed commits on `feat/aws-bench`
that existed nowhere else**. Ancestry checks alone would have missed that.

**Prime directive: a branch is never deleted without explicit per-branch user
approval, and never on ancestry evidence alone.**

## Method

### Step 1 — Enumerate COMPLETELY

```bash
git branch --format='%(refname:short)'   # never pipe through head — a truncated
                                         # list caused a missed-branch round
git worktree list                        # branches checked out in worktrees
                                         # cannot be deleted and may be live sessions
```

Exclude from the audit: the current `main` checkout, any `claude/*` branch
attached to a worktree (possibly a live session), and `pilot-worktree-GH-*`
daemon worktrees (per CLAUDE.md, leave alone).

### Step 2 — Per-branch evidence (mechanical)

For each branch collect:

```bash
git log -1 --format='%h %cd %s' --date=short "$b"          # tip
gh pr list --head "$b" --state all --json number,state      # PR record
git cherry main "$b" | grep -c '^+'                         # non-equivalent commits
```

**`git cherry` is the ground truth, not `--merged`.** This repo squash-merges:
merged branches are never ancestors of main, and `git branch --merged` is
useless. `git cherry` compares patch IDs — 0 non-equivalent commits means
every change is content-identical to something on main.

### Step 3 — Classify

| Class | Criteria | Action |
|---|---|---|
| SAFE | merged PR **and** cherry = 0 | propose deletion |
| SAFE (ancestor) | no PR, cherry = 0, `merge-base --is-ancestor` = yes | propose deletion |
| HOLDS-WORK | cherry > 0 | deep-check (Step 4) — never propose blind |
| CHECKED-OUT | in a worktree | check worktree dirty state; live sessions untouchable |

### Step 4 — Deep checks for HOLDS-WORK branches

1. **Local vs remote divergence** — the highest-stakes check:
   ```bash
   git fetch origin "$b"
   git rev-list --count origin/"$b".."$b"   # local-only commits = sole copy
   ```
   If local is ahead: **push to origin as backup before anything else**.
2. **Closed-PR supersession** — a closed (not merged) PR may carry the answer:
   ```bash
   gh pr view <n> --json comments --jq '.comments[-2:][].body'
   ```
   Precedent: `recover/TASK-300-ghost-sha-guard` showed 2 "unique" commits,
   but PR #3189's closing comment named the superseding PR (#3193) and the
   guard was verified live in `internal/executor/git_freshness.go` — same
   logic, different patch IDs. Verify the supersession claim **in main's
   code**, not just the comment.
3. **Lost-docs salvage** — backup branches can hold `.agent/` files missing
   from main (graph nodes referencing nonexistent files are the tell):
   ```bash
   git diff --name-only $(git merge-base main "$b") "$b" | while read f; do
     [ -e "$f" ] || echo "MISSING: $f"
   done
   ```
   Salvage via a docs PR before considering deletion.

### Step 5 — Approval gate (mandatory)

Present the full classification table with evidence. Deletion requires
explicit user approval, per branch or per named group. Branches the user has
flagged as sensitive (historically: `feat/aws-bench`, `feat/openrouter-engine`)
stay untouched regardless of evidence.

### Step 6 — Execute in safety order

1. Backup pushes first (`git push origin <branch>` for any sole-copy work)
2. Salvage PRs second
3. Deletions last: `git branch -D <approved...>`
4. `git remote prune origin`

## Pitfalls learned in the field

- `gh pr merge --delete-branch` fails with *"'main' is already used by
  worktree"* in multi-worktree layouts — merge succeeds remotely; verify with
  `gh pr view --json state`, clean the local branch manually.
- A branch checked out in a clean worktree at a merged tip loses nothing when
  the worktree is removed — but confirm `status --porcelain` is empty first.
- Deleted branches remain reflog-recoverable for ~90 days; mention this in the
  approval ask.
