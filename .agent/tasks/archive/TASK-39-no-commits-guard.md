# TASK-39: No-Commits-to-PR Guard (P2 of Sonnet success-rate plan)

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

4 of 25 Sonnet 4.6 failures (16%) are `PR creation failed: ... GraphQL: No commits between main and <branch>`. The executor calls `git.CreatePR()` (which shells out to `gh pr create`) without first checking that the branch contains commits vs the base. The gh CLI errors out and the run fails.

Today's existing "no-changes" retry logic at `internal/executor/runner.go:2162-2188` already does the right thing — it detects empty branches via `git.CountNewCommits()` and re-prompts Claude — but that check only runs in one specific code path. Other paths reach `git.CreatePR` (line 2951) without the guard, so an empty branch leaks through to gh CLI.

Goal: ensure every path to `git.CreatePR` is gated by `git.CountNewCommits() > 0`, and reuse the existing retry block when zero. Recovers ~4 wins (~2.8pp success-rate bump).

This is P2 of `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md`. P1 (TASK-38, v2.130.0) shipped 2026-05-06.

## Success Criteria

- [ ] Pre-`git.CreatePR` guard verifies the branch has commits relative to the base
- [ ] When commits == 0, the existing retry block (runner.go:2162-2253) is invoked instead of attempting PR creation
- [ ] No regression on the existing retry path
- [ ] New test exercising the empty-branch case
- [ ] `make test` / `make lint` / `make build` pass

---

## Implementation Plan

### Phase 1: Locate every call to `git.CreatePR`

Single call site is at `internal/executor/runner.go:2951` for the GitHub-default path. The non-GitHub adapter path uses `r.prCreator.CreatePR(...)` at line 2939.

Confirm during implementation:
```bash
grep -rn "\.CreatePR(\|prCreator.CreatePR" internal/executor/ --include="*.go" | grep -v _test.go
```

### Phase 2: Insert guard before PR create

In `runner.go`, between the post-push step (line 2838) and the title-validation/PR-create block (lines 2888-2951), insert:

```go
commitCount, countErr := git.CountNewCommits(ctx, baseBranch)
if countErr != nil {
    log.Warn("count commits before PR create failed", ...)
} else if commitCount == 0 {
    // Reuse existing retry block (runner.go:2162-2253).
    // Refactor that block into a helper `retryNoChanges(...)` if needed
    // and call it here.
    ...
}
```

If the lines 2162-2253 block is in-line and not currently a helper, factor it into a method:

```go
func (r *Runner) retryNoChanges(
    ctx context.Context,
    log *slog.Logger,
    task *Task,
    state *executionState,
    backendResult *BackendResult,
    baseBranch string,
    selectedModel, watchdogTimeout, ...
) (commitCount int, retryResult *BackendResult, retryErr error)
```

This keeps the new guard's body small (call → check → return-or-continue) and removes duplication.

### Phase 3: Tests

New test in `runner_test.go`:

- `TestRunner_PRCreate_EmptyBranch_TriggersRetry`:
  - Mock backend returns success but no diff
  - Mock `git.CountNewCommits` returns 0
  - Assert: `git.CreatePR` is NOT called; retry path fires
  - Assert: error message classifies as `no_changes` if retry also fails

- `TestRunner_PRCreate_HasCommits_ProceedsToCreate`:
  - Mock returns 1+ commits
  - Assert: `git.CreatePR` IS called

If the existing tests already mock the backend at this layer, reuse the same scaffolding. If they don't, add the minimum mock.

---

## Out of Scope

- Schema changes
- Touching the retry-prompt content (P3 will rewrite it)
- Pre-flight intent judge (P4)
- Effort-routing wiring (P5)
- New error classifications

---

## Verify

```bash
make test ./internal/executor/...
make lint
make build
```

Post-merge metric:

```sql
SELECT COUNT(*) FROM executions
WHERE model_name='claude-sonnet-4-6'
  AND created_at > '<merge-date>'
  AND error LIKE '%No commits between%';
-- Expect: 0
```

---

## Done

- [ ] Guard placed before every `CreatePR` call (GitHub path + adapter path)
- [ ] Existing retry block reused via helper or in-line check
- [ ] New tests pass
- [ ] PR opened, CI green, approved, auto-merged, auto-released

---

## Notes

- Single Pilot issue. ~40 LoC including tests.
- Reuses `git.CountNewCommits()` (already invoked at line 2162) and the existing retry block (lines 2162-2253).
- Per `pattern_burst_auto_release_starvation.md`: ship alone after P1 has merged + auto-released.
- Plan reference: `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md` (P2 of 5)

---

**Last Updated**: 2026-05-06
