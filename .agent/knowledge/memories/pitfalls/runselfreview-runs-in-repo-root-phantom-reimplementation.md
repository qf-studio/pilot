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
  just unstaged) changes first. Repair = prove the dirt is a phantom copy
  (see 2026-09-04 procedure below), pin it on a `backup/*` branch, then
  `git reset --hard origin/main`. Never reset before the proof.
- Fix: GH-4702 (one-line, mirror the GH-3577 diff). Structural guard:
  GH-4703 (chokepoint wrapper, `repo_guardrail.go` idiom).
- When adding ANY new `backend.Execute` call site: `ProjectPath` must be
  `executionPath`, never `task.ProjectPath` (until GH-4703 makes this
  structural).

Related: [[localmode-tasks-never-get-worktree-qdocs-in-root]] (the other
root-writing mechanism found in the same investigation).

## Confirmed instances + repair (forensics 2026-09-04, box via SSM)

Two roots stayed dirty for 5 weeks after the 08-04 fix because nothing repairs
a root; the fix only stops new writes. Both were this class, pre-#4706 binary
(v2.249.0):

| Root | Root write | Task | Self-review window | What the phantom was |
|---|---|---|---|---|
| `pilot-console` | index mtime 11:44:28Z 07-29, staged `0005_sessions.{up,down}.sql` | GH-63 (worktree `/tmp/pilot-worktree-GH-63-*`, `useWorktree=true`) | 11:43:33–11:44:44Z | re-derived migration; real one merged as console PR#66 |
| `pilot-console-ui` | `pull --ff-only` 18:26:54Z + commit `64069db` on root `main` 18:28:19Z | GH-17 (worktree `/tmp/pilot-worktree-GH-17-*`) | 18:26:25–18:28:25Z (SIGKILL at deadline) | re-implemented vite proxy; real one squash-merged as ui PR#18 (`23593e8`) |

The "126 staged deletions / −40k lines" reading was an artifact, not data
loss: `main` kept advancing (ref-only, empty reflog messages, 08-06→08-17)
while index+worktree stayed pinned at the 07-29 tree, so every file added
upstream since showed as `D` against HEAD. `syncMainBranch` (`merge --ff-only`)
then started WARN-skipping from 08-29 once upstream touched the phantom paths.

**Repair procedure that was applied (safe because no unique content):**
1. Prove no unique content: `git diff --cached <last-real-HEAD> --stat` shows
   only the phantom files; every `D` path exists in `origin/main`; any local
   commit is content-equivalent to a merged PR; no stashes.
2. Pin the state: `git commit-tree $(git write-tree) -p <HEAD>` → `git branch
   backup/root-index-<date>`; local commits → `git branch backup/root-commit-<date>`.
   Stray `.claude/worktrees/*` → commit WIP on its branch, `git worktree remove`.
3. `git reset --hard origin/main` with the queue empty (`executions` has no
   running/queued rows for that project). Verify `status --porcelain` empty and
   `HEAD == origin/main`.
Backups left on the box: `backup/root-index-2026-07-29` + `worktree-agent-a41a6b96`
(console), `backup/root-commit-2026-07-29` (ui).

**Still open from this class:** `finish_tripwire_root_clean` is stateless (no
pre-execution baseline), so a root left dirty once fires on every later finish
(381 violations = 1 condition). Unexplained but benign: ref-only `main`
advances 08-06→08-17 with empty reflog messages (no HEAD reflog, no index) —
not `syncMainBranch`, not any grep-able `update-ref`/`main:main` idiom in the
daemon or recordings.
