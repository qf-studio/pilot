---
name: verify-branch-and-working-tree-before-destructive-ops
description: Before ANY destructive op (rm, git checkout HEAD --, branch delete, wipe) in the Pilot main repo, run pwd + git status + git rev-parse HEAD and READ the output. The main repo working tree often holds in-progress work from other sessions.
type: pitfall
---

Before ANY destructive op in the Pilot main repo (`rm`, `git checkout HEAD -- <file>`, `git stash drop`, branch deletes, wipes), **stop and verify** what's actually there. Required pre-flight:

1. `pwd` — confirm you're in the directory you think you're in
2. `git rev-parse --abbrev-ref HEAD` — confirm the branch
3. `git status --short -uall` — confirm what's modified AND what's untracked
4. **READ the output**. Don't skim. The main repo's working tree often hosts in-progress work made outside the current session — from other Claude agents, from offline manual edits, from Pilot executor sessions.

**Why:** During a Wave 4 reconciliation session (2026-05-27), I (Claude) wiped 14 untracked files from the main repo to "clean up duplicates" — but `backend_factory.go` and `runner.go` had **tracked** modifications from another session that depended on those untracked files (engine.go, workflow/*). Wipe broke the build. User had to intervene angrily. Recovery was only possible because a tar backup was made earlier.

**How to apply:**

- For any destructive op on the main repo, prefer `git stash push -u -m "<context>"` over `rm` so the action is reversible
- If files appear "duplicate" with another branch/PR, that does NOT mean they're disposable — they may be the source dependencies of tracked-file modifications you haven't noticed yet
- Always run a `go build ./...` before AND after touching the working tree to catch breakage immediately
- Prefer working in a `.claude/worktrees/<name>` worktree over operating on the main repo — leaves main repo's in-flight state alone
- If you discover unexpected tracked modifications mid-session (files that weren't M at session start), STOP. Don't proceed with cleanup until you've inspected the diff and understood whose work it is
