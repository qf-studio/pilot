> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-306: Workpad ticket-comment pattern (cross-adapter)

**Status**: queued
**Created**: 2026-05-26
**Severity**: P0
**Effort**: M-L (~1 day)
**Job (JTD)**: J3 Observe + J5 Review
**Source**: Symphony research, Wave 5 / `~/.claude/plans/let-s-plan-that-use-staged-seal.md`

---

## Context

**Problem**: Today Pilot's executor leaves a stream of free-form comments on the source ticket (GitHub Issue, Linear, Jira, etc.). Operators/reviewers must scroll the thread to reconstruct what the agent is doing. There is no single canonical artifact per ticket. Stakeholders watching from the tracker side see noise, not progress.

**Goal**: One persistent, structured comment per ticket — the **Workpad** — that the agent edits in place at each milestone. Always renders the same template:

```
## Pilot Workpad

### Plan
1. ...
2. ...

### Acceptance Criteria
- [ ] ...
- [ ] ...

### Validation
- ...

### Notes
- ...

### Confusions / Pushback
- ...
```

Borrowed from OpenAI Symphony's Codex Workpad (`/tmp/symphony/elixir/WORKFLOW.md` lines 296–326).

**Why now**: Highest-ROI Symphony borrow per JTD map. Hits Observe (J3) and Review (J5) jobs simultaneously. Low surface-area change touching only adapter comment-poster paths — no executor or autopilot churn.

---

## Acceptance Criteria

