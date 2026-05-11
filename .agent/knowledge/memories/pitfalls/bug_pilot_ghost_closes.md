---
name: Pilot ghost closes — issues marked done without actual work shipped
description: Pilot closes sub-issues or parent issues with pilot-done label when the PR was closed unmerged, when execution produced no diff, or when the PR only touched task-tracking docs — creates false-positive "completed" state
type: project
---

Three observed variants of the same pattern: Pilot reports work as completed when it isn't. Dangerous because the issue tracker looks green and downstream decisions (release, deploy, trust the fix) assume the code actually shipped.

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
