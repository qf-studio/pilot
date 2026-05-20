---
name: git reset --hard in automated post-task sync silently destroys local commits
description: syncMainBranch() uses git reset --hard origin/main after every task. When push propagation to GitHub is slower than the immediately-following fetch+reset, local commits are silently destroyed. Fix: replace with merge --ff-only.
type: pitfall
incident: 2026-05-20 gitnation-companion-pilot workshop demo (TASK-283 / GH-3018)
severity: critical
files:
  - internal/executor/runner_git.go:45
  - internal/executor/epic.go:1498
  - internal/executor/runner.go:3258
---

**Symptom**: Pilot completes a task, commits to `main`, pushes, then `main` rewinds and the just-committed work is gone. Only recoverable via `git reflog` within 90 days.

**Reflog signature** (from 2026-05-20 demo):
```
7ca8846 HEAD@{0}: reset: moving to origin/main      ← sync wiped the commit
51e7422 HEAD@{1}: commit: feat(tooling): M1.1 …     ← Pilot's local commit
7ca8846 HEAD@{2}: reset: moving to 7ca8846
f9a287f HEAD@{3}: commit: feat(deploy): CI gate …   ← also wiped
```

**Root cause**: `runner_git.go:45` runs `git reset --hard origin/main` unconditionally after `git fetch origin main`. There is no divergence check, no `--ff-only` semantics. The race is **GitHub propagation latency**, not push failure:

1. `PushToMain` succeeds locally (HTTP 200 from github.com).
2. `syncMainBranch` immediately runs `git fetch origin main`.
3. GitHub's `origin/main` ref hasn't propagated yet — the fetch returns the *pre-push* SHA.
4. `reset --hard origin/main` rewinds local `main` to that stale SHA.
5. The just-pushed commit is now unreachable except via reflog.

**Why this is severe**:
- Silent — no error, no warning.
- Reflog-only recovery has a 90-day expiry window.
- Reordering push/sync doesn't fix it — the race is in remote-side propagation, not local code ordering.
- Two call sites; only one is gated by config:
  - `runner.go:3258` — gated by `executor.sync_main_after_task` flag (users can opt out).
  - `epic.go:1498` — **NOT gated**, fires unconditionally in sequential epic mode. Users who disabled the flag to dodge the bug were still exposed during sub-issue execution.

**Fix** (TASK-283 / GH-3018): Replace `reset --hard` with `merge --ff-only`. The fast-forward-only merge is a no-op when local is ahead/diverged and safely advances when local is behind. Failure modes (local-ahead, divergence, dirty working tree) all become safe non-fatal warnings.

**Don't**:
- Don't use `git reset --hard <remote>` in any automated post-action sync. The remote ref can lag, be unreachable, or point to a different branch than you think.
- Don't gate the fix on push success — the bug fires *after* successful push.
- Don't add a `sleep` to wait for propagation — propagation latency is unbounded.

**Sibling-trap check** (passed during TASK-283 research):
- `worktree.go:349` also runs `reset --hard` but on an isolated pooled tmpdir that is discarded after use — intentional and safe.
- No `push --force` in production Go code.

**Pattern to apply elsewhere**: For any "sync local to remote" hygiene step in automated flows, prefer `merge --ff-only` or `pull --ff-only`. If you need destructive behavior (rare), gate it on an explicit `git rev-list --count local..remote` divergence check first.
