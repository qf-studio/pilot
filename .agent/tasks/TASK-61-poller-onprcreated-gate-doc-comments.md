# TASK-61: Document the OnPRCreated gate in poller.go

**Status**: 🚧 Pending Pilot pickup
**Created**: 2026-05-11
**Assignee**: Pilot

---

## Context

Today's TASK-60 Phase 1 investigation found that the `OnPRCreated` callsites at `internal/adapters/github/poller.go:589` (sequential) and `:1174` (parallel) are correctly wired — they fail to fire because the executor surfaces `result.PRNumber == 0`. The gate semantics are non-obvious at the read site; future readers will likely repeat the same call-graph spelunk.

Add a one-line doc comment above each gate so the next person doesn't have to.

## Goal

Documentation-only chore. Zero code/semantics change.

## Implementation

**Files**:
- `internal/adapters/github/poller.go` — add a one-line `//` comment immediately above each `if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {` gate (two sites).

**Comment text** (verbatim, both sites):
```
// Gate: PRNumber > 0 implies executor surfaced a valid PR URL via runner.go:3151. Empty PRUrl (no-commits guard, push-fail, title-rejection) leaves PRNumber=0 and we silently skip — see TASK-60 for the upstream chain.
```

**Constraints**:
- Single-phase. **Do NOT decompose** — this is a literal two-line edit.
- Title must be conventional (`docs(autopilot): ...` or similar).
- PR body must include a `## Context` H2.

## Verify

```bash
make build    # compiles cleanly
make lint     # no new lint findings
git diff --stat origin/main...HEAD  # exactly one file changed (poller.go), ≤2 lines added
```

## Done

- [ ] Two comment lines added above the two OnPRCreated gates in poller.go
- [ ] No other code touched
- [ ] PR opened with conventional title and `## Context` body header

---

**Last Updated**: 2026-05-11
