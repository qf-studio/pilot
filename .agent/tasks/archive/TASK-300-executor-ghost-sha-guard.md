> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-300: Executor ghost-SHA guard — fail closed when commit_sha is already on base branch

**Wave:** 4 (S) · **Severity:** P0 (silent data loss — 1h10m of Pilot work vanished for #3090) · **Source:** ghost-close incident 2026-05-26 (Variant 4 in `bug_pilot_ghost_closes.md`)

---

## Problem

Executor harvests `commit_sha` from worktree HEAD at `internal/executor/runner.go:2158-2200`. When Claude finishes without making a new commit, `git log -1 --format=%H` returns the **parent commit** (the base SHA from before execution started). That SHA is then recorded in `executions.commit_sha`. `IsResultShipped` at `internal/executor/task_shipped.go:20-27` returns true on `commit_sha != ""`, so:

- `result.Success = true`
- `IsResultShipped(result) = true` (CommitSHA is non-empty, even though it's a parent SHA)
- Completion comment posts at `cmd/pilot/handlers.go:834`
- `pilot-done` label added, issue closed
- No branch on remote, no PR — work is permanently lost

**Concrete incident:** Issue #3090 (TASK-298), 2026-05-26 11:08 UTC. `executions.commit_sha = 84273ab8` — which is TASK-293's already-shipped commit on main.

## Approach

### Step 1 — SHA freshness check (~30 min)

New helper in `internal/executor/runner.go` (or a new `internal/executor/git_freshness.go`):

```go
// commitSHAIsNew returns true iff sha exists in the repo AND is NOT an ancestor of origin/<baseBranch>.
// A SHA already on the base branch is a parent SHA — proof the executor made no new commit.
func commitSHAIsNew(ctx context.Context, repoPath, sha, baseBranch string) (bool, error) {
    if sha == "" {
        return false, nil
    }
    // `git merge-base --is-ancestor SHA origin/BASE` exits 0 if SHA is ancestor of BASE.
    // We want it NOT to be — i.e., a new commit not yet merged.
    cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-base", "--is-ancestor", sha, "origin/"+baseBranch)
    err := cmd.Run()
    if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
        return true, nil // not ancestor — fresh commit
    }
    if err != nil {
        return false, fmt.Errorf("merge-base check: %w", err)
    }
    return false, nil // ancestor — parent SHA, not fresh
}
```

### Step 2 — Gate the SHA capture (~30 min)

`internal/executor/runner.go:2158-2200` (post-claude SHA harvest):

```go
result.CommitSHA = capturedSHA
isNew, _ := commitSHAIsNew(ctx, projectPath, capturedSHA, baseBranch)
if !isNew {
    slog.Warn("executor: harvested SHA is already on base branch — no new commit",
        "sha", capturedSHA, "base", baseBranch, "task", taskID)
    result.CommitSHA = "" // treat as no-op
    result.Success = false
    result.Error = "no new commit produced — worktree HEAD matches base branch parent"
}
```

Repeat after `git push` at runner.go:3082 (post-push SHA capture) — defense in depth.

### Step 3 — Tighten `IsResultShipped` predicate (~15 min)

`internal/executor/task_shipped.go:20-27`:

```go
// Before:
//   return row.CommitSHA != "" || row.PRUrl != ""
// After:
func IsTaskShipped(row Execution) bool {
    if row.Status != "completed" {
        return false
    }
    // PR URL is the strongest signal — a PR with that URL was created against the remote
    if row.PRUrl != "" {
        return true
    }
    // CommitSHA alone is insufficient (Variant 4: can be parent SHA).
    // But if we have a SHA AND status==completed AND error=="", trust it for backwards-compat
    // until autopilot is hardened. Log when this path is taken.
    if row.CommitSHA != "" && row.Error == "" {
        slog.Warn("IsTaskShipped: trusting CommitSHA without PRUrl — verify freshness",
            "sha", row.CommitSHA)
        return true
    }
    return false
}
```

(Or take the stricter path: require `PRUrl != ""`. Decision deferred until TASK-301 lands the autopilot-side hardening — see Out of scope.)

### Step 4 — Tests (~45 min)

- New `internal/executor/git_freshness_test.go`:
  - SHA on main → `commitSHAIsNew` returns false
  - SHA on local branch ahead of main → returns true
  - SHA that doesn't exist → returns error
  - Empty SHA → returns false
- New `internal/executor/task_shipped_test.go` additions (file exists from TASK-296):
  - Row with CommitSHA + Status=completed + no PR URL → should warn but return true (or false per Step 3 decision)
  - Row with parent-SHA equivalent: assert the upstream guard at runner.go prevents this row from being recorded as `completed`

### Step 5 — Backfill correction (~30 min)

One-shot SQL migration that marks affected legacy rows:

```sql
-- Find executions where commit_sha is already on origin/main — these are ghosts
-- (Cannot run automatically without git context; provide a manual `pilot db backfill-ghosts` CLI command instead)
```

Or document the detection query (already in `bug_pilot_ghost_closes.md` Variant 4) and let operators reconcile manually.

## Files to modify

- New: `internal/executor/git_freshness.go`
- New: `internal/executor/git_freshness_test.go`
- `internal/executor/runner.go` (SHA capture sites at lines 2158, 3082)
- `internal/executor/task_shipped.go` (Step 3 hardening)
- `internal/executor/task_shipped_test.go` (extend coverage)

## Test Strategy

- Unit: `commitSHAIsNew` table-driven against a temp git repo
- Integration: synthetic Claude run that produces no commit → assert `executions.status='failed'` not `'completed'`
- Manual: re-run #3090 against a worktree where Claude is forced to no-op (e.g. trivial issue)

## Effort

S (~2h total). One PR.

## Out of scope

- Hardening the autopilot-side `pilot-done` label timing — TASK-301
- Backfill of historical ghost executions in `~/.pilot/data/pilot.db` — operational, not code

## Recovery of #3090 work

This task does NOT recover the lost #3090 work. Open a fresh issue (TASK-298 redo) so Pilot's worker sees a new issue number not in its processed set.
