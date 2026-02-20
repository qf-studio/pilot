# TASK-04: Deduplicate History Panel — Show Latest Execution Per Issue

**Status**: 🚧 In Progress
**Created**: 2026-02-20
**Assignee**: Pilot

---

## Context

**Problem**:
Desktop app HISTORY panel shows duplicate entries for the same issue when Pilot retries. Example: GH-1657 appears twice as failed (`x`), while GH-1658 and GH-1660 (which completed the same work) show as succeeded (`+`). Users see confusing duplicate noise instead of a clean history.

**Root Cause**:
`desktop/app.go` `GetHistory()` calls `store.GetRecentExecutions(limit)` which returns every execution record from SQLite. Each retry attempt is a separate row. No deduplication or grouping by issue.

**Goal**:
History panel shows only the latest execution per issue. If GH-1657 failed twice then succeeded via retry, show one entry with status "done" — not three entries.

**Success Criteria**:
- [ ] Each issue appears at most once in history
- [ ] The displayed status reflects the latest execution attempt
- [ ] Timestamp shows the most recent execution
- [ ] PR URL from the successful attempt is shown when available
- [ ] Total history count remains accurate

---

## Implementation Plan

### Phase 1: Deduplicate in `GetHistory()` (`desktop/app.go`)

**Goal**: Group execution records by issue ID, keep only the latest

**Tasks**:
- [ ] After fetching executions from `store.GetRecentExecutions(limit)`, build a map keyed by `TaskID` (e.g. "GH-1657")
- [ ] For each issue, keep the execution with the latest timestamp
- [ ] If any execution for an issue succeeded, use that status (success takes priority over earlier failures)
- [ ] If a successful execution has a PR URL, prefer that entry
- [ ] Return deduplicated list sorted by most recent first

**Files**:
- `desktop/app.go` — `GetHistory()` method (~line 165)

**Implementation**:

```go
func (a *App) GetHistory(limit int) []HistoryEntry {
    // ... existing fetch logic ...

    // Deduplicate: keep best result per issue
    bestByIssue := make(map[string]*HistoryEntry)
    for i := range entries {
        e := &entries[i]
        existing, ok := bestByIssue[e.IssueID]
        if !ok {
            bestByIssue[e.IssueID] = e
            continue
        }
        // Success takes priority over failure
        if e.Status == "completed" && existing.Status != "completed" {
            bestByIssue[e.IssueID] = e
        } else if e.CompletedAt.After(existing.CompletedAt) && e.Status == existing.Status {
            // Same status: keep most recent
            bestByIssue[e.IssueID] = e
        }
    }

    // Collect and sort by time descending
    deduped := make([]HistoryEntry, 0, len(bestByIssue))
    for _, e := range bestByIssue {
        deduped = append(deduped, *e)
    }
    sort.Slice(deduped, func(i, j int) bool {
        return deduped[i].CompletedAt.After(deduped[j].CompletedAt)
    })
    return deduped
}
```

### Phase 2: Also deduplicate `GetQueueTasks()` (`desktop/app.go`)

**Goal**: Queue panel has the same duplication problem

**Tasks**:
- [ ] Apply same dedup logic in `GetQueueTasks()` (~line 125)
- [ ] Running status takes priority, then completed, then failed
- [ ] Keep the entry with the highest progress

**Files**:
- `desktop/app.go` — `GetQueueTasks()` method

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Dedup layer | SQLite query vs Go code | Go code in app.go | Simpler, no schema changes, desktop-only concern |
| Priority | Latest timestamp vs success wins | Success wins | User cares about outcome, not attempt order |
| Scope | Desktop only vs also TUI | Desktop only | TUI reads from monitor (live state), not SQLite history |

---

## Verify

```bash
make test
# Build desktop app
cd desktop && wails build
# Visual: history should show each issue once with correct status
```

---

## Done

- [ ] Each issue appears at most once in HISTORY panel
- [ ] Success status takes priority over earlier failures
- [ ] QUEUE panel also deduplicated
- [ ] Sort order: most recent first
- [ ] All tests pass

---

**Last Updated**: 2026-02-20
