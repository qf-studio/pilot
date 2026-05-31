# TASK-338 (C2): Project board source silently ignored in parallel/auto execution mode

**Wave:** 2 · **Pilot** · **Severity:** HIGH (bug) · **Issue:** #3329 · **Audit ref:** TASK-322 finding C2 (TASK-319 board trio)

---

## Problem
`WithProjectBoardSource` only affects `findOldestUnprocessedIssue`, which runs **only** in sequential mode
(`startSequential`). The parallel/auto path `checkForNewIssues` unconditionally calls `p.client.ListIssues`
by label and never consults `p.projectBoardSource`. In `cmd/pilot/main.go` the board source is wired
regardless of execMode, and execMode can be `parallel` (the production default for concurrency>1) or `auto`.
So `source_enabled:true` + `mode:parallel` gets the board source **silently dropped** — Pilot reverts to
label-based polling, ignoring the board column entirely.

## Approach
Refactor candidate fetch into a single helper (e.g. `fetchCandidates(ctx)`) that checks
`p.projectBoardSource` first, and call it from **both** `checkForNewIssues` and `findOldestUnprocessedIssue`.
(Alternative considered: reject `source_enabled && mode!=sequential` at config-validation time — prefer
making it work in parallel mode.)

## Files to modify
- `internal/adapters/github/poller.go:1040-1049, 741-746` (+ auto-mode default at poller.go:360)
- `internal/adapters/github/poller_test.go`

## Test Strategy
- Stubbed board source + `mode:parallel` → `checkForNewIssues` pulls from the board source, not `ListIssues`.

## Effort
M. One PR. **Land first** of the github trio — the `fetchCandidates` refactor is the structural anchor.

## Out of Scope / coordinate
- Distinct files from TASK-339 (client.go) / TASK-340 (project_source.go) but same package — rebase on main.
- CI gotcha: don't add per-candidate API calls that slow the parallel poller (starves the stress `-race` test).
