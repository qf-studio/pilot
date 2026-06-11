---
name: Subprocess without cmd.Dir — daemon-CWD SHA poisoned the commit harvest
description: getPostExecutionSummary spawned claude with no cmd.Dir, so its git commands ran in the daemon's CWD and reported the wrong repo's HEAD as the commit SHA — turning real worker commits into ghost-guard no-ops (TASK-320) and recording wrong-repo completed SHAs (TASK-355).
type: pitfall
originTask: TASK-320
---

`getPostExecutionSummary` (`internal/executor/runner.go`) ran
`claude --print -p "run 'git log --oneline -1' ..."` with **no `cmd.Dir`**. The
subprocess inherited the **daemon's working directory**, so the LLM dutifully
reported *that* repo's HEAD as "the commit SHA" — not the execution worktree's.

Two failure modes from one bug, both observed live (2026-06-10/11):

1. **Daemon CWD = project repo pinned to main** → reported SHA is an ancestor of
   `origin/main` → the ghost-SHA guard (GH-3126) "correctly" rejects it →
   `no_op` ("no new commit produced") → worktree cleanup destroys the worker's
   real, hook-passing commit. 4/4 deterministic on GH-3569/GH-3570; transcript
   proved the worker committed (`ecdf5017`) before the harvest discarded it.
2. **Daemon CWD = a different repo** → foreign SHA → `merge-base` errors in the
   worktree → guard **fails open** → execution recorded `completed` with a
   wrong-repo SHA (TASK-355's `ee238476` = the daemon repo's own HEAD).

Why it looked intermittent: when the summary subprocess failed, the correct,
worktree-bound git fallback ran instead — the outcome depended on a subprocess
race, not task content. Workers that pushed + opened PRs in-session "survived"
(their work lived on the remote), making the executor look healthy.

**Fixes (PR #3571, v2.186.2):** deterministic git harvest in the worktree runs
BEFORE the LLM summary; the summary is last-resort and pinned via
`cmd.Dir = executionPath`.

**Lessons:**
- Every `exec.Command` in the executor MUST set `cmd.Dir` explicitly — the
  daemon's CWD is never the right execution context.
- Never ask an LLM for a fact git answers deterministically (`git -C <dir>
  log -1 --format=%H`); the LLM layer added a silent wrong-directory hazard.
- When a guard fires "deterministically" on good work, suspect the guard's
  INPUT before the guarded behavior — verify by artifact (worker transcript,
  worktree state), not by the classifier's verdict ([[mem-033]]).

Related: [[bug_premature_parent_close_partial_links]] (the no-op classification
fed the wave-2 incident chain), TASK-320, TASK-355.
