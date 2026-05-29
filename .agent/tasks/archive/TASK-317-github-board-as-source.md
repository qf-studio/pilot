# TASK-317: GitHub Projects V2 board as work source (`FindIssuesFromProject`)

**Status:** ready to hand off to Pilot — spec hardened against executor no-op (durable fix tracked in TASK-320)
**Priority:** P1 — prerequisite for driving `qf-studio/studio-sdk` from its Project board
**Repo:** `qf-studio/pilot`
**Area:** `internal/adapters/github/`

> ⚠️ **Pilot execution note (read first).** A prior dispatch (GH-3228) failed 4×
> with `no new commit produced — worktree HEAD matches base branch parent`: the
> executor read the *existing* `project_board.go`, judged the board machinery
> "already present," and exited without editing. **This is a false-negative no-op
> (root cause: TASK-320).** To defeat it here: the **primary deliverable is a NEW
> file `internal/adapters/github/project_source.go` that does NOT exist yet** —
> verify with `ls internal/adapters/github/project_source.go` (expect "No such
> file"), then create it. `project_board.go` is the *write* path and a pattern to
> mirror, NOT evidence this task is done. The read path `FindIssuesFromProject`
> does not exist (`grep -rn FindIssuesFromProject internal/` returns nothing).
> You MUST produce a commit.

---

## Context

Pilot's GitHub poller discovers work **only by label** today:
`internal/adapters/github/poller.go:705` `findOldestUnprocessedIssue()` calls
`client.ListIssues(ctx, owner, repo, &ListIssuesOptions{Labels: []string{p.label}, ...})`
(`client.go:339`), then filters by lifecycle labels in Go.

We want a second sourcing mode: **pull work items from a GitHub Projects V2
board** (e.g. items in the `Todo` status column), so the board is the source of
truth instead of the `pilot` label. This unblocks the Studio SDK extraction
roadmap, where `qf-studio/projects/1` ("Studio SDK", org qf-studio, project #1)
drives the build.

The **write** direction already exists and is the pattern to mirror:
`internal/adapters/github/project_board.go` (`ProjectBoardSync`) resolves the
projectV2 node ID (org-then-user fallback), resolves the Status field + option
IDs, and mutates item status — all via `client.ExecuteGraphQL()` (`client.go:787`),
a raw HTTP+JSON GraphQL caller with no external library. Config already carries
what we need: `ProjectBoardConfig{ProjectNumber, StatusField, Statuses}` (`types.go:36`).

This is the **inverse** of `ProjectBoardSync`: instead of "given an issue node ID,
set its column", we need "given a project + status column, list the issue node IDs/numbers".

---

## Goal

Add a board-sourcing path so the poller can discover issues from a Projects V2
board filtered by status column, behind config, without breaking the existing
label flow.

## Scope

### In scope
1. **`FindIssuesFromProject`** — **CREATE NEW FILE** `internal/adapters/github/project_source.go`
   (sibling to `project_board.go`; it does not exist — `ls` it first):
   - GraphQL query: page through `node(projectID){ ... on ProjectV2 { items(first:100, after:$cursor) { pageInfo{hasNextPage endCursor} nodes { fieldValueByName(name:"Status"){ ... on ProjectV2ItemFieldSingleSelectValue { name } } content { ... on Issue { number id title body repository{nameWithOwner} labels(first:30){nodes{name}} } } } } } }`.
   - Reuse `resolveProjectID` (org-then-user fallback) — refactor it out of
     `ProjectBoardSync` into a shared free function `resolveProjectID(ctx, client, owner, number)`;
     have the existing method delegate to it. **Do not duplicate.**
   - **Decision (locked): hydrate labels + title + body directly in the GraphQL
     query** (the `labels(first:30)` + `title`/`body` selections above), NOT via a
     follow-up REST `GetIssue` per item. Rationale: the downstream candidate-filter
     loop (`poller.go:717-758`) is built entirely on `HasLabel(issue, ...)`; a
     board-sourced `*Issue` with empty `Labels` would silently bypass every
     lifecycle filter. GraphQL hydration keeps `HasLabel` working in one paginated
     call set (no N+1 REST). Map content node → `*Issue{Number, NodeID, Title, Body,
     Labels: []Label{{Name: ...}}}`.
   - Filter out items whose `repository.nameWithOwner` != configured `owner/repo`
     (a board can span repos), and skip nodes with `number == 0` (draft items / PRs).
   - Cursor pagination until `hasNextPage` is false.

2. **Config** — extend `ProjectBoardConfig` (`types.go:36`) with a sourcing toggle
   and source column, e.g.:
   ```go
   SourceEnabled bool   `yaml:"source_enabled"` // pull work FROM the board
   SourceStatus  string `yaml:"source_status"`  // column to pull from, default "Todo"
   ```
   Keep label mode the default; board-source is opt-in.

3. **Poller wiring** — in `findOldestUnprocessedIssue` (`poller.go:705`), when
   `ProjectBoard.SourceEnabled`, source the candidate list from
   `FindIssuesFromProject` instead of `ListIssues(Labels:…)`. **Everything after
   candidate fetch stays identical** — the lifecycle-label filters
   (`LabelInProgress`/`LabelDone`/`LabelBlocked`/`LabelNeedsClarification`,
   superseded-by-parent, failed/retry-ready, processed grace period, taskChecker)
   must still run. Board-source changes *where candidates come from*, not how they
   are filtered or dispatched.

### Out of scope
- Webhook-driven board events (poll only).
- Multi-status sourcing (single source column is enough for v1).
- Changing the status-write path (`ProjectBoardSync`) — leave as is.
- Any SDK-extraction work (separate roadmap).

---

## Critical files

| File | Role |
|---|---|
| `internal/adapters/github/project_board.go` | Pattern to mirror; `resolveProjectID` to share |
| `internal/adapters/github/poller.go:705` | `findOldestUnprocessedIssue` — wire source switch here |
| `internal/adapters/github/client.go:787` | `ExecuteGraphQL` — reuse verbatim |
| `internal/adapters/github/client.go:339` | `ListIssues` — the label path being conditionally replaced |
| `internal/adapters/github/types.go:36` | `ProjectBoardConfig` — extend |
| `configs/pilot.example.yaml` | document new `source_enabled`/`source_status` keys |

---

## Acceptance criteria

- [ ] New file `internal/adapters/github/project_source.go` exists and a fresh
      commit is produced (no-op = task failure).
- [ ] `FindIssuesFromProject(ctx, statusColumn)` returns `[]*Issue` for the named
      column, paginated, scoped to the configured `owner/repo`.
- [ ] Returned `*Issue` values carry `Labels` (hydrated from GraphQL) so the
      existing `HasLabel` lifecycle filters still apply.
- [ ] `resolveProjectID` is shared (not duplicated) between read and write paths.
- [ ] With `source_enabled: false` (default), behaviour is byte-identical to today
      (label sourcing). Regression-guard this.
- [ ] With `source_enabled: true`, poller dispatches issues sitting in the source
      column; lifecycle-label filtering + dedup + grace-period logic still applies.
- [ ] Table-driven unit tests with a fake GraphQL transport (mirror existing
      `client_test.go` patterns; use `internal/testutil` fake tokens — NO realistic
      token strings, per CLAUDE.md push-protection rules).
- [ ] `configs/pilot.example.yaml` documents the new keys.
- [ ] `make test` + `make lint` green.

## Auth note (for whoever runs it, not the implementer)
Board sourcing needs a token with `read:project` (classic) or a fine-grained PAT
with Projects: read on `qf-studio`. The status-write path needs project write.
This is a runtime/credentials concern, independent of the code change.

---

## Verification

```bash
make test && make lint
# Targeted:
go test ./internal/adapters/github/ -run 'Project|FindIssues|Poller' -v
```

End-to-end (manual, after a token with project scope exists):
1. Configure a project_board block with `source_enabled: true`, `project_number: 1`,
   `source_status: Todo` for `qf-studio/studio-sdk`.
2. Drop a test issue into the board's Todo column.
3. Confirm the poller picks it up and dispatches (label not required).
