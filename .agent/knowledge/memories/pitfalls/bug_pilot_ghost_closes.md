---
name: Pilot ghost closes — issues marked done without actual work shipped
description: Pilot closes sub-issues or parent issues with pilot-done label when the PR was closed unmerged, when execution produced no diff, when the PR only touched task-tracking docs, when the executor harvested a parent SHA as proof of work, or when a post-creation rebase conflict killed the PR — creates false-positive "completed" state
type: project
---

Five observed variants of the same pattern: Pilot reports work as completed when it isn't. Dangerous because the issue tracker looks green and downstream decisions (release, deploy, trust the fix) assume the code actually shipped.

## Variant 1: Sub-issue closed when sibling PR merged (epic coordinator false-positive)

**Example (2026-04-07, pilot#2211):**
- Epic #2209 decomposed into 4 sub-issues: #2210 #2211 #2212 #2213
- PR #2214 (GH-2210) merged — client.go GraphQL methods shipped
- PR #2215 (GH-2211) **closed without merging** (`mergedAt: null`) — epic.go wiring NOT shipped
- GH-2211 was closed with `pilot-done` label and a comment: `"✅ Completed as part of GH-2209"`
- Result: `LinkSubIssue` defined in client.go but never called anywhere — dead code
- The #2209 epic appears green, but the actual bug fix (wire native linking into epic decomposition) is missing
- Detection: `grep -n LinkSubIssue internal/executor/epic.go` returns nothing

**Root cause (suspected):** Epic completion tracking trusts the CLOSED state of sub-issues instead of the MERGED state of their PRs. When PR #2215 was closed (for whatever reason — conflict, CI failure, autopilot giveup), the epic coordinator saw "sub-issue closed" and posted the fake completion comment.

## Variant 2: No-op execution marked done

**Example (2026-04-07, pilot#2176):**
- Issue #2176 picked up by Pilot after re-label
- Execution ran, produced no code changes
- Retry ran, produced no code changes
- Last comment: `"❌ Pilot execution failed: execution failed: Claude completed but made no code changes after retry"`
- Issue closed with **both** `pilot-done` AND `pilot-failed` labels (conflicting!)
- Result: tracker shows done, code unchanged, bug unfixed

**Root cause (suspected):** Notifier adds `pilot-failed` on execution failure, but some other code path (possibly parent-close coordinator or a retry handler) also adds `pilot-done` without checking whether a diff actually shipped.

## Variant 3: Ghost PR — task-doc-only commits

**Example (2026-04-07, auth-service#371 hygiene sweep decomposition):**
- Hygiene sweep epic decomposed into ~9 sub-issues including multiple fmt.Errorf sweep sub-issues
- **PR #407** (GH-404: "Sweep fmt.Errorf → %w in internal/dpop/, internal/...") — only change: `.agent/tasks/gh-404.md` (the task doc file)
- **PR #409** (GH-406: "Verify/clean stub packages, verify session test coverage") — only changes: `.agent/tasks/gh-406.md` + `.claude/settings.json`
- Both PRs were `MERGEABLE` and would have auto-merged, marking their sub-issues `pilot-done` — but zero production code was touched despite the titles
- Verified with `gh pr view N --json files --jq '[.files[].path]'` before the dispatcher race would have merged them
- Manually closed both PRs with explanation

**Root cause (suspected):** Pilot's executor writes a task-tracking doc to `.agent/tasks/GH-N.md` as part of its workflow. When Claude completes without making any actual code changes, the only diff is the task doc file itself, but the commit + PR creation logic doesn't detect "this diff contains only infrastructure files" and still ships. No-op execution slips through the "no commits" gate because there ARE commits — they just don't touch the thing the issue asked for.

**Detection:** The real tell is the PR's `.files[].path` only containing paths under `.agent/`, `.claude/`, or docs-only directories — nothing under `internal/`, `cmd/`, `pkg/`, or wherever production code lives.

## Variant 4: Ghost branch — parent SHA harvested as proof of work

**Example (2026-05-26, pilot#3090 — TASK-298):**
- Issue #3090 picked up by Pilot, executor ran 1h10m
- Pilot posted `"✅ Pilot completed! | Branch | pilot/GH-3090 |"` on the issue
- Issue closed with `pilot-done`
- Reality:
  - `git ls-remote origin "pilot/GH-3090*"` → **empty** (no remote branch)
  - `gh pr list --head pilot/GH-3090` → **empty** (no PR ever opened)
  - `executions` table: `task_id=GH-3090, status=completed, error='', commit_sha=84273ab8, pr_url=''`
  - **`84273ab8` is the SHA of an already-merged commit on main** (TASK-293 from a day earlier — `feat(metrics): add pilot_poller_skipped...`)
- The executor harvested the worktree's HEAD SHA, which equalled the parent commit on `main` because no new commit was ever created. `commit_sha != ""` made `IsTaskShipped` return true → completion comment fired → issue closed → 1h10m work vanished.

**Root cause:** `IsResultShipped`/`IsTaskShipped` at `internal/executor/task_shipped.go:20-27` returns true on `commit_sha != "" || pr_url != ""`. The predicate proves the executor *thinks* it shipped, not that the commit is *new*. A worktree HEAD SHA can be the parent SHA (no commit made) and still pass.

**Detection:**
```bash
# If the recorded commit_sha is already on origin/main, the work didn't ship
git fetch origin main
sqlite3 ~/.pilot/data/pilot.db \
  "SELECT task_id, commit_sha FROM executions WHERE task_id='GH-<N>'" \
  | while IFS='|' read task sha; do
      git merge-base --is-ancestor "$sha" origin/main && \
        echo "$task ghost — $sha already on main"
    done
```

## Variant 5: Post-creation rebase conflict closes PR but issue stays pilot-done

**Example (2026-05-26, pilot#3081 — TASK-297):**
- Issue #3081 picked up by Pilot, PR #3088 opened with real docs changes
- Executor finished `Success=true`, `commit_sha=47f509d07a`, `pr_url=#3088` → `pilot-done` label + issue closed at `cmd/pilot/handlers.go:354-382`
- Later, autopilot tried to auto-rebase #3088 against new main commits (`docs: sync version v2.150.0` then `v2.151.0` landed during execution, both touched same docs files)
- Rebase conflicted → `internal/autopilot/controller.go:1766-1806` (`handleMergeConflict`):
  - Line 1787: posts conflict comment
  - Line 1793: closes the **PR**
  - Line 1799: removes `pilot-in-progress` from **issue**
  - **Does NOT reopen issue, does NOT remove `pilot-done`**
- Reality: issue closed with `pilot-done`, PR closed without merge, no code on main

**Root cause:** `pilot-done` + issue closure fires eagerly at PR-creation time (`handlers.go:354-382`), not at merge time. The auto-rebase failure path only acts on the PR, not the issue. The "issue closed but code never shipped" state is the steady-state outcome.

**Detection:** Same as Variant 1 — check `gh pr list --search "GH-<N>" --state all --jq '.[] | select(.mergedAt != null)'`. If empty → ghost close even though issue says done.

**Aggravating factor:** Files commonly touched by both Pilot work and `docs-version-sync` workflow (`.agent/system/FEATURE-MATRIX.md`, `.agent/DEVELOPMENT-README.md`, `docs/lib/version.ts`) almost guarantee post-creation rebase conflicts when a release is cut mid-execution. Long-running tasks (>30 min) that touch these files are high-risk.

## How to detect ghost closes

Before trusting a "pilot-done" state, verify:

1. **Did a PR actually merge for this issue?**
   ```bash
   gh pr list --search "GH-<N> in:title" --state all --json number,state,mergedAt \
     --jq '.[] | select(.mergedAt != null) | .number'
   ```
   If empty → ghost close, reopen the issue.

2. **Are both `pilot-done` and `pilot-failed` set?**
   ```bash
   gh issue view <N> --json labels --jq '[.labels[].name] | contains(["pilot-done","pilot-failed"])'
   ```
   If true → conflicting state, investigate.

3. **Does the PR touch actual production code? (Variant 3 check)**
   ```bash
   gh pr view <PR> --json files --jq '[.files[].path] | map(select(test("^(internal|cmd|pkg)/"))) | length'
   ```
   If `0` → ghost PR. The only files touched are task docs, config, or similar non-production paths. The stated work did not ship.

4. **For sub-issue claims, grep the code for the expected artifact:**
   ```bash
   grep -rn '<ExpectedFunctionName>' internal/
   ```
   If the function the issue said it would add isn't anywhere in the codebase → ghost close.

## How to recover

1. Reopen the issue: `gh issue reopen <N>`
2. Strip stale labels: `gh issue edit <N> --remove-label pilot-done --remove-label pilot-failed`
3. Post a comment with explicit acceptance criteria that include a grep check ("verify `grep LinkSubIssue internal/executor/` returns a match")
4. Leave `pilot` label for re-dispatch

## Not yet filed as Pilot bugs

### Variant 1 fix direction
Epic completion tracking should only mark a sub-issue as "complete via parent epic" when its **associated PR merged**, not when the issue was closed. Check `internal/executor/epic.go` `ExecuteSubIssues` path + wherever `pilot-done` gets added post-epic. File a Pilot issue if this pattern recurs.

### Variant 2 fix direction
When the notifier receives a failure with "no code changes after retry", it should add `pilot-failed` exclusively — no code path should subsequently add `pilot-done` without first verifying that a PR was created and merged. File a Pilot bug once the notifier's `pilot-done` add-path is traced.

### Variant 3 fix direction
PR creation logic should detect "diff contains only infrastructure files" (task docs under `.agent/`, `.claude/settings.json`, etc.) and either fail the execution or mark it as `pilot-failed` with a "no production code changes" reason. Currently Pilot's "no commits" gate only checks `git diff` emptiness, not whether the diff is meaningful. Candidate location: wherever the executor decides to run `gh pr create` — add a gate that requires at least one file in the diff under production paths (configurable whitelist of path prefixes). File once we've verified the exact location in `internal/executor/` that creates the PR.

### Variant 4 fix direction
The no-commit guard at `internal/executor/runner.go:3038` checks `CountNewCommits == 0` but the SHA-harvest path at runner.go:2158-2200 falls back to `git log -1 --format=%H` which returns the worktree's HEAD — which equals the parent SHA when no new commit exists in the worktree's branch. Fix: before reporting `commit_sha`, require `git merge-base --is-ancestor <sha> origin/<base_branch>` to return **non-zero** (i.e., the SHA is NOT yet on the base branch). If it IS on the base branch, treat as no-op and fail with `pilot-failed`. Apply at runner.go:2158 (post-claude SHA capture) AND runner.go:3082 (post-push SHA capture). Companion change: tighten `IsResultShipped` at `task_shipped.go:20` to require `pr_url != ""` (CommitSHA alone is insufficient — it can be a parent SHA).

### Variant 5 fix direction
The auto-rebase-fail path at `internal/autopilot/controller.go:1766-1806` (`handleMergeConflict`) closes the PR without reverting the issue's `pilot-done` label or reopening the issue. Fix options:
1. **Conservative**: in `handleMergeConflict`, add `RemoveLabel(issue, "pilot-done") + AddLabel(issue, "pilot")` + `ReopenIssue(issue)` after closing the PR, so Pilot re-picks the task against current main.
2. **Better**: move the `pilot-done` add + issue close from `handlers.go:354-382` (PR-creation time) to autopilot's merge-success handler (`controller.go` `handleMergeSuccess` or equivalent). Then no inversion is needed because `pilot-done` only fires on actual merge. This is the structurally correct fix and aligns the *worker* layer with the *executor* layer hardening done in TASK-296.
Files commonly conflicting with docs-version-sync should also get pre-execution lock advisory: don't start docs-touching tasks while a release sync is pending.
