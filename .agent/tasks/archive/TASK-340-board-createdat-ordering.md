# TASK-340 (C4): Board-sourced issues have zero CreatedAt — breaks oldest-first dispatch in board mode

**Wave:** 2 · **Pilot** · **Severity:** MEDIUM (bug) · **Issue:** #3331 · **Audit ref:** TASK-322 finding C4 (TASK-319 board trio)

---

## Problem
`findOldestUnprocessedIssue` sorts candidates by `CreatedAt` ascending and dispatches the oldest. In label
mode `CreatedAt` is populated from the REST `created_at` field. But the board GraphQL query
`queryProjectBoardItems` does **not** request the issue `createdAt`, and `FindIssuesFromProject` constructs
`Issue{}` without setting `CreatedAt`. So every board-sourced candidate has `CreatedAt == zero`, making
`sort.Slice` a no-op — selection picks whatever order GraphQL returned (board/insertion order), not creation
order. Silently violates the FIFO dispatch contract and is non-deterministic across pages.

## Approach
Add `createdAt` to the Issue inline fragment in `queryProjectBoardItems`, add a `CreatedAt` field to
`projectBoardItemNode.Content`, parse it (RFC3339), and set `issues[i].CreatedAt` in `FindIssuesFromProject`
so the existing oldest-first sort works identically in board mode.

## Files to modify
- `internal/adapters/github/project_source.go:15-34, 145-153`
- `internal/adapters/github/project_source_test.go`

## Test Strategy
- Two board items with known `createdAt` → `FindIssuesFromProject` returns them with populated `CreatedAt`; oldest-first ordering holds.

## Effort
S. One PR. Distinct file from TASK-338/339.

## Out of Scope
- Pagination/cursor changes (separate concern).
