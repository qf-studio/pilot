# fix(executor): no-commits guards diff against stale local base ref — decomposed epics die "No commits between main and pilot/GH-N" at umbrella-PR creation

**Created**: 2026-07-27 · **Status**: ✅ Shipped 2026-07-27 — GH-4566→PR#4568 merged (origin-relative guards at all 3 sites + benign-No-commits backstop). Live proof pending: next decomposed epic on the box should finalize clean; then revisit the GH-916 "~10%" number · **Last Updated**: 2026-07-27

## Problem

Decomposed epics whose children merge to main routinely end `infra` with
`epic PR creation failed: GraphQL: No commits between main and pilot/GH-NNN
(createPullRequest)` — a spurious failure on an epic whose work fully shipped.
Two live hits on auth-service 2026-07-26: GH-435 (18:37Z) and GH-431 (23:49Z,
all 4 child PRs #471/#472/#474/#475 merged). The parent issue is usually
already closed by then (`epic.go:3025-3031` closes it before finalize), so the
damage is a false `infra` ledger row + failure alert on shipped work — plus,
until #4563/#4564, orphaned child rows.

## Root cause (verified 2026-07-27)

The empty umbrella branch is **by design**: post-TASK-401, nothing can commit
to it (children run isolated worktrees on their own branches, `epic.go:2643-2646,
2829`; planning is read-only, `epic.go:272-277`; parent re-dispatch
re-implementation is short-circuited, `dispatcher.go:2230-2258`). The
no-commits guard at `runner.go:1666` exists precisely to turn that into a
clean skip/no-op (GH-2743/TASK-356, classification per GH-3779) — `CreatePR`
should never be reached.

**The guard is fooled by a stale local base ref:**

- Every worktree (parent's and children's) is cut from **freshly fetched
  `origin/main`**: `git fetch origin main` + `git worktree add -B <branch>
  <path> origin/main` (`internal/executor/worktree.go:548,559,571`).
- The guard calls `CountNewCommits(ctx, baseBranch)` with `baseBranch` = a
  **local branch name** ("main"), running `git rev-list --count main..HEAD`
  (`internal/executor/git.go:870-883`).
- Nothing fast-forwards the shared clone's local `refs/heads/main` during a
  sequential epic run: `syncMainBranch` (`runner_git.go:30`) only fires on the
  explicit-dependency `wait_for_merge` path (`epic.go:2952,2976`), not the
  TASK-402 default for independent siblings (`epic.go:2984-3002`).
- So when local `main` lags `origin/main`, `main..HEAD` on the frozen-at-epic-
  start parent branch counts commits that are NOT parent work → guard sees
  nonzero → `CreatePR` fires on a branch whose origin-relative diff is zero →
  GitHub answers "No commits" → classified `infra`
  (`infraErrorSignatures`, `runner.go:232-252`).

Same stale-ref mechanism plausibly feeds the direct path's long-standing
"~10% of failures are No commits" class (GH-916 retry, `runner.go:3532-3719`)
and its pre-CreatePR guard (`runner.go:4306-4322`).

## Fix

**Core — compare against the ref the branch was actually cut from.**

1. Add an origin-relative variant (extend `CountNewCommits` or add
   `CountNewCommitsAgainstOrigin`): `git fetch origin <base>` (best-effort;
   on fetch failure fall back to the tracking ref as-is — `refs/remotes/
   origin/<base>` is still strictly fresher than local `<base>` here, since
   worktree creation fetched it), then `git rev-list --count
   origin/<base>..HEAD`.
2. Use it at the epic finalize guard (`runner.go:1666`) — this alone makes the
   empty-umbrella case take the intended clean skip/no-op path, preserving the
   GH-3779 classification (`evaluateEmptyBranchPRGuard`) exactly as today.
3. Mirror at the direct-path guards (`runner.go:~3536` and `~4297-4315`) —
   same mechanism, same fix; expected to shrink the GH-916 retry class.

**Backstop — a real "No commits" from GitHub is not infra on a shipped epic.**

4. At `finalizeEpicBranchPR`'s CreatePR-failure exit (`runner.go:1812-1824`):
   when the error matches `No commits between` AND the children's terminal
   states show shipped/no_op work, classify via `evaluateEmptyBranchPRGuard`
   (completed / no_op per child states) instead of failing `infra`; skip the
   failure alert. Keep the #4563 sweep call for genuinely non-terminal
   children. Defense-in-depth for any future way the guard is wrong.

## Acceptance criteria

- Unit test reproducing the false positive: repo where local `main` is N
  commits behind `origin/main`, branch cut from `origin/main` with zero own
  commits → old guard counts N (>0), new guard counts 0. (Simulate origin
  with a second bare repo / `git update-ref`; no network.)
- Epic finalize with empty umbrella branch and stale local main → no
  `CreatePR` call, outcome per child states (completed when children shipped;
  `no_op` when all children no-op'd — existing GH-3779 tests stay green).
- Backstop test: `CreatePR` returns GraphQL "No commits between…" with all
  children shipped → result completed, no infra classification, no alert.
- Direct-path guards: branch WITH real commits vs stale local main still
  proceeds to PR (count via origin ref is still >0) — no behavior change for
  legitimate content; empty-branch retry (GH-916) unaffected semantics.
- Fetch-failure fallback covered (offline → tracking ref comparison).
- `go test ./internal/executor/` green.

## Out of scope

- Removing `finalizeEpicBranchPR`/umbrella-PR flow entirely (candidate
  redesign; not needed once the guard is truthful).
- Child finalization on parent abort (#4563 shipped, #4564 refines).
- Epic issue-close paths (`epic.go:3006-3031`, `controller.go:3033`,
  `epic_reconcile.go:322`) — already correct, three independent closers.
- The historical GH-916 "~10%" number — revisit after this ships.

## Scope fence

`internal/executor/git.go`, `internal/executor/runner.go` (guard call sites +
CreatePR-failure exit), matching tests. Do not decompose: the three guard
sites share one helper and one mechanism; splitting them re-creates the
GH-4544 same-file-conflict failure.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4566
- Incidents: auth-service GH-431 + GH-435, 2026-07-26
- Research: navigator-research session 2026-07-27 (epic flow map; verdict:
  guard fooled by stale local ref; three close paths independent of umbrella PR)
- Prior art: TASK-356 (guard origin), GH-3779 (no_op classification), TASK-401
  (parent re-implementation closed), TASK-402 (independent-sibling dispatch —
  why syncMainBranch doesn't run), GH-916 (direct-path retry), #4563/#4564
  (child sweep)
- Post-merge follow-up: write pitfall memory
  `epic-no-commits-guard-stale-local-ref` once the fix is proven live
