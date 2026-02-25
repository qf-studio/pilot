# TASK-05: Plane.so Adapter Integration

**Status**: 🚧 In Progress
**Created**: 2026-02-25
**Parent Issue**: GH-1827

---

## Context

**Problem**:
Pilot has adapters for Linear, Jira, Asana, GitHub, GitLab, and Azure DevOps — but not for Plane.so, a popular open-source project planning tool. Self-hosted teams using Plane.so cannot integrate with Pilot.

**Goal**:
Add a Plane.so adapter (`internal/adapters/plane/`) that mirrors existing adapter capabilities: webhook intake, polling, bidirectional feedback (PR comments, state transitions), and parallel execution support.

**Success Criteria**:
- [ ] Plane.so poller picks up issues with `pilot` label
- [ ] Bidirectional feedback: PR URL posted as comment, issue state updated on completion
- [ ] Webhook handler for real-time intake
- [ ] ProcessedStore for dedup across restarts
- [ ] Parallel execution with scope-overlap guard
- [ ] Wired into main.go with `--plane` flag
- [ ] Unit tests for client, poller, webhook

---

## Implementation Plan

### Phase 1: Types, Config, REST Client (GH-TBD-1)
**Goal**: Foundation — types, config, and API client

**Tasks**:
- [ ] Create `internal/adapters/plane/types.go` — Config, Issue struct, state group mappings
- [ ] Create `internal/adapters/plane/client.go` — REST client with methods:
  - `ListWorkItems(ctx, filter)` — fetch issues by label
  - `GetWorkItem(ctx, id)` — fetch single issue
  - `UpdateWorkItem(ctx, id, fields)` — update state/labels
  - `ListStates(ctx)` — get project states (cache by group)
  - `ListLabels(ctx)` — get project labels
  - `AddLabel(ctx, issueID, labelID)` / `RemoveLabel(ctx, issueID, labelID)`
  - `AddComment(ctx, issueID, body)` — post PR comment
  - `CreateWorkItem(ctx, fields)` — for epic decomposition
- [ ] Add `Plane *plane.Config` to `AdaptersConfig` in `internal/config/config.go`

**Files**:
- `internal/adapters/plane/types.go` (create)
- `internal/adapters/plane/client.go` (create)
- `internal/config/config.go` (modify — add 1 field)

**Reference**: `internal/adapters/linear/client.go`, `internal/adapters/jira/client.go`

**API Notes**:
- Base URL: `https://api.plane.so/` (configurable for self-hosted)
- Auth header: `X-API-Key: plane_api_<token>`
- All paths: `/api/v1/workspaces/{slug}/projects/{project_id}/...`
- Use `/work-items/` endpoints (NOT deprecated `/issues/`)
- Rate limit: 60 req/min — respect `X-RateLimit-Remaining` header
- Labels assigned as UUID array on work item PATCH (not separate endpoint)

### Phase 2: ProcessedStore (GH-TBD-2)
**Goal**: Persistent dedup for Plane issues across restarts

**Tasks**:
- [ ] Add `plane_processed` table to `internal/autopilot/state_store.go`
- [ ] Implement: `MarkPlaneIssueProcessed`, `UnmarkPlaneIssueProcessed`, `IsPlaneIssueProcessed`, `LoadPlaneProcessedIssues`

**Files**:
- `internal/autopilot/state_store.go` (modify — add table + 4 methods)

**Reference**: Copy Jira processed pattern exactly (`jira_processed` table)

### Phase 3: Poller (GH-TBD-3)
**Goal**: Poll Plane.so for issues with `pilot` label, dispatch for execution

**Tasks**:
- [ ] Create `internal/adapters/plane/poller.go` — copy Jira poller structure
- [ ] Label caching: resolve `pilot`, `pilot-in-progress`, `pilot-done`, `pilot-failed` label UUIDs on startup
- [ ] Orphaned issue recovery: find `pilot-in-progress` issues on startup, remove label
- [ ] Parallel execution with semaphore + WaitGroup
- [ ] `PollerOption` builder pattern: OnIssue, ProcessedStore, MaxConcurrent, OnPRCreated, Logger
- [ ] Filter by configured `project_ids`

**Files**:
- `internal/adapters/plane/poller.go` (create)

