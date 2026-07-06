> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-301: Gate `pilot-done` + issue close on PR merge, not PR creation

**Wave:** 4 (M) · **Severity:** P1 (silent data loss when post-creation rebase fails) · **Source:** ghost-close incident 2026-05-26 (Variant 5 in `bug_pilot_ghost_closes.md`)

---

## Problem

`pilot-done` label is added and issue is closed at `cmd/pilot/handlers.go:354-382` when `IsResultShipped(result)` returns true at PR-creation time. This fires AS SOON AS a PR URL is captured — long before merge.

When autopilot later detects a merge conflict (`internal/autopilot/controller.go:1766-1806` `handleMergeConflict`), it closes the PR but:
- Line 1799: only removes `pilot-in-progress` from the issue
- Does NOT remove `pilot-done`
- Does NOT reopen the issue
- Does NOT re-add `pilot` for re-dispatch

Steady-state outcome: issue closed with `pilot-done`, PR closed without merge, code never on main. Pilot's worker won't re-pick because the issue is in CLOSED+pilot-done state.

**Concrete incident:** Issue #3081 (TASK-297), 2026-05-26. PR #3088 opened, post-creation rebase against fresh `docs: sync v2.150.0` + `v2.151.0` commits conflicted on `.agent/system/FEATURE-MATRIX.md`. PR closed by autopilot with comment "Merge conflict detected. Auto-rebase failed — closing PR so the issue can be re-executed from updated main." But the issue was already closed+`pilot-done`, so Pilot never re-picked it.

## Approach

### Option A (preferred) — Move `pilot-done` to merge-success handler

The structurally correct fix.

1. **Remove premature add at `cmd/pilot/handlers.go:354-382`** — keep only `pilot-in-progress` removal there
2. **Add `pilot-done` + issue close in autopilot's merge-success path** — locate the existing merge handler in `internal/autopilot/controller.go` (search for `handleMergeSuccess` or wherever `removePR` is called on a successfully-merged PR)
3. **On merge-conflict path (`handleMergeConflict`)** — issue stays open, `pilot` label preserved for re-dispatch
4. **Edge case**: when running with `--no-autopilot` or in CI mode without autopilot, the merge gate doesn't fire. Add a manual close path: `pilot close <issue> --reason merged` for operators, OR have `handlers.go` add `pilot-done` only when both `IsResultShipped && !autopilotEnabled` (legacy behavior preserved).

### Option B (conservative) — Reverse the close in conflict path

Less invasive but leaves the underlying race intact.

In `internal/autopilot/controller.go:1799` (`handleMergeConflict`):

```go
gh.RemoveLabel(issue, "pilot-in-progress")
// NEW:
gh.RemoveLabel(issue, "pilot-done")
gh.AddLabel(issue, "pilot")
gh.ReopenIssue(issue) // only if issue.State == "closed"
gh.AddComment(issue, fmt.Sprintf("Re-opening for re-execution after auto-rebase failure on PR #%d. Rebase target: current main HEAD.", pr.Number))
```

### Step-by-step (assuming Option A)

1. **Research (~30 min)**: find the merge-success handler. Grep `internal/autopilot/controller.go` for `MergedAt`, `IsMerged`, `removePR`. Identify where successful merges are observed in the poll loop.
2. **Move label/close logic (~60 min)**: extract a helper `markIssueShipped(issue, prURL)` that adds `pilot-done` + closes the issue. Call from merge-success handler. Delete the eager call in `handlers.go:354-382`.
3. **Update conflict path (~30 min)**: `handleMergeConflict` keeps `pilot` label on issue, re-adds it if missing.
4. **Tests (~90 min)**:
   - Unit: `markIssueShipped` adds label + closes (mock GitHub client)
   - Integration: simulate PR-open → conflict-detected → assert issue stays open with `pilot` label
   - Integration: simulate PR-open → merged → assert issue closes with `pilot-done`
5. **Backfill (~15 min)**: document `bin/pilot ghost-recovery` operator command or manual gh script for existing ghost-closed issues (already present in `bug_pilot_ghost_closes.md` "How to recover" section).

### Coordination with docs-version-sync

Files commonly conflicting with `docs-version-sync` workflow are guaranteed conflict surfaces for long-running tasks. Add a pre-execution advisory at `internal/executor/runner.go` (BuildPrompt phase): if the task plan mentions any of `.agent/system/FEATURE-MATRIX.md`, `.agent/DEVELOPMENT-README.md`, `docs/lib/version.ts`, and a release is pending (check `gh workflow list --workflow=docs-version-sync.yml`), warn or defer.

(This third item is the smallest of the three but the highest-leverage for preventing TASK-297-style conflicts in the future.)

## Files to modify

- `cmd/pilot/handlers.go` (remove eager close at 354-382, keep `pilot-in-progress` toggle)
- `internal/autopilot/controller.go` (merge-success path adds `pilot-done` + close; merge-conflict path preserves `pilot` label)
- New: `internal/autopilot/controller_merge_test.go` (or extend existing test file)

## Test Strategy

- Unit + integration as above
- Manual: open a PR, force a conflict (rebase main), observe issue stays open and re-picks

## Effort

M (~4h total). One PR.

## Out of scope

- Backfill of existing ghost-closed issues — operational
- Versioning the `pilot-done` semantic (e.g. add `pilot-ghost-recovered` label) — separate task if needed
- Per-task-file lock against docs-version-sync — separate enhancement
