# SOP: Reconcile scattered uncommitted work against shipped main

**When to invoke:** You walk into a session and find untracked files, branches with stale commits, and "uncommitted" work scattered across the main repo / worktrees. You don't know what's already on main vs. what's genuinely new vs. what's an orphan. You need a clean state without losing anything real.

**First-time established:** 2026-05-27 Wave 4 reconciliation session. Took ~3 hours and one broken-build incident to converge. This SOP captures the path that works.

---

## TL;DR

1. **Audit first, never wipe blind.** Identify each file's status vs `origin/main` BEFORE deciding what's trash.
2. **Work in a fresh `.claude/worktrees/<slug>` off `origin/main`** — never reconcile from the cluttered branch itself.
3. **Tar backup before any destructive op.** Stash > delete.
4. **Classify each file/branch** into one of five buckets (rubric below).
5. **One PR per task** for genuinely new work. Use `.agent/drafts/` for half-baked code you want Pilot to finish.
6. **Verify build + tests after every change**, never leave the project broken.

---

## Phase 1 — Audit (read-only)

### Snapshot what exists

```bash
# Where am I, what branch?
pwd
git rev-parse --abbrev-ref HEAD

# What's in the working tree (modified + untracked, including ignored locations)
git status --short -uall

# All worktrees (other Claude sessions may be mid-work)
git worktree list

# Branches local vs remote
git branch -a
git fetch origin --prune
```

### Get the truth about `main`

```bash
git fetch origin main --quiet
git rev-parse origin/main           # what's actually live
git log HEAD..origin/main --oneline # commits I'm missing
git log origin/main..HEAD --oneline # commits I have that main doesn't
```

If the current branch diverged from `origin/main`, **assume `origin/main` is authoritative.** Anything on the local branch that looks "ahead" may be stale predecessors of commits that landed via a different SHA (squash-merge, rebase, etc.).

### Per-file verdict vs `origin/main`

For each untracked or modified file, run:

```bash
# Is this file already on main?
git cat-file -e origin/main:"$f" 2>/dev/null && echo "ON MAIN" || echo "NEW"

# If on main, how much does our local version differ?
git diff origin/main -- "$f" | wc -l
```

Helper loop (template):

```bash
for f in <list>; do
  on_main=$(git cat-file -e origin/main:"$f" 2>/dev/null && echo "yes" || echo "no")
  if [ "$on_main" = "yes" ]; then
    n=$(git diff origin/main -- "$f" | wc -l | tr -d ' ')
    [ "$n" = "0" ] && echo "$f  ON MAIN identical — discard"
    [ "$n" != "0" ] && echo "$f  ON MAIN, $n-line diff — check"
  else
    echo "$f  NEW — needs landing"
  fi
done
```

### Classification rubric

| Bucket | Symptom | Action |
|---|---|---|
| **A. Already on main, identical** | `git diff origin/main -- $f` is empty | Discard locally |
| **B. Already on main, stale predecessor** | Diff non-empty, but `git log -- $f` on main shows a newer commit that supersedes your version | Discard locally; `origin/main` is authoritative |
| **C. Genuinely new work** | Not on main, has callers/value | Land via PR |
| **D. Orphan** | Not on main, no callers (grep across repo finds zero imports/refs) | Discard |
| **E. Half-baked draft** | Compiles standalone but doesn't fit current main's API | Park under `.agent/drafts/<task-slug>/` + create GH issue with `pilot` label |

**Rule of thumb:** If a file appears as "uncommitted" but its functionality is already on main (likely via a squash-merge or differently-named PR), it's **B**. Discard. Don't try to merge stale predecessors back in.

---

## Phase 2 — Branches audit

For each local-only branch (`git branch | grep -v origin`), classify:

```bash
git log origin/main..<branch> --oneline   # unique commits
gh pr list --state all --head <branch>    # was this branch ever PR'd?
```

| Branch state | Action |
|---|---|
| Unique commits all superseded on main (different SHAs, same titles/intent) | Delete locally |
| PR closed/unmerged, work since shipped via different PR | Delete locally |
| PR still open, work valid | Leave alone |
| Holds genuine WIP, never PR'd | Decide: PR it, or park under `.agent/drafts/` |

For the user's own working branches (e.g. `recover/TASK-NNN-*`), **ask before deleting** — they may want to PR or keep as reference even if the work shipped via another path.

---

## Phase 3 — Set up a safe workspace

**Never reconcile from the cluttered branch in the main repo.** Two reasons:

1. The main repo's working tree may be hosting another agent's in-flight work (Pilot executor, parallel Claude sessions). Mistaking their changes for "your" leftovers leads to silent damage.
2. Switching branches with untracked files present is risky — Git won't auto-stash untracked, and a `git checkout` or `git reset` against unfamiliar state burns work.

### Worktree setup

```bash
# From main repo dir
git fetch origin main --quiet
git worktree add -b reconcile/<slug> .claude/worktrees/<slug> origin/main
cd .claude/worktrees/<slug>
```

The worktree gives you isolation: your reconciliation work, your branch, your build/test. The main repo is left alone.

### Tar backup before any destructive op

```bash
mkdir -p /tmp/pilot-reconcile/$(date +%Y-%m-%d)
# Save modifications as patch (for tracked files):
git diff > /tmp/pilot-reconcile/$(date +%Y-%m-%d)/modifications.patch
# Save untracked files as tarball:
git status --porcelain -uall | grep '^??' | awk '{print $2}' > /tmp/pilot-reconcile/untracked-list.txt
tar -czf /tmp/pilot-reconcile/$(date +%Y-%m-%d)/untracked.tar.gz -T /tmp/pilot-reconcile/untracked-list.txt
```

