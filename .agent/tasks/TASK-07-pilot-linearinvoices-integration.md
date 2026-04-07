# TASK-07: Pilot + LinearInvoices Integration

**Status**: Planning
**Created**: 2026-03-04
**Assignee**: Manual (cross-repo)

---

## Context

**Problem**:
Pilot ships code via Linear tickets. LinearInvoices bills clients from Linear tickets. But there's no connection between them — a freelancer using both must manually track which Pilot executions map to which invoices. Execution metadata (time, cost, complexity) is lost.

**Goal**:
When Pilot completes a task, LinearInvoices automatically receives execution data and enriches invoice line items with real metrics (duration, AI cost, files changed). Full code-to-cash pipeline with zero manual steps.

**Success Criteria**:
- [ ] Pilot `task.completed` webhook fires to LinearInvoices endpoint
- [ ] LinearInvoices creates/enriches invoice line items from Pilot events
- [ ] Execution metadata (duration, cost, model, PR link) visible in invoice UI
- [ ] Client mapping via shared Linear project IDs works end-to-end
- [ ] HMAC signature verification between services

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Linear                                │
│  (shared source of truth: projects, tickets, statuses)       │
└──────┬──────────────────────────────────┬────────────────────┘
       │ GraphQL API                      │ GraphQL API
       ▼                                  ▼
┌──────────────┐   task.completed    ┌──────────────────┐
│    Pilot     │ ──── webhook ────►  │  LinearInvoices  │
│  (executor)  │   HMAC-signed       │   (billing)      │
│              │                     │                   │
│ ExecutionResult:                   │ Creates/enriches: │
│  - duration                        │  - invoice item   │
│  - tokens/cost                     │  - with metadata  │
│  - PR URL                          │  - maps to client │
│  - files changed                   │  - via project ID │
│  - commit SHA                      │                   │
│  - model name                      │                   │
└──────────────┘                     └──────────────────┘
```

**Data flow:**
1. Pilot completes Linear ticket → fires `task.completed` webhook
2. LinearInvoices receives webhook at `POST /api/v1/webhooks/pilot`
3. LI extracts Linear issue number from `task_id` (format: `GH-{number}` or Linear ID)
4. LI resolves issue → project → client via existing client-project mapping
5. LI creates draft invoice item with execution metadata
6. Item appears in next invoice generation (manual or recurring rule)

---

## Implementation Plan

### Phase 1: Enrich Pilot Webhook Payload (Pilot repo)

**Goal**: Extend `TaskCompletedData` to include full execution metrics.

**Current payload** (`internal/webhooks/event.go`):
```go
type TaskCompletedData struct {
    TaskID    string        `json:"task_id"`
    Title     string        `json:"title"`
    Project   string        `json:"project"`
    Duration  time.Duration `json:"duration_ms"`
    PRCreated bool          `json:"pr_created"`
    PRURL     string        `json:"pr_url,omitempty"`
    Summary   string        `json:"summary,omitempty"`
}
```

**Enriched payload**:
```go
type TaskCompletedData struct {
    // Existing fields
    TaskID    string        `json:"task_id"`
    Title     string        `json:"title"`
    Project   string        `json:"project"`
    Duration  time.Duration `json:"duration_ms"`
    PRCreated bool          `json:"pr_created"`
    PRURL     string        `json:"pr_url,omitempty"`
    Summary   string        `json:"summary,omitempty"`

    // New: execution metrics for billing
    CommitSHA        string  `json:"commit_sha,omitempty"`
    TokensInput      int64   `json:"tokens_input,omitempty"`
    TokensOutput     int64   `json:"tokens_output,omitempty"`
    EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
    FilesChanged     int     `json:"files_changed,omitempty"`
    LinesAdded       int     `json:"lines_added,omitempty"`
    LinesRemoved     int     `json:"lines_removed,omitempty"`
    ModelName        string  `json:"model_name,omitempty"`
    BranchName       string  `json:"branch_name,omitempty"`
    IssueNumber      int     `json:"issue_number,omitempty"`
    IssueSource      string  `json:"issue_source,omitempty"` // "github", "linear", "jira"
    LinearProjectID  string  `json:"linear_project_id,omitempty"`
}
```

**Tasks**:
- [ ] Extend `TaskCompletedData` struct with execution metrics
- [ ] Update `dispatchWebhook()` call in `runner.go:2552` to populate new fields from `ExecutionResult`
- [ ] Add `IssueNumber`, `IssueSource`, `LinearProjectID` fields from task metadata
- [ ] Update webhook docs/example config
- [ ] Tests

**Files**:
- `internal/webhooks/event.go` — extend struct
- `internal/executor/runner.go:2552-2560` — populate new fields
- `internal/webhooks/manager_test.go` — test serialization

**Effort**: Small (1-2 hours)

---

### Phase 2: Add Pilot Webhook Handler (LinearInvoices repo)

**Goal**: Receive and validate Pilot task completion events.

**New endpoint**: `POST /api/v1/webhooks/pilot`

**Handler design**:
```go
// internal/pilot/webhook/handler.go
type PilotWebhookHandler struct {
    signingSecret string
    service       PilotEventService
    logger        *zap.Logger
}

