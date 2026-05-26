---
name: git reset --hard in automated post-task sync silently destroys local commits
description: syncMainBranch() used git reset --hard origin/main after every task. When push propagation to GitHub was slower than the immediately-following fetch+reset, local commits were silently destroyed. Fixed in v2.146.7 with merge --ff-only.
type: pitfall
resolved: 2026-05-20 (v2.146.7)
incident: 2026-05-20 gitnation-companion-pilot workshop demo (TASK-283 / GH-3018)
severity: critical
files:
  - internal/executor/runner_git.go:45
  - internal/executor/epic.go:1498
  - internal/executor/runner.go:3258
---

**Pattern resolved** — see SOP at `.agent/sops/git/never-reset-hard-in-automated-flows.md`

**Fix shipped:** TASK-283 / GH-3018 / v2.146.7 — replaced `reset --hard` with `merge --ff-only` at both call sites.

---

**Original pitfall (archived for reference):**

**Symptom**: Pilot completes a task, commits to `main`, pushes, then `main` rewinds and the just-committed work is gone. Only recoverable via `git reflog` within 90 days.

**Root cause**: `runner_git.go:45` ran `git reset --hard origin/main` unconditionally after `git fetch origin main`. The race was **GitHub propagation latency** — GitHub's `origin/main` ref hadn't propagated yet after a successful push, causing the fetch to return a stale SHA, then reset --hard would rewind to it.

**Fix**: Replace `reset --hard` with `merge --ff-only`. Fast-forward-only merge is a no-op when local is ahead/diverged and safely advances when local is behind. All failure modes become safe non-fatal warnings.

**Pattern to apply elsewhere**: For any "sync local to remote" hygiene step in automated flows, prefer `merge --ff-only` or `pull --ff-only`.
