# TASK-320: Executor false-negative no-op fix (evidence-backed specs)

**Status:** 🟢 **ALL LAYERS SHIPPED** — A + B1 via PR [#3230](https://github.com/qf-studio/pilot/pull/3230) (merged 2026-05-29); **B2 root-caused + fixed in v2.186.2** (PR [#3571](https://github.com/qf-studio/pilot/pull/3571), MANUAL, 2026-06-11 — see below). Remaining: live verification — next dispatched code task must record the worker's real SHA + `completed`. Archive after that.

**🎯 LAYER B2 ROOT CAUSE FOUND + FIX IN REVIEW (2026-06-11, PR [#3571](https://github.com/qf-studio/pilot/pull/3571))** — NOT a v2.184+ regression and NOT the worker: **`getPostExecutionSummary` (runner.go) spawned `claude` with no `cmd.Dir`** → its `git log -1` ran in the **daemon's CWD** → reported that repo's HEAD as the commit SHA. Proof chain on #3569 (worker transcript `~/.claude/projects/...pilot-worktree-GH-3569.../35a7c062*.jsonl`): worker edited, built, tested, linted, committed `ecdf5017` (hooks passed) — then the harvest, poisoned with root HEAD `d63946d9` (ancestor of origin/main), let the ghost guard (GH-3126) classify the run `no_op` and the worktree cleanup destroyed the real commit. 4/4 deterministic on #3569/#3570 (daemon CWD = pilot root). **Same bug = TASK-355**: daemon CWD on a different repo → foreign SHA → merge-base errors → guard fails open → `completed` with wrong-repo SHA. Also explains #3558 + #3552 recorded `no_op`/`completed`-with-empty-SHA while their PRs (#3559/#3561) were real — workers that push+create PRs in-session survive; commit-only workers lose their work. **Fix (PR #3571, MANUAL)**: worktree git harvest BEFORE the LLM summary; summary pinned via `cmd.Dir = executionPath`. Earlier intermittency explained: when the summary subprocess failed, the (correct, worktree-bound) git fallback ran instead — outcome depended on a subprocess race, not task content.
**Priority:** P1 — systemic; blocks/poisons every explicit-spec task (GH-3222, GH-3228 class)
**Repo:** `qf-studio/pilot`
**Area:** `internal/executor/`
**Source bug:** GH-3224 (auto-filed). Instances: GH-3222 (`--version` one-liner, fixed manually in #3223), GH-3228 (board-as-source, 4× no-op).

---

## Why manual (not Pilot)

This is a fix **to the executor's own no-op behavior**. Handing it to Pilot is
chicken-and-egg: the bug being fixed is exactly what makes Pilot exit without
editing on explicit specs. It also edits Pilot's execution guard — high blast
radius, must be human-reviewed. Implement directly, open PR for review.

---

## Problem

The executor's model reads existing code, judges it "looks correct," and exits
with **zero edits** — overriding an explicit, evidence-backed spec with its prior.
The TASK-300 ghost-SHA guard catches the result (`HEAD == base parent`) but:
1. only **after** the wasted run, and
2. (bug) emits an **empty error string** — `execution failed:` with no message
   (observed on studio-sdk #4/#5 re-dispatch and implied on GH-3228).

Deterministic: same no-op on all retries. A P0 one-liner with a perfect spec
(exact file, line, before/after, proof current code is wrong) still could not be
executed autonomously.

---

## Goal

Make the executor honor evidence-backed specs over its prior, and when it still
no-ops, fail loudly and recover once — never silently.

---

## Implementation (3 layers)

### Layer A — prevent (prompt directive)
Inject into the executor task/system prompt:
> If the issue specifies an explicit change (file + line + before/after) with
> evidence the current code is wrong, you MUST apply it even if the existing code
> looks correct — your general knowledge does not override the spec's verified
> claim. If you conclude no change is genuinely needed, emit an explicit
> `NO-OP RATIONALE: <file:line> <reason>` block instead of exiting silently.

> **Scope shipped in first PR:** Layer A (prompt directive) + Layer B1 (terminal
> classification of the no-op so the poller stops the 4×-identical-retry burn and
> labels `pilot-blocked`). **Layer B2 (in-executor escalated re-invocation) is
> DEFERRED** — `executeWithOptions` is a ~1700-line critical function with existing
> self-review retry machinery (`runner.go:2843`); bolting a second backend
> re-invocation onto the ghost-SHA guard duplicates SHA-harvest + ghost-check +
> metrics logic and risks regressing the executor's core path. It needs a
> structured retry-loop refactor, tracked as a follow-up. Layer A alone addresses
> the GH-3222/GH-3228 root cause; B1 stops the wasted retries.

### Layer B1 — classify the no-op as terminal (SHIPPED)
- `runner.go`: added `"no new commit produced"` to `permanentFailurePatterns`
  (both ghost-SHA messages share that prefix). `IsPermanentFailure` now returns
  true → poller applies `pilot-blocked` (not `pilot-failed`), ending identical
  retries. The guard messages at `runner.go:2380`/`:3294` are already descriptive
  (no empty-error on this path).

### Layer B2 — recover (escalated single retry on no-op) — DEFERRED
At the ghost-SHA guard failure branch (HEAD == base parent):
1. **Fix the empty-error bug first** — always format a descriptive message, e.g.
   `no new commit produced — worktree HEAD matches base branch parent (issue %s)`.
   Never emit `execution failed:` with an empty tail.
2. Re-invoke the executor **once** with an escalation preamble:
   *"Your previous run made ZERO edits but this task requires a change. Re-read the
   explicit instruction and apply it. Do not exit without editing or without a
   NO-OP RATIONALE."*
3. If the retry still no-ops → fail with the descriptive error and auto-comment the
   `NO-OP RATIONALE` (if produced) so a human sees *why* the model refused.

### Layer C — observability (optional, low cost)
Counter/log for no-op-on-explicit-spec so the rate is visible (mirrors TASK-293
poller skip-reason counters pattern).

---

## Acceptance criteria

- [ ] No-op (HEAD == base parent) never produces an empty `execution failed:`
      message — always a descriptive, actionable string naming the issue.
- [ ] On no-op, the executor retries exactly once with the escalation preamble
      before failing.
- [ ] Prompt directive present in the executor template; covered by a prompt-
      assembly test.
- [ ] A regression test simulating an explicit-spec issue asserts the executor
      either edits or emits `NO-OP RATIONALE` (never silent exit).
- [ ] `make test` + `make lint` green.

## Out of scope
- Changing the ghost-SHA guard's correctness (it is right to reject empty commits).
- Model/router changes.

---

## Critical files (to locate during implementation)
- `internal/executor/` — ghost-SHA / TASK-300 guard site (the `no new commit
  produced — worktree HEAD matches base branch parent` string is the anchor:
  `grep -rn "no new commit produced" internal/`).
- Executor prompt template assembly (where the task prompt is built).

## Verification
```bash
grep -rn "no new commit produced" internal/   # find the guard
make test && make lint
go test ./internal/executor/ -run 'NoOp|GhostSHA|Prompt' -v
```

---

## Refs
- GH-3224 (root-cause report), GH-3222/#3223 (first instance + manual fix),
  GH-3228 / TASK-317 (second instance — unblocked once this ships).