type TaskCompletedEvent struct {
    TaskID           string  `json:"task_id"`
    Title            string  `json:"title"`
    Project          string  `json:"project"`
    DurationMs       int64   `json:"duration_ms"`
    PRURL            string  `json:"pr_url,omitempty"`
    CommitSHA        string  `json:"commit_sha,omitempty"`
    TokensInput      int64   `json:"tokens_input,omitempty"`
    TokensOutput     int64   `json:"tokens_output,omitempty"`
    EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
    FilesChanged     int     `json:"files_changed,omitempty"`
    LinesAdded       int     `json:"lines_added,omitempty"`
    LinesRemoved     int     `json:"lines_removed,omitempty"`
    ModelName        string  `json:"model_name,omitempty"`
    IssueNumber      int     `json:"issue_number,omitempty"`
    IssueSource      string  `json:"issue_source,omitempty"`
    LinearProjectID  string  `json:"linear_project_id,omitempty"`
}
```

**Verification**:
- Validate `X-Pilot-Signature` header (HMAC-SHA256, same scheme as Pilot's outbound)
- Validate `X-Pilot-Event` header equals `task.completed`
- Return 200 OK on success, 401 on bad signature

**Tasks**:
- [ ] Create `internal/pilot/` package (webhook handler, event types, service interface)
- [ ] HMAC-SHA256 signature verification (match Pilot's signing scheme)
- [ ] Register route in `internal/api/app.go`
- [ ] Add `pilot_webhook_secret` to config
- [ ] Store raw events in `pilot_events` table for audit trail
- [ ] Tests with httptest

**Files**:
- `internal/pilot/webhook/handler.go` — HTTP handler
- `internal/pilot/webhook/handler_test.go` — tests
- `internal/pilot/domain/events.go` — event types
- `internal/pilot/service/event_service.go` — business logic
- `internal/pilot/repository/event_repository.go` — persistence
- `internal/api/app.go` — route registration
- `internal/config/config.go` — new config field
- `db/migrations/034_pilot_events.sql` — events table

**Effort**: Medium (3-4 hours)

---

### Phase 3: Invoice Item Enrichment (LinearInvoices repo)

**Goal**: Map Pilot execution events to invoice line items with metadata.

**Logic**:
1. Pilot event arrives with `LinearProjectID` or `IssueNumber`
2. Resolve project → client via existing `client_projects` table
3. Look up pricing config for client (hourly, fixed, size-based)
4. Create a `pending_invoice_item` record with:
   - Description: task title + PR link
   - Rate: from pricing config
   - Quantity: based on pricing model (hours from duration, or count=1 for fixed)
   - Metadata: full execution metrics (JSON column)
5. When invoice is generated (manual or recurring), pending items are included

**New table**: `pending_invoice_items`
```sql
CREATE TABLE pending_invoice_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    client_id UUID REFERENCES clients(id),
    pilot_event_id UUID REFERENCES pilot_events(id),
    linear_project_id TEXT,
    description TEXT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    rate DECIMAL(10,2) NOT NULL DEFAULT 0,
    amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    metadata JSONB,           -- full execution metrics
    status TEXT NOT NULL DEFAULT 'pending',  -- pending|invoiced|skipped
    invoiced_at TIMESTAMPTZ,
    invoice_id UUID REFERENCES invoices(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Pricing resolution**:
```
if pricing_model == "hourly":
    quantity = ceil(duration_ms / 3600000)  // round up to hours
    rate = client hourly rate
elif pricing_model == "fixed":
    quantity = 1
    rate = fixed rate per task
elif pricing_model == "size_based":
    quantity = 1
    rate = rate_for_estimate(linear_ticket_estimate)
```

**Tasks**:
- [ ] Create migration for `pending_invoice_items` table
- [ ] Create `PilotEventService` that processes events → pending items
- [ ] Resolve client from `LinearProjectID` via `client_projects` join
- [ ] Apply pricing config resolution (existing `PricingConfigService`)
- [ ] Integrate with invoice creation flow — pull pending items when generating invoice
- [ ] Add "Pilot executions" section to invoice detail UI
- [ ] Tests

**Files**:
- `db/migrations/035_pending_invoice_items.sql`
- `internal/pilot/service/event_service.go` — event → pending item logic
- `internal/pilot/repository/pending_items_repository.go` — CRUD
- `internal/domain/invoice/service/invoice_service.go` — pull pending items during creation
- `linearinvoices-client/src/components/invoice/PilotExecutions.tsx` — UI component

**Effort**: Medium-Large (4-6 hours)

---

### Phase 4: Dashboard & Visibility (LinearInvoices repo)

**Goal**: Show Pilot execution data in LinearInvoices UI for transparency.

**Tasks**:
- [ ] "Pilot Executions" tab on client detail page — list of completed tasks with metrics
- [ ] Pending items indicator on invoice creation page — "3 Pilot tasks ready to invoice"
- [ ] Execution cost vs billable amount comparison (AI cost margin)
- [ ] Webhook status page (last received, errors, connection test button)

**Files**:
- `linearinvoices-client/src/app/dashboard/clients/[id]/pilot/page.tsx`
- `linearinvoices-client/src/components/invoice/PendingPilotItems.tsx`
- `linearinvoices-client/src/app/dashboard/settings/integrations/page.tsx`

**Effort**: Medium (3-4 hours)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Communication | Shared DB, REST API calls, Webhooks | Webhooks | Decoupled, Pilot already has webhook system, no shared infra needed |
| Auth between services | API key, OAuth, HMAC | HMAC-SHA256 | Pilot already signs webhooks with HMAC. Proven pattern in both codebases |
| Invoice item creation | Immediate (auto-create invoice), Deferred (pending items) | Deferred (pending items) | User controls when to invoice. Batch multiple tasks. Review before sending |
| Client resolution | By email, by Linear project, by task title | By Linear project ID | Both services already map Linear projects. Most reliable join key |
| Pricing | Fixed per-task, pass-through AI cost, config-based | Config-based (existing pricing configs) | LinearInvoices already has flexible pricing resolution. Reuse it |
| Metadata storage | Separate columns, JSON blob, external | JSONB column | Flexible, queryable, no schema migration for new Pilot fields |

---

## Configuration

**Pilot side** (`~/.pilot/config.yaml`):
```yaml
webhooks:
  enabled: true
  endpoints:
    - name: "linearinvoices"
      url: "https://api.linearinvoices.com/api/v1/webhooks/pilot"
      secret: "${PILOT_WEBHOOK_SECRET}"
      events:
        - "task.completed"
      enabled: true
```

**LinearInvoices side** (`config.yaml` or env):
```yaml
integrations:
  pilot:
    enabled: true
    webhook_secret: "${PILOT_WEBHOOK_SECRET}"  # must match Pilot's signing secret
    auto_create_pending_items: true
    default_pricing_model: "hourly"  # fallback if no client pricing config
```

---

## Dependencies

**Requires**:
- [ ] Both services deployed and accessible to each other (HTTPS)
- [ ] Shared webhook secret configured in both services
- [ ] At least one client in LinearInvoices mapped to a Linear project that Pilot works on

**Blocks**:
- [ ] Future: Midday integration (LinearInvoices → Midday invoice sync, separate task)

---

## Verify

### Phase 1 (Pilot):
```bash
cd ~/Projects/startups/pilot
make test && make lint
# Verify webhook payload includes new fields
```

### Phase 2-4 (LinearInvoices):
```bash
cd ~/Projects/startups/linearinvoices/linearinvoices-service
go test ./internal/pilot/... -v
go test ./... -count=1

# E2E: configure Pilot webhook → trigger task → verify pending item created
```

---

## Done

Observable outcomes that prove completion:

- [ ] Pilot `task.completed` webhook includes execution metrics (tokens, cost, duration, files)
- [ ] LinearInvoices `POST /api/v1/webhooks/pilot` receives and validates events
- [ ] Pending invoice items auto-created from Pilot events with correct client mapping
- [ ] Invoice generation includes pending Pilot items
- [ ] Execution metadata visible in LinearInvoices dashboard
- [ ] HMAC verification works end-to-end
- [ ] `make test` passes in both repos

---

## Rollout Plan

1. **Phase 1** first (Pilot side) — backward compatible, just adds fields to existing webhook
2. **Phase 2** next (LI webhook handler) — can deploy independently, just starts receiving
3. **Phase 3** (item enrichment) — the actual value delivery
4. **Phase 4** (UI) — polish, can ship incrementally

Each phase is independently deployable. No big-bang required.

---

## Future Extensions

- **Midday sync**: LinearInvoices → Midday for financial overview (separate TASK)
- **Cost margin alerts**: Warn when AI execution cost exceeds invoice amount
- **Auto-invoice**: Option to auto-generate and send invoice on task completion (skip pending state)
- **Multi-adapter**: Support Jira/Asana task IDs in addition to Linear
- **Billing dashboard**: Cross-service view showing Pilot cost vs revenue per client

---

**Last Updated**: 2026-03-04
