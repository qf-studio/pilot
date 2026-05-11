# TASK-38: Title Auto-Rewrite Fallback (P1 of Sonnet success-rate plan)

**Status**: 🚧 In Progress
**Created**: 2026-05-06
**Assignee**: Pilot

---

## Context

5 of 25 Sonnet 4.6 failures (20%) are `PR creation refused: title is not a conventional commit: could not auto-correct ...`. Today `normalizeTitle()` (`internal/executor/title.go:87-98`) returns an error and the executor classifies the run as a permanent failure — even though we have all the info needed (the diff + labels) to generate a valid title.

Goal: extend `normalizeTitle()` with a diff-based fallback so a non-conventional title auto-rewrites instead of failing the run. Recover ~5 wins (~3.5pp success-rate bump).

This is P1 of the larger plan in `.agent/.context-markers/.active`-adjacent doc `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md`.

## Success Criteria

- [ ] `normalizeTitle()` returns a valid conventional-commit title for any input, never an error (when called with a non-empty title and access to a diff)
- [ ] New helper `inferConventionalPrefix(diff GitDiff, labels []string) string` chooses prefix from file paths and net line counts
- [ ] Title-related entries removed from `permanentFailurePatterns` (`internal/executor/runner.go:27-31`)
- [ ] All existing tests pass; new table-driven tests cover all heuristic branches
- [ ] No regression — titles that already conform still pass through unchanged

---

## Implementation Plan

### Phase 1: Diff-based prefix heuristic

Add `inferConventionalPrefix(diff, labels) string` near `autoPrefixTitle()` in `title.go`.

Heuristic rules (apply in order, first match wins):

1. All changed files match `*.md` or `*.mdx` → `docs`
2. All changed files match `*_test.go` or `*_test.ts` or `*.test.*` → `test`
3. Any label in `labelPrefixMap` matches → use that (delegate to `autoPrefixTitle`)
4. Files only under `.github/workflows/` or `.gitlab-ci.yml` → `ci`
5. Files only under `Dockerfile`, `Makefile`, `go.mod`, `go.sum`, `package.json`, `package-lock.json` → `build`
6. Net diff added > removed by ≥2x AND any code file changed → `feat`
7. Net diff close to zero (`abs(added - removed) / total < 0.2`) AND code file changed → `refactor`
8. Otherwise → `chore`

### Phase 2: Wire into `normalizeTitle()`

Modify `normalizeTitle()` to accept a third arg (or use a builder/options pattern) for the diff. Cleanest: change signature to `normalizeTitle(title string, labels []string, diff GitDiff) (string, error)`.

- If `validatePRTitle(title)` passes → return as-is
- Else if `autoPrefixTitle(title, labels)` returns valid → return that (existing behavior)
- Else `prefix := inferConventionalPrefix(diff, labels); return prefix + ": " + truncated_subject, nil`
- Subject normalization: strip leading `GH-NNNN: `, truncate to 72 chars, lowercase first word
- Only return error for truly unrecoverable cases (empty title, no diff)

### Phase 3: Update callers

`internal/executor/runner.go:2888` is the single call site. It already has access to git and the working tree — pass a `GitDiff` snapshot computed from `git diff --numstat origin/main...HEAD`.

If a `GitDiff` type doesn't exist, define a minimal one:

```go
type GitDiff struct {
    Files       []GitDiffFile
    LinesAdded  int
    LinesRemoved int
}

type GitDiffFile struct {
    Path         string
    LinesAdded   int
    LinesRemoved int
}
```

Add `git.GetDiffStats(ctx, baseBranch) (GitDiff, error)` in `internal/executor/git.go` if not present.

### Phase 4: Drop title patterns from permanent-failure list

`internal/executor/runner.go:27-31` — remove:
- `"title is not a conventional commit"`
- `"could not auto-correct"`

Keep `"PR creation refused"` (broader umbrella for other refusal cases).

### Phase 5: Tests

`internal/executor/title_test.go` — new cases:

- `inferConventionalPrefix`: docs-only, test-only, ci-only, build-only, feat (heavy additions), refactor (net-zero), chore (default)
- `normalizeTitle` with diff: valid title (passthrough), label-only path (existing behavior preserved), label-miss + diff-based fallback (new), truncation of long subject
- Regression test: empty title still errors

`internal/executor/git_test.go` (or wherever `git.go` tests live) — `GetDiffStats` smoke test if added.

`internal/executor/runner_test.go` — `IsPermanentFailure` no longer matches title-related strings.

---

## Out of Scope

- Changes to the `validatePRTitle` regex
- Removing `IsPermanentFailure` mechanism — only its title entries
- Smarter subject summarization (LLM-generated subject) — heuristic is enough for P1
- Touching the no-commits guard or no-changes retry (those are P2/P3)

---

## Verify

```bash
make test ./internal/executor/...
make lint
make build

# After deploy: file a Pilot issue with a deliberately non-conventional title
# (no `feat:`/`fix:`/etc prefix; no helpful labels) and confirm Pilot
# auto-rewrites and ships rather than failing
```

Post-merge metric:

```sql
SELECT COUNT(*) FROM executions
WHERE model_name='claude-sonnet-4-6'
  AND created_at > '<merge-date>'
  AND error LIKE '%conventional commit%';
-- Expect: 0
```

---

## Done

- [ ] `inferConventionalPrefix` implemented with heuristic table
- [ ] `normalizeTitle` extended; signature change propagated
- [ ] `git.GetDiffStats` exists (existing or new)
- [ ] Title patterns dropped from `permanentFailurePatterns`
- [ ] All test branches covered
- [ ] PR opened, CI green, approved, auto-merged, auto-released

---

## Notes

- Single Pilot issue. ~80 LoC including tests.
- Smallest, lowest-risk item in the success-rate plan; chosen to ship first to confirm `pattern_burst_auto_release_starvation.md` fix is holding under normal cadence.
- Plan reference: `~/.claude/plans/use-nav-research-prepare-delegated-dragon.md`

---

**Last Updated**: 2026-05-06
