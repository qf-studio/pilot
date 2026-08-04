---
name: localmode-tasks-never-get-worktree-qdocs-in-root
description: LocalMode/Q&A tasks (Branch=="") can never get a worktree — runner.go:2162's gate requires a non-empty Branch regardless of allowWorktree — so they run in the shared repo root and CreateTaskDoc writes untracked q-<epoch>.md files into <root>/.agent/tasks/. Intentional for read-only tasks, but the doc side-effects dirty the shared checkout; recurred for months (two archived q-*.md instances).
type: pitfall
---

# LocalMode tasks silently run in the repo root — q-docs are the fingerprint

**What happens:** `handleQuestion` (`internal/comms/handler.go:410`, the
Telegram/Slack question intent) builds `Task{ID: "Q-<epoch>", LocalMode:
true}` with **no Branch**. The worktree gate
(`runner.go:2162`: `allowWorktree && r.config.UseWorktree && task.Branch !=
"" && !task.DirectCommit`) fails on the empty Branch, so `executionPath`
stays `task.ProjectPath` — the shared daemon repo root — for the whole task.
`CreateTaskDoc` (`runner.go:2828-2833`, filename lowercased by
`docs.go:69`) then writes `<root>/.agent/tasks/q-<epoch>.md`, untracked.

**Evidence it recurs:** `.agent/tasks/archive/q-1771018269.md` and
`q-1778013288.md` are committed in the repo; a third (`q-1784736791.md`,
~2026-07-22) sat untracked in the box root until 2026-08-04.

**Why it's only half a bug:** root-scoped execution is *intentional* for
read-only Q&A/CLI analysis — the defect is the write side-effects (task
docs) landing in a shared checkout, plus the general principle that
`Branch==""` means "no isolation" silently rather than explicitly.

**How to apply:**
- Untracked `q-*.md` in a repo root = this mechanism, not an intruder.
  Archive or delete freely.
- GH-4703's chokepoint guard must keep the LocalMode/`Branch==""`/
  `DirectCommit` exception — root execution is legitimate there; blocking it
  breaks `handleQuestion`/`handleResearch`/`pilot task`.
- If q-doc litter becomes a problem, the fix is routing LocalMode task docs
  to a temp/state dir, not forcing worktrees onto read-only tasks.

Related: [[runselfreview-runs-in-repo-root-phantom-reimplementation]].