**Reference**: `internal/adapters/jira/poller.go` (~90% copy), `internal/adapters/asana/poller.go`

### Phase 4: Webhook Handler (GH-TBD-4)
**Goal**: Real-time issue intake via Plane.so webhooks

**Tasks**:
- [ ] Create `internal/adapters/plane/webhook.go`
  - HMAC-SHA256 signature verification (`X-Plane-Signature` header)
  - Parse `issue` create/update events from webhook payload
  - Filter by `pilot` label, ignore non-matching projects
- [ ] Register `/webhooks/plane` route in `internal/gateway/server.go`

**Files**:
- `internal/adapters/plane/webhook.go` (create)
- `internal/gateway/server.go` (modify — add 1 route)

**Reference**: `internal/adapters/linear/webhook.go` (~80% copy)

**Webhook Payload**:
- Header `X-Plane-Event`: `issue`
- Header `X-Plane-Signature`: HMAC-SHA256 hex digest
- Body: `{ "event": "issue", "action": "created|updated", "data": { ...work item... } }`

### Phase 5: State Transitions & PR Comments (GH-TBD-5)
**Goal**: Update Plane.so issue state and post PR links on completion

**Tasks**:
- [ ] `UpdateWorkItemState()` — resolve state UUID by group (`started` → in-progress, `completed` → done)
- [ ] `AddComment()` — post PR URL with `external_source: "pilot"`, `external_id: "pilot-exec-{id}"`
- [ ] Wire state transitions into poller success/failure paths
- [ ] Cache state UUIDs on startup (list states, group by category)

**Files**:
- `internal/adapters/plane/client.go` (modify — add state transition logic)
- `internal/adapters/plane/poller.go` (modify — wire state updates)

**Reference**: Linear `UpdateIssueState`, Jira `TransitionIssueTo`, Asana `CompleteTask`

### Phase 6: Wire into main.go (GH-TBD-6)
**Goal**: Full integration — CLI flag, poller startup, handler wiring

**Tasks**:
- [ ] Add `--plane` CLI flag to `cmd/pilot/main.go`
- [ ] Wire poller startup (copy Linear wiring pattern, ~40 lines)
- [ ] Create `handlePlaneIssueWithResult()` handler (copy `handleLinearIssueWithResult`)
- [ ] Wire `OnPRCreated` callback to autopilot controller
- [ ] Wire `SubIssueCreator` for epic decomposition
- [ ] Add plane section to `configs/pilot.example.yaml`
- [ ] Add plane adapter tests

**Files**:
- `cmd/pilot/main.go` (modify — ~80 lines)
- `configs/pilot.example.yaml` (modify — add plane section)
- `internal/adapters/plane/client_test.go` (create)
- `internal/adapters/plane/poller_test.go` (create)
- `internal/adapters/plane/webhook_test.go` (create)

**Reference**: Linear wiring in `main.go` (~lines 824-860)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| API endpoints | `/issues/` vs `/work-items/` | `/work-items/` | `/issues/` deprecated, EOL March 2026 |
| Label management | Separate API vs PATCH array | PATCH array on work item | Plane assigns labels as UUID array, no separate label-issue endpoint |
| State transitions | Hardcode state names vs group lookup | Group lookup | States are per-project, but groups are fixed (5 categories) |
| PR feedback | Native PR field vs comment | Comment with `external_source` | Plane has no native PR field; `external_source`/`external_id` enable dedup |

---

## Dependencies

**Requires**:
- [ ] Plane.so API access (user provides API key)
- [ ] Webhook secret (generated in Plane UI)

**Blocks**:
- [ ] Nothing

---

## Verify

```bash
make test
make lint
# User will E2E test against self-hosted and cloud Plane.so
```

---

## Done

- [ ] `internal/adapters/plane/` package exists with client, poller, webhook, types
- [ ] ProcessedStore table for Plane in state_store.go
- [ ] `--plane` CLI flag works
- [ ] Poller picks up `pilot`-labeled issues
- [ ] PR comment posted on completion
- [ ] Issue state updated to `completed` group
- [ ] Webhook handler verifies signature and dispatches
- [ ] Unit tests pass
- [ ] Example config documented

---

**Last Updated**: 2026-02-25
