# TASK-283: Fix destructive `syncMainBranch` reset that wipes Pilot's local commits

**Status**: 🚧 Queued
**Created**: 2026-05-20
**Assignee**: Manual (no `pilot` label — needs human design review before execution)

---

## Context

**Problem**: `syncMainBranch()` uses `git reset --hard origin/main` after every task. When Pilot's commit hasn't yet been pushed at the moment sync runs, the reset rewinds local `main` to the *old* origin SHA — silently discarding Pilot's just-committed work.

**Where it broke**: `qf-studio/gitnation-companion-pilot` workshop demo on 2026-05-20. Reflog evidence:

```
7ca8846 HEAD@{0}: reset: moving to origin/main      ← sync wiped Pilot's commit
51e7422 HEAD@{1}: commit: feat(tooling): M1.1 …     ← Pilot's local commit
7ca8846 HEAD@{2}: reset: moving to 7ca8846
f9a287f HEAD@{3}: commit: feat(deploy): CI gate …   ← also wiped
```

Two of Pilot's commits were silently destroyed. User had to manually intervene to recover state from the remote (where one squashed version eventually landed).

**Why this is severe**: `git reset --hard` is an unconditional destructive operation. There's no divergence check, no "is local ahead of remote?" guard, no `--ff-only` semantics. If origin hasn't caught up yet for any reason (push retry, network blip, race with the push step), local commits are unrecoverable except via `git reflog` (which the user has 90 days to find).

## Goal

Make post-task main sync **non-destructive**: only fast-forward, never rewind. If local is ahead of or diverged from origin, log and skip the sync — don't reset.

## Success Criteria

- [ ] `syncMainBranch()` never loses local commits
- [ ] When local is behind origin (the common case), sync still fast-forwards
- [ ] When local has commits not on origin (the bug case), sync logs a warning and exits cleanly
- [ ] When local has diverged (commits on both sides), sync logs a warning and exits cleanly
- [ ] Add unit tests covering all three states (behind, ahead, diverged)
- [ ] Existing reset-based test (if any) replaced

---

## Implementation Plan

### Phase 1: Replace `reset --hard` with merge `--ff-only` + master-branch fix

**File**: `internal/executor/runner_git.go:22-56`

**Current code (lines 27-50)**:
```go
// Fetch latest from origin
fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "main")
fetchCmd.Dir = repoPath
if output, err := fetchCmd.CombinedOutput(); err != nil {
    return fmt.Errorf("failed to fetch origin/main: %w: %s", err, output)
}

// Check current branch
branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
...
currentBranch := strings.TrimSpace(string(branchOutput))

if currentBranch == "main" || currentBranch == "master" {
    resetCmd := exec.CommandContext(ctx, "git", "reset", "--hard", "origin/main")
    resetCmd.Dir = repoPath
    if output, err := resetCmd.CombinedOutput(); err != nil {
        return fmt.Errorf("failed to reset main to origin/main: %w: %s", err, output)
    }
    log.Info("Synced main branch with origin/main")
}
```

**New code (reorder: detect branch first, then fetch/merge using `currentBranch`)**:
```go
// Check current branch FIRST so we can fetch/merge the right ref
branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
branchCmd.Dir = repoPath
branchOutput, err := branchCmd.Output()
if err != nil {
    return fmt.Errorf("failed to get current branch: %w", err)
}
currentBranch := strings.TrimSpace(string(branchOutput))

// Only sync if on main/master (don't disrupt feature branches or worktrees)
if currentBranch != "main" && currentBranch != "master" {
    log.Debug("Not on main branch, skipping sync", slog.String("branch", currentBranch))
    return nil
}

remoteRef := "origin/" + currentBranch

// Fetch the matching remote ref
fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", currentBranch)
fetchCmd.Dir = repoPath
if output, err := fetchCmd.CombinedOutput(); err != nil {
    return fmt.Errorf("failed to fetch %s: %w: %s", remoteRef, err, output)
}

// GH-3018: Use --ff-only to prevent silent commit loss when local is ahead.
// reset --hard would wipe Pilot's just-committed work when push propagation lags.
mergeCmd := exec.CommandContext(ctx, "git", "merge", "--ff-only", remoteRef)
mergeCmd.Dir = repoPath
output, err := mergeCmd.CombinedOutput()
if err != nil {
    // ff-only fails on:
    //   (a) local ahead of origin (the bug case — push hasn't propagated yet)
    //   (b) divergence (local has commits origin doesn't, and vice versa)
    //   (c) dirty working tree ("Your local changes would be overwritten")
    // All three are safe to skip. Log raw git output so operators can distinguish.
    log.Warn("Skipped main sync — fast-forward not possible (non-fatal)",
        slog.String("branch", currentBranch),
        slog.String("remote_ref", remoteRef),
        slog.String("git_output", strings.TrimSpace(string(output))),
        slog.String("hint", "To sync manually: cd <repo> && git fetch && git merge --ff-only "+remoteRef),
    )
    return nil // non-fatal — sync is hygiene, not correctness
}
log.Info("Synced main branch with origin", slog.String("branch", currentBranch))
return nil
```

**Files**:
- `internal/executor/runner_git.go` — patch as above (full function rewrite, lines 22-56)
- `internal/executor/runner_git_test.go` — new test file with table-driven test for behind/ahead/diverged/dirty

### Phase 2: Add observability

- Warning log is at `Warn` level with structured fields: `branch`, `remote_ref`, `git_output`, `hint`
- `git_output` field preserves the raw git error so operators can distinguish local-ahead vs. divergence vs. dirty-tree
- Manual-recovery hint embedded in the log so users don't need to consult docs