- [ ] Single comment per ticket on each of the 6 trackers (github, gitlab, jira, linear, azuredevops, asana, plane). On second + subsequent milestones, **edit** the existing comment rather than post a new one.
- [ ] Workpad section template fixed (Plan / Acceptance Criteria / Validation / Notes / Confusions) and identical across adapters.
- [ ] Comment carries a stable marker (e.g., HTML comment `<!-- pilot-workpad:v1 -->`) so future runs can locate and update it.
- [ ] Executor populates Workpad fields at: (a) start of run (Plan + Acceptance Criteria), (b) after each significant phase (Validation + Notes), (c) on completion / failure (final state).
- [ ] If the adapter API does not support comment editing (Slack/Discord/Telegram out of scope; they're chat, not trackers), gracefully skip — no error.
- [ ] Verbose per-step chatter elsewhere is reduced or routed to executor logs, not the ticket.

---

## Implementation

### Phase 1: Workpad renderer + locator (adapter-agnostic)
**Goal**: Define the canonical Workpad type and renderer in one place.

**Tasks**:
- [ ] Create `internal/executor/workpad/workpad.go` with `Workpad` struct (Plan, AcceptanceCriteria, Validation, Notes, Confusions fields) and `Render() string` method.
- [ ] Define marker constant (e.g., `<!-- pilot-workpad:v1 -->`) used as locator for find-and-edit.
- [ ] Unit test the renderer for stable output across multiple update cycles.

**Files**:
- `internal/executor/workpad/workpad.go` (new)
- `internal/executor/workpad/workpad_test.go` (new)

### Phase 2: Per-adapter UpsertWorkpad
**Goal**: Each tracker adapter gains an `UpsertWorkpad(issueID, workpad)` method that locates the marker and either creates or edits the comment.

**Tasks**:
- [ ] Extend each adapter's comment client with `UpsertWorkpad`:
  - `internal/adapters/github/client.go` (GH REST: PATCH `/repos/:o/:r/issues/comments/:id`)
  - `internal/adapters/gitlab/client.go` (notes API supports edit)
  - `internal/adapters/jira/client.go` (PUT `/issue/:key/comment/:id`)
  - `internal/adapters/linear/client.go` (`commentUpdate` GraphQL mutation)
  - `internal/adapters/azuredevops/client.go` (PATCH work-item-comments API)
  - `internal/adapters/asana/client.go` (story update — verify if supported; if not, fall back to delete+create)
  - `internal/adapters/plane/client.go` (comment update endpoint)
- [ ] Each `UpsertWorkpad` first list-comments + grep-for-marker, then create OR update.

**Files**: see above per adapter.

### Phase 3: Executor wiring
**Goal**: Executor calls `UpsertWorkpad` at three points: start-of-run, post-phase, end-of-run.

**Tasks**:
- [ ] In `internal/executor/runner.go` (around the existing comment-posting calls) replace ad-hoc comment posts with `workpad.Upsert(...)` calls.
- [ ] Allow the Claude Code session to update the Workpad mid-flight by exposing a small tool/hook (deferred — phase 4 if needed).
- [ ] Confirm `executor.Task` has enough metadata (`SourceAdapter`, `SourceIssueID`) to route Workpad writes (already does, per `internal/executor/runner.go:134`).

**Files**:
- `internal/executor/runner.go`

### Phase 4 (optional): Agent-driven Workpad updates
**Goal**: Claude Code itself can update Workpad fields during the run, not just at executor-controlled boundaries.

Deferred — ship phases 1–3 first, measure stakeholder feedback, then decide.

---

## Out of Scope

- Workpad on chat adapters (Slack/Discord/Telegram). Different surface — handled by a separate task if/when needed.
- Backfill of Workpads on already-merged or in-flight pre-TASK-306 tickets.
- Workpad localization / templating per repo (that's `TASK-304` territory).
- Removing all existing executor comments — only consolidate the *operational status* updates; PR-creation and final-state comments may stay separate if cleaner.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Marker mechanism | HTML comment, hidden char, frontmatter | HTML comment (`<!-- pilot-workpad:v1 -->`) | Visible in raw markdown, ignored by renderers, easy to grep, version-able |
| Renderer location | Per-adapter, shared package | Shared package (`internal/executor/workpad/`) | One source of truth; identical render across trackers |
| Edit vs delete+recreate | edit, delete+recreate, append | Edit (with fallback to delete+recreate where API doesn't support edit) | Preserves comment ID for permalink stability |
| Trigger points | Every event, fixed milestones | Fixed milestones (start, post-phase, end) | Avoids API rate-limit thrash; clear semantics |

---

## Files Affected (estimate)

- `internal/executor/workpad/` (new package)
- `internal/executor/runner.go` (modified)
- `internal/adapters/{github,gitlab,jira,linear,azuredevops,asana,plane}/client.go` (each modified)

Test files alongside each.

---

## Verify

```bash
# Unit tests for the Workpad renderer
go test ./internal/executor/workpad/...

# Per-adapter UpsertWorkpad tests (mocked HTTP)
go test ./internal/adapters/{github,gitlab,jira,linear,azuredevops,asana,plane}/...

# Full executor integration test against a sandbox repo
make test
```

**Manual E2E**: file a `pilot`-labeled issue on a test repo; confirm exactly one Pilot Workpad comment exists at end of run; confirm marker present; confirm second run on same issue *edits* the comment, doesn't duplicate.

---

## Done

- [ ] Workpad renderer ships with unit tests
- [ ] All 6 tracker adapters have working `UpsertWorkpad`
- [ ] Executor uses Workpad at 3 milestone points
- [ ] One-comment-per-ticket invariant verified on at least 2 adapters end-to-end (GitHub + Linear minimum)
- [ ] No regression in existing comment paths (PR creation, final-state notification)

---

## Refs

- Master plan: `~/.claude/plans/let-s-plan-that-use-staged-seal.md` (JTD context)
- Symphony evidence: `/tmp/symphony/elixir/WORKFLOW.md` lines 296–326 (Workpad template)
- Symphony spec: `/tmp/symphony/SPEC.md` §10 (agent protocol)
- Related: `TASK-304` (`.pilot/workflow.yaml` may eventually override Workpad template)

---

**Last Updated**: 2026-05-26
