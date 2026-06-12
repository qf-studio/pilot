# TASK-355: Board-sourced execution no-op'd, recorded `completed` with the wrong-repo commit SHA

> **✅ CLOSED 2026-06-12.** Same root cause + fix as TASK-320 B2 (#3571); live-verified via GH-3583 (`completed` + real worker SHA `b18f35b6ca` matching PR #3593). One clean board-equivalent run achieved. Archived.

**Status:** 🎯 **ROOT-CAUSED 2026-06-11, fix in review — PR [#3571](https://github.com/qf-studio/pilot/pull/3571)** (shared root cause with TASK-320 B2): `getPostExecutionSummary` (runner.go) spawned `claude` with no `cmd.Dir` → its `git log -1` ran in the **daemon's CWD** and reported the **daemon repo's HEAD** as the commit SHA. That is exactly run `afb3b68d`'s `ee238476` ("pilot daemon's own HEAD") below: foreign SHA → ghost-guard `merge-base` errors in the studio-sdk worktree → fail-open → recorded `completed` with the wrong-repo SHA. Fix: worktree git harvest before the LLM summary + `cmd.Dir = executionPath`. Close this task when #3571 ships and a board-sourced no-op records either a real same-repo SHA or a clean no-op without a foreign SHA.
**Priority:** P1 — false-positive completion is the TASK-320/321 class; corrupts the lifecycle
**Severity:** high (executor correctness)
**Pilot:** ⚠️ **MANUAL candidate** — self-modifying executor/no-op core (TASK-320 B2 / TASK-323 precedent)
**Related:** [[TASK-319]], [[TASK-354]], TASK-320 (executor false-negative no-op), TASK-321 (phantom redispatch)

## ⤴ Update (2026-06-01) — hypothesis #1 REFUTED by the #12 control run

The TASK-319 follow-up smoke test dispatched a **trivial, one-shot-able** board issue
(`studio-sdk#12`, test-only) as a control. Both runs recorded `project_path =
/Users/aleks.petrov/Projects/startups/studio-sdk` (correct). Outcomes:

| run | issue | outcome | project_path | commit_sha | line stats | PR |
|---|---|---|---|---|---|---|
| `afb3b68d` | #11 | no-op | studio-sdk ✅ | `ee238476` ❌ (**pilot daemon's own HEAD**) | 0/0 | none |
| `7809982c` | #12 | success | studio-sdk ✅ | `ad0a4d48` ✅ (real studio-sdk worktree HEAD) | 0/0 ❌ | #13 (merged → `18d2a02`) |

**Conclusions:**
- **Hypothesis #1 (wrong `project_path` resolution) is REFUTED.** Board-sourced dispatch
  runs in the *correct* studio-sdk worktree — #12 produced a genuine same-repo commit,
  pushed branch, and merged PR. Drop the `main.go:846-852` / project_path investigation.
- **Hypothesis #2 (no-op recorded `completed` + foreign SHA) CONFIRMED + localized.** On a
  real commit the SHA is captured correctly; **only on the no-op path** does it fall back to
  the daemon's own process HEAD (`ee238476` = pilot's `main`). That foreign, non-empty SHA is
  exactly why the "worktree HEAD == base parent" no-op guard was bypassed — it compared
  against the wrong git context. Fix: capture the SHA from the issue's worktree (and treat
  "no new commit in the worktree" as the no-op signal) instead of falling back to the
  orchestrator's CWD/HEAD.
- **New corroborating bug: line/file stats are mis-captured even on success.** #12 recorded
  `0 lines / 0 files` despite PR #13 being `+66/-0` (one new file). The diff-stat stage isn't
  reading the worktree diff — same wrong-git-context root cause as the SHA fallback. Fold the
  stats capture into the same fix.

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

- [x] A board-sourced issue executes in the correct project's worktree (verified by logged path + a real same-repo commit SHA). **— DONE: #12 → studio-sdk commit `ad0a4d48`, PR #13 merged.**
- [ ] A genuine no-op is recorded `failed` (no-deliverable), never `completed` with a foreign `commit_sha`. **(primary remaining fix — SHA/no-op detection reads worktree HEAD, not the daemon's CWD/HEAD.)**
- [ ] Line/file diff stats are computed from the issue's worktree diff (#12 recorded 0/0 for a +66 PR).
- [ ] Regression test covering the no-op detection + SHA/stat capture from the worktree (not the orchestrator's git context).
- [ ] `make test` + `make lint` green; `-race` clean if touching the runner.

## Out of scope
- Whether SDK M2 itself is one-shot-able (it's a hard cross-repo port). This task is about the
  executor recording/path correctness, not the task difficulty.
