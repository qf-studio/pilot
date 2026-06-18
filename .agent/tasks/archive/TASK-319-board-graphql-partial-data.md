# TASK-319: Board-GraphQL partial-data tolerance

**Status**: ✅ Completed (PR #3610 merged, shipped v2.187.0)
**Created**: 2026-06-16
**Assignee**: Pilot
**Parent**: TASK-319 (GH Projects V2 full loop, archived) · carry-over from TASK-322 audit Wave 4

---

## Context

**Problem**:
GitHub Projects V2 GraphQL frequently returns **partial responses** — valid `data` for
most board nodes plus a per-node `NOT_FOUND` / `FORBIDDEN` error for nodes the token
can't see (e.g. an item from a private repo, or a deleted issue still referenced by the
board). Today `Client.ExecuteGraphQL` returns immediately whenever `len(gqlResp.Errors) > 0`,
so it never reaches the `Data` unmarshal. One bad node therefore aborts the **entire board
page** — and because the board pagination loop in `project_source.go` does
`return nil, err`, it also discards **every page already collected**. A single
inaccessible item silently zeroes the poller's view of the board.

The diagnosability half already shipped (PR #3603, TASK-357 board low): `ExecuteGraphQL`
now aggregates **all** errors via `GraphQLError.String()` (message + type + path), and
`GraphQLError` carries `Type` and `Path` fields. This task adds the actual **tolerance**.

**Goal**:
Let the board source keep the good nodes from a partial response (dropping only the
nodes that errored with a tolerable type), continue pagination, and still abort hard on
genuinely fatal errors — **without changing behavior for the many other `ExecuteGraphQL`
callers**.

---

## Acceptance Criteria

- [ ] A partial board response (valid `data` + one `FORBIDDEN`/`NOT_FOUND` node error) yields
      the good nodes; the errored node is dropped, not the page.
- [ ] Partial errors do not abort pagination — already-collected pages are retained and
      subsequent pages are still fetched.
- [ ] A fatal error (`RATE_LIMITED`, auth, GraphQL syntax, or any non-tolerable type) still
      aborts the query exactly as today (aggregated error message, no data returned).
- [ ] A mixed response (one tolerable + one fatal error) is treated as **fatal** (aborts).
- [ ] Existing `ExecuteGraphQL` callers are unchanged — same signature, same behavior.
- [ ] Dropped nodes are logged (count + aggregated `GraphQLError.String()`) so partial
      pages are observable, not silent.

---

## Implementation

### Phase 1: Partial-tolerant GraphQL variant
**Goal**: Add an opt-in path that unmarshals `Data` alongside tolerable errors.

**Tasks**:
- [ ] Add a typed error `PartialGraphQLError struct { Errors []GraphQLError }` with an
      `Error()` method that reuses the existing per-error `String()` aggregation.
- [ ] Add a tolerable-type classifier. Tolerable set: `NOT_FOUND`, `FORBIDDEN`.
      Everything else (incl. empty `Type`, `RATE_LIMITED`, auth, syntax) is fatal.
- [ ] Add `ExecuteGraphQLTolerant(ctx, query, vars, result)` (or refactor a shared
      private core that both the strict and tolerant methods call):
    - If **any** error is non-tolerable → return the aggregated fatal error (current
      behavior), do **not** unmarshal `Data`.
    - If **all** errors are tolerable → unmarshal `Data` into `result`, then return
      `*PartialGraphQLError{Errors}` so the caller can inspect/log.
    - No errors → unmarshal `Data`, return nil (same as strict path).
- [ ] Keep `ExecuteGraphQL` behavior byte-identical (delegate to the shared core with a
      no-op tolerable predicate / strict mode).

**Files**:
- `internal/adapters/github/client.go` (`ExecuteGraphQL` ~895-952, error branch ~932, types ~58-85)

### Phase 2: Board caller tolerates partial pages
**Goal**: `project_source.go` keeps good nodes and continues paginating.

**Tasks**:
- [ ] Switch the board pagination call (`project_source.go:124`) to
      `ExecuteGraphQLTolerant`.
- [ ] On `*PartialGraphQLError`: log dropped-node count + aggregated error, then proceed
      to process `resp.Node.Items.Nodes` (good nodes already unmarshalled) and continue
      the loop. The existing per-node filters (number==0, cross-repo, state, status) already
      skip malformed/empty nodes, so no extra node-level guard is needed.
- [ ] On any other (fatal) error: keep `return nil, err`.

**Files**:
- `internal/adapters/github/project_source.go` (`FindIssuesFromProject` loop ~116-170)

### Phase 3: Tests
**Goal**: Lock the contract.

**Tasks**:
- [ ] `client_test.go`: partial response (data + one `FORBIDDEN`) → returns
      `*PartialGraphQLError` AND populates `result`.
- [ ] `client_test.go`: fatal response (`RATE_LIMITED`, or empty-type error) → returns
      fatal error, `result` untouched.
- [ ] `client_test.go`: mixed (one tolerable + one fatal) → fatal.
- [ ] `client_test.go`: strict `ExecuteGraphQL` unchanged — any error aborts.
- [ ] `project_source_test.go`: partial board page keeps the good issues + continues
      pagination across a second page.

---

## Out of Scope

- Changing any other `ExecuteGraphQL` caller to tolerate partials (board-source only).
- Retrying tolerable-error nodes individually.
- Making the tolerable-type set configurable (hard-code `NOT_FOUND`/`FORBIDDEN` for now).
- The `WithBoardSync` write-back wiring (in-flight on `fix/task319-wire-boardsync`,
  touches only `cmd/pilot/main.go` — no overlap with this change).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| API shape | (a) flag on `ExecuteGraphQL`, (b) always unmarshal Data + typed error, (c) separate tolerant method | (c) `ExecuteGraphQLTolerant` + shared private core | Zero behavior change for existing callers; opt-in is explicit at the call site |
| Tolerable types | NOT_FOUND/FORBIDDEN; vs configurable set | NOT_FOUND, FORBIDDEN only | These are the per-node access errors GH Projects V2 emits on partial reads; everything else signals a real failure |
| Mixed tolerable+fatal | tolerate / fatal | fatal | A real error in the same response must not be masked by a tolerable one |
| Signaling partial to caller | sentinel error / typed error / out-param | typed `*PartialGraphQLError` | Caller can `errors.As` it, log details, and still use the unmarshalled data |

---

## Verify

```bash
go test ./internal/adapters/github/... -run 'GraphQL|ProjectBoard|PartialData' -v
make lint
make test-short
```

---

## Done

- [ ] `ExecuteGraphQLTolerant` + `PartialGraphQLError` exist in `client.go`; `ExecuteGraphQL` signature/behavior unchanged.
- [ ] `project_source.go` uses the tolerant variant, logs dropped nodes, retains good nodes + pages.
- [ ] New tests pass (partial / fatal / mixed / strict-unchanged / board pagination).
- [ ] `make lint` clean, `make test-short` green.

---

## Refs

- Parent (archived): `.agent/tasks/archive/TASK-319-gh-projects-full-loop.md`
- Carry-over recorded in: `.agent/tasks/TASK-322-remediation-roadmap.md` (Wave 4)
- Diagnosability half: PR #3603 (TASK-357 board low)
- Pilot issue: https://github.com/qf-studio/pilot/issues/3609

---

**Last Updated**: 2026-06-16 (dispatched to Pilot as #3609)