### Phase 3: Document the trap

- Add SOP: `.agent/sops/git/never-reset-hard-in-automated-flows.md`
- Cross-link from the runner_git.go function comment
- Add pitfall memory: `.agent/knowledge/memories/pitfalls/pitfall_git_reset_hard_in_automated_sync.md`
- Add safety comment at `worktree.go:349` clarifying that `reset --hard` there is safe (isolated pooled worktree)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Sync mechanism | `reset --hard` / `merge --ff-only` / `pull --ff-only` | `merge --ff-only` | Pure-local op (origin already fetched), safe on local-ahead, no surprise when origin is missing |
| Failure behavior | Return error / warn + continue | Warn + continue | Sync is hygiene, not correctness. Failing the task because hygiene step couldn't run is wrong |
| Pre-check | None / `git status` / dirty-tree check | None | `merge --ff-only` itself catches all bad states (local-ahead, divergence, dirty tree); pre-check would be redundant. Raw git output in the warning log lets operators distinguish the cases. |
| Push-then-sync ordering | Reorder so push always happens before sync / fix sync to be non-destructive | Fix sync | The actual race is **GitHub propagation latency** (push succeeded locally, but `origin/main` hasn't updated yet on github.com), not push failure. Reordering does not help; sync must be safe regardless of propagation timing. |
| Branch handling | Hardcode `origin/main` / use `"origin/" + currentBranch` | Use currentBranch | Today's code hardcodes `origin/main` even for `master` repos, so the `master` branch is broken at the `fetch` step (line 27) before the reset runs. The fix should detect branch first, then fetch/merge `origin/<currentBranch>`. |

---

## Dependencies

**Requires**:
- Repo access to `internal/executor/runner_git.go`
- Existing test infrastructure for `Runner` (none needed if we add a focused unit test)

**Blocks**:
- Any user running Pilot on a fresh project where origin isn't pre-populated
- Future autopilot reliability work — silent data loss is a credibility killer

---

## Verify

### Test setup (important)

`syncMainBranch` calls `git fetch origin <branch>`, so a single `git init` tmpdir won't work — the test needs a **fetchable remote**. Use a two-repo pattern:

```go
// Bare "origin" repo
originDir := t.TempDir()
exec.Command("git", "init", "--bare", originDir).Run()

// Local clone with that remote
localDir := t.TempDir()
exec.Command("git", "clone", originDir, localDir).Run()
// configure user.email / user.name in localDir
// create initial commit, push to origin
```

The existing `git_test.go` only shows single-repo setup — this file needs the bare-origin pattern. Build minimal `Runner` with just `r.log = slog.Default()` since `syncMainBranch` only uses `r.log`.

Table-driven cases to cover:
1. **behind** — origin has a commit local doesn't → expect fast-forward, local matches origin
2. **ahead** — local has a commit origin doesn't → expect warning, **local commit preserved** (regression test for the bug)
3. **diverged** — both have unique commits → expect warning, no destructive merge
4. **dirty** — local has uncommitted changes + origin has new commit → expect warning, changes preserved
5. **feature branch** — current branch is `feat/x` → expect skip-debug, no fetch/merge
6. **master repo** — current branch is `master` → expect successful fast-forward (regression test for `origin/main` hardcode)

```bash
# Unit tests
go test ./internal/executor/ -run TestSyncMainBranch -v

# Lint + build
make fmt && make lint && make build
```

---

## Done

- [ ] `runner_git.go:syncMainBranch` uses `merge --ff-only` with `"origin/" + currentBranch`
- [ ] Branch detection moved before fetch so master repos work
- [ ] Six unit tests pass: behind, ahead, diverged, dirty, feature-branch, master
- [ ] Warning log includes raw git output + manual-recovery hint
- [ ] `make lint && make test && make build` clean
- [ ] SOP file committed at `.agent/sops/git/never-reset-hard-in-automated-flows.md`
- [ ] Pitfall memory committed at `.agent/knowledge/memories/pitfalls/pitfall_git_reset_hard_in_automated_sync.md` + indexed in `graph.json`
- [ ] Safety comment added at `worktree.go:349` clarifying the isolated-worktree reset is intentional
- [ ] Demoable: re-run the 2026-05-20 workshop scenario locally, confirm no commit loss

---

## Notes

- Don't add a `pilot` label to the GitHub issue. This needs human design review (sync semantics decisions) before Pilot executes.
- The reflog evidence in the Context section is the strongest debugging artifact — preserve it in the GitHub issue body.
- **Two call sites, only one is config-gated**:
  - `runner.go:3258` (post-task) — gated by `executor.sync_main_after_task` config flag
  - `epic.go:1498` (between sequential sub-issues) — **NOT gated**, fires unconditionally
  - Patching `syncMainBranch()` fixes both sites at once. But it means users who disabled `sync_main_after_task` to dodge the bug were **still exposed** during sequential epic execution — important severity context.
- **Race is GitHub propagation latency, not push failure**. `PushToMain` returning success only means the push *reached* GitHub; `origin/main` ref propagation can lag behind. Both `DirectCommit` and `CreatePR` modes have early-return on push failure, so push failure was never the trigger.
- **`worktree.go:349` uses `reset --hard` intentionally** — pooled worktrees are isolated tmp dirs that are discarded after use. Add a code comment there during this PR so future security reviewers don't flag it as the same bug.

---

**Last Updated**: 2026-05-20
