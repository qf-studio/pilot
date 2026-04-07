# TASK-06: Merged PR Guard — Prevent Retry of Completed Work

**Status**: ✅ Completed
**Created**: 2026-03-04
**Completed**: 2026-03-04
**Priority**: P1
**PRs**: #1985 (closed, lint fail), #1987 (merged, CI fix)

---

## Context

**Problem**:
When `pilot-failed` is removed from an issue, the poller clears ProcessedStore and re-dispatches it — even if merged PRs already exist for that issue. This causes:
1. Decomposition into duplicate sub-issues
2. Execution against already-merged code (no changes possible)
3. "No commits" failure → `pilot-failed` re-added → infinite retry loop

**Incident**: GH-1944 had 3 merged PRs (#1973, #1975, #1977). On retry, Pilot decomposed it into GH-1978/1979 (duplicates), failed on empty push, looped.

**Goal**:
Before dispatching any issue, check if merged PRs already exist for it. If so, mark as `pilot-done` and skip.

**Success Criteria**:
- [ ] Poller skips issues with merged PRs (both parallel and sequential paths)
- [ ] Issue gets `pilot-done` label when skipped due to merged PRs
- [ ] No API rate impact (single search query per candidate, cached)
- [ ] Table-driven tests covering: no PRs, open PRs only, merged PRs exist

---

## Implementation Plan

### Phase 1: Add `HasMergedPRs` to GitHub Client

**Goal**: Query GitHub Search API for merged PRs referencing an issue number.

**Tasks**:
- [ ] Add `SearchMergedPRsForIssue(ctx, owner, repo, issueNumber) (bool, error)` to `client.go`
- [ ] Use GitHub Search API: `GET /search/issues?q=repo:{owner}/{repo}+is:pr+is:merged+GH-{number}+in:title`
- [ ] Return `true` if `total_count > 0`
- [ ] Add table-driven test with httptest mock

**Files**:
- `internal/adapters/github/client.go` — new method (~20 lines)
- `internal/adapters/github/client_test.go` — test cases

### Phase 2: Add Guard to Poller

**Goal**: Insert merged PR check in both polling paths before dispatching.

**Tasks**:
- [ ] Add `hasMergedWork(ctx, issue) bool` helper on Poller
- [ ] Call `p.client.SearchMergedPRsForIssue()` inside it
- [ ] Insert check in `checkForNewIssues()` (line ~673, after ProcessedStore clear, before appending to candidates)
- [ ] Insert identical check in `findOldestUnprocessedIssue()` (line ~516, same position)
- [ ] When merged PRs found: log, add `pilot-done` label, mark processed, skip
- [ ] Add table-driven tests for both paths

**Files**:
- `internal/adapters/github/poller.go` — `hasMergedWork()` + 2 insertion points (~30 lines)
- `internal/adapters/github/poller_test.go` — test cases

**Insertion point (parallel path, `checkForNewIssues`)**:
```go
// After line 673 (ProcessedStore clear block), before line 675 (dependency check):
if p.hasMergedWork(ctx, issue) {
    continue
}
```

**Insertion point (sequential path, `findOldestUnprocessedIssue`)**:
```go
// After line 516 (ProcessedStore clear block), before line 518 (append):
if p.hasMergedWork(ctx, issue) {
    continue
}
```

### Phase 3: Defense-in-Depth — Runner Early Exit (Optional)

**Goal**: Second guard in runner for cases where poller check is bypassed (e.g., manual dispatch).

**Tasks**:
- [ ] In `runner.executeWithOptions()` (~line 1179, branch handling), if branch exists on remote AND has merged PR, abort with clear message
- [ ] Not critical — Phase 1+2 cover the primary path

**Files**:
- `internal/executor/runner.go` — optional guard (~10 lines)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to check | Runner, Poller, Handlers | Poller | Catches it earliest, prevents all downstream waste (decomposition, execution) |
| API method | `ListPullRequests` (existing) + filter, Search API | Search API | `ListPullRequests` returns all PRs (expensive). Search API filters server-side by issue number in title. Single query. |
| Search query | Body search, title search, branch name | Title search (`GH-{number}`) | Pilot branch naming is `pilot/GH-{number}` and PR titles include `GH-{number}`. Title search is reliable. |
| Rate limiting | Cache results, no cache | No cache needed | Check only runs on retry (label removal), not every poll cycle. Low frequency. |

---

## Dependencies

**Requires**:
- [ ] Existing `Client.doRequest()` for GitHub API calls
- [ ] GitHub Search API access (already available via PAT)

**Blocks**:
- [ ] None

---

## Verify

```bash
# Run affected tests
go test ./internal/adapters/github/... -run TestSearchMergedPRs -v
go test ./internal/adapters/github/... -run TestHasMergedWork -v

# Run full test suite
make test

# Lint
make lint
```

---

## Done

- [ ] `SearchMergedPRsForIssue()` method exists on GitHub client
- [ ] Both poller paths check for merged PRs before dispatching
- [ ] Issues with merged PRs get `pilot-done` label and are skipped
- [ ] Table-driven tests pass for all scenarios
- [ ] `make test && make lint` pass

---

## GitHub Issue Body

```markdown
## TASK-06: Merged PR Guard — Prevent Retry of Completed Work

**Priority**: P1

### Problem

When `pilot-failed` is removed from an issue, the poller clears ProcessedStore and re-dispatches — even when merged PRs already exist. This causes:
1. Decomposition into duplicate sub-issues
2. Execution against already-merged code (no changes)
3. "No commits" failure → `pilot-failed` → infinite retry loop

**Incident**: GH-1944 had 3 merged PRs. On retry, Pilot decomposed into duplicate sub-issues, failed on empty push.

### Solution

Add `hasMergedWork()` guard in poller before dispatching any issue:

1. **New client method**: `SearchMergedPRsForIssue(ctx, owner, repo, issueNumber)` — uses GitHub Search API to check for merged PRs with `GH-{number}` in title
2. **Poller guard**: Insert in both `checkForNewIssues()` (line ~673) and `findOldestUnprocessedIssue()` (line ~516) — after ProcessedStore clear, before candidate append
3. **On match**: Add `pilot-done` label, mark processed, skip issue

### Files to Modify

- `internal/adapters/github/client.go` — add `SearchMergedPRsForIssue()` (~20 lines)
- `internal/adapters/github/client_test.go` — table-driven test
- `internal/adapters/github/poller.go` — add `hasMergedWork()` + 2 insertion points (~30 lines)
- `internal/adapters/github/poller_test.go` — table-driven tests

### Acceptance Criteria

- [ ] Poller skips issues with merged PRs in both parallel and sequential paths
- [ ] Skipped issues get `pilot-done` label
- [ ] No API rate impact (single search query per retry candidate)
- [ ] Table-driven tests: no PRs, open PRs only, merged PRs exist
- [ ] `make test && make lint` pass
```

---

**Last Updated**: 2026-03-04
