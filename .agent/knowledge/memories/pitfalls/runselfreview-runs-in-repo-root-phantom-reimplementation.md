---
name: runselfreview-runs-in-repo-root-phantom-reimplementation
description: runSelfReview (runner.go:5223) spawns its review subprocess with ProjectPath = task.ProjectPath (daemon repo ROOT), not executionPath (worktree) — empty root diff + the prompt's "FIX missing changes" instruction makes it re-implement the task's spec from scratch and stage a phantom copy into the shared root. Third recurrence of the TASK-323/GH-3577 class; fix = GH-4702, chokepoint = GH-4703.
type: pitfall
---

# Self-review runs in the repo root — and "fixes" the empty diff by reimplementing the task

**Incident (found 2026-08-04):** the box repo root had a STAGED, uncommitted
reimplementation of GH-4659's helper under a different name
(`hasNonTerminalDecomposedChild` vs the merged
`decomposedChildLedgerNonTerminal`). It blocked the morning rebuild
(`git checkout` refused over the dirty root).

**Mechanism (nav-research, confidence 0.85):**
1. `runSelfReview` (`internal/executor/runner.go:5223-5226`) passes
   `ProjectPath: task.ProjectPath` — the daemon repo root. `task.ProjectPath`
   is never reassigned; the worktree lives in `executionPath`
   (`runner.go:2201`). So `cmd.Dir` = shared root
   (`backend_claudecode.go:554`) for every self-review, on every task.
2. In the root, `git diff --cached` (`prompt_builder.go:542`) shows nothing —
   the real diff exists only in the worktree.
3. The self-review prompt (`prompt_builder.go:565-584`) says: if files the
   issue mentions are NOT in the diff, **make the required changes**. The
   session obeys — re-derives the spec from the embedded issue text, writes
   its own implementation, stages it. Never commits (prompt asks for fixes,
   not commits) → index-only dirt in the shared root.

**Class history:** same bug fixed reactively at 4 other call sites —
TASK-323 (`runner.go:3402`, `:3759`) and GH-3577/PR#3580 (`:4120`, `:4402`).
`runSelfReview` was missed both times. `mockSelfReviewBackend.Execute`
discards `ExecuteOptions` (`runner_test.go:3209`), so no test could catch it.

**How to apply:**
- A dirty box-repo root is a SYMPTOM of this class — check for staged (not
  just unstaged) changes and `git stash push` pathspec-limited with a
  descriptive message before any rebuild; never reset.
- Fix: GH-4702 (one-line, mirror the GH-3577 diff). Structural guard:
  GH-4703 (chokepoint wrapper, `repo_guardrail.go` idiom).
- When adding ANY new `backend.Execute` call site: `ProjectPath` must be
  `executionPath`, never `task.ProjectPath` (until GH-4703 makes this
  structural).

Related: [[localmode-tasks-never-get-worktree-qdocs-in-root]] (the other
root-writing mechanism found in the same investigation).
