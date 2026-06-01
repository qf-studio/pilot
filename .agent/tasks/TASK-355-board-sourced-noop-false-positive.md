# TASK-355: Board-sourced execution no-op'd, recorded `completed` with the wrong-repo commit SHA

**Status:** open — found during TASK-319 go-live smoke test (2026-06-01)
**Priority:** P1 — false-positive completion is the TASK-320/321 class; corrupts the lifecycle
**Severity:** high (executor correctness)
**Pilot:** ⚠️ **MANUAL candidate** — self-modifying executor/no-op core (TASK-320 B2 / TASK-323 precedent)
**Related:** [[TASK-319]], [[TASK-354]], TASK-320 (executor false-negative no-op), TASK-321 (phantom redispatch)

## Context

The TASK-319 smoke test sourced `qf-studio/studio-sdk#11` ("extract GitLab+Azure DevOps connectors,
SDK M2") from the board and executed it for 15m5s. The issue body carries an explicit
"defeat-the-no-op" preamble because the deliverables are NEW files. Outcome:

```
executions row afb3b68d (2026-06-01 15:08):
  status:        completed          ← NOT failed
  commit_sha:    ee238476           ← this is the PILOT repo's main HEAD, not a studio-sdk sha
  files_changed: 10 / lines_added: 0
  pr_url:        (none)
  task_branch:   pilot/GH-11        ← never pushed to the studio-sdk remote
```

Compare a real success (GH-6 → `studio-sdk/pull/10`): `commit_sha=e8f8ea93` (a genuine studio-sdk
sha) + PR created. So #11 **no-op'd** (0 lines, no commit pushed, no PR) yet was recorded as
`completed`, and the recorded `commit_sha` is **pilot's HEAD `ee238476`**, not a studio-sdk commit.

## Two hypotheses (investigate)

1. **Wrong `project_path` resolution for board-sourced issues.** The board source supplies issues
   from `qf-studio/studio-sdk`, but the recorded SHA being *pilot's* HEAD suggests the executor may
   have resolved/run against the pilot repo (or its own CWD) instead of the studio-sdk project_path.
   Trace board-sourced dispatch → which `projectPath` reaches the runner (`main.go:846-852` wiring,
   `handleGitHubIssueWithResult`'s `projectPath` arg). The global adapter's `project_path` was set to
   studio-sdk, but board-sourced issues may not pick it up.
2. **No-op recorded as `completed` instead of caught.** Even on a genuine no-op, status should not be
   `completed` with a stale/foreign `commit_sha`. The "no new commit produced — worktree HEAD matches
   base branch parent" guard (seen as `status=failed` elsewhere) did not fire here — possibly because
   the foreign HEAD (`ee238476`) didn't match the studio-sdk base parent, so the guard was bypassed.

`files_changed=10 / lines_added=0` is itself suspicious — investigate how that stat is computed on
the no-op path.

## Approach

- Reproduce: dispatch a board-sourced studio-sdk issue and log the resolved `projectPath` + worktree
  base before/after execution. Confirm whether the runner ran in pilot vs studio-sdk.
- If project_path mis-resolves for board-sourced issues, fix the resolution so board-sourced work
  runs in the correct project's worktree.
- Harden the no-op/commit-sha recording so a 0-deliverable run is `failed` (no-op), never `completed`
  with a foreign SHA.

## Acceptance

- [ ] A board-sourced issue executes in the correct project's worktree (verified by logged path + a real same-repo commit SHA).
- [ ] A genuine no-op is recorded `failed` (no-deliverable), never `completed` with a foreign `commit_sha`.
- [ ] Regression test covering the board-sourced dispatch project_path resolution.
- [ ] `make test` + `make lint` green; `-race` clean if touching the runner.

## Out of scope
- Whether SDK M2 itself is one-shot-able (it's a hard cross-repo port). This task is about the
  executor recording/path correctness, not the task difficulty.