If anything goes wrong, the tar lets you restore: `tar -xzf /tmp/pilot-reconcile/.../untracked.tar.gz`

---

## Phase 4 — Land the work

### For genuinely new code (bucket C)

If it's **clearly your work** and reviewable in one PR: branch off `origin/main`, commit, push, open PR. Squash-merge.

If it's **new feature code that should go through the Pilot pipeline** (per project CLAUDE.md "Navigator + Pilot" rule for interactive sessions):

1. Park the half-baked code under `.agent/drafts/<task-slug>/` with a README explaining the gap
2. Open a GH issue with the `pilot` label that references the draft path
3. Pilot picks up the issue, finishes the integration, ships a PR, deletes the draft directory in the same commit

This is faster and respects the pipeline. Don't write integration glue yourself in an interactive session.

### For docs / spike findings / SOPs

Direct PR off `origin/main`. Squash-merge. No Pilot needed.

### For salvage from old branches

Cherry-pick the specific commits worth keeping; abandon the rest of the branch.

```bash
git checkout origin/main -b feat/<descriptive-name>
git cherry-pick <sha>
git push -u origin <branch>
gh pr create --title "..." --body "..."
```

### For superseded PRs

Close with a comment pointing to what shipped instead. Don't just abandon.

```bash
gh pr close <N> --comment "Superseded by #<M> which shipped <thing> on <date>. Closing as redundant."
```

---

## Phase 5 — Verify and clean up

### After every PR merge

```bash
git fetch origin main --quiet
git -C <main-repo> pull --ff-only origin main
go build ./...                 # or whatever the project's build command is
go test ./...                  # or focused test scope
```

If anything is red, fix or revert immediately. **Never leave the project in a broken state.**

### Stash residual working-tree clutter

If the main repo still has untracked / modified files that aren't yours:

```bash
cd <main-repo>
git stash push -u -m "session-$(date +%Y-%m-%d)-residual"
git status   # should be clean
```

Stash is reversible — `git stash list` / `git stash pop`. Leave for the user to inspect later; **don't `git clean -fd`** without explicit go-ahead.

### Delete obsolete local-only branches

Only after their commits are confirmed shipped (or salvaged elsewhere):

```bash
git branch -D <branch>   # local
gh api -X DELETE "repos/<org>/<repo>/git/refs/heads/<branch>" 2>&1 || true   # remote, if needed
```

### Reset local `main` if it diverged

When the local `main` branch is ahead-and-behind of `origin/main` (typically: ahead with duplicate-SHA cherry-picks, behind on real progress), don't try to merge. Backup + reset:

```bash
git branch backup/local-main-$(date +%Y-%m-%d) main
git checkout main
git reset --hard origin/main
```

The backup branch keeps your displaced commits in case any was novel. Verify, then delete the backup when you're sure.

---

## Hardened pitfalls (don't repeat)

| Pitfall | What goes wrong | Mitigation |
|---|---|---|
| Reconcile in-place on the main repo's feature branch | You can't tell whose work is whose; switching branches with untracked files is brittle | Use `.claude/worktrees/<slug>` |
| `rm` untracked files without checking tracked diffs | Tracked-file modifications may depend on the untracked source files; wipe breaks the build | Run `go build ./...` BEFORE and AFTER any wipe |
| Assume "uncommitted == new work" | Most "uncommitted" files in a stale branch are predecessors of already-shipped main code | Diff each file against `origin/main` |
| Skip the audit because you "remember" the state | The state changes between sessions, between cron firings, and from other agents | Re-run `git status -uall` before every destructive op |
| Run a polling loop on an empty queue | Generates noise, masks real signals | Check the queue once first; only start a loop when something's actually in flight |
| Develop dormant code while reconciling | Conflates "cleanup" with "new feature work" — defeats both | Park dormant code on a feature branch (e.g. `feat/<name>`); don't merge until follow-ups land |
| Manual factory wire-up / glue code in interactive session | Bypasses Pilot's quality gates + knowledge graph | Create GH issue with `pilot` label; let Pilot do the integration |

---

## When to consult Pilot vs. do it yourself

In **interactive sessions** (per project CLAUDE.md):

- ✅ DO yourself: cleanup, docs, SOP writing, branch hygiene, PR review/merge
- ✅ DO yourself: cherry-picks of small, isolated commits
- ❌ DELEGATE to Pilot: any new code (factory wires, glue, feature impl, refactors)
- ❌ DELEGATE to Pilot: anything requiring a new PR with real implementation

The cost of delegating tiny wire-ups feels high but it's worth it — Pilot's pipeline catches things ad-hoc edits miss.

---

## Related

- `.agent/knowledge/memories/pitfalls/pitfall_verify_branch_before_destructive_ops.md` — pre-flight checklist for destructive ops
- `.agent/knowledge/memories/learnings/feedback_use_worktree_for_reconciliation.md` — why worktree, not in-place
- `.agent/knowledge/memories/learnings/feedback_no_apologies_just_working_project.md` — verify build/tests, no apology theater
- `.agent/knowledge/memories/learnings/feedback_check_state_before_designing.md` — check before automating
- `.agent/sops/git/never-reset-hard-in-automated-flows.md` — when reset is OK and when it isn't
