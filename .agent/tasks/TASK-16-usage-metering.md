# TASK-16: Usage Metering & Billing Foundation

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-27
**Category**: Monetization

---

## Context

**Problem**:
No way to track usage for billing. Can't charge customers fairly.

**Goal**:
Accurate usage metering as foundation for subscription/usage-based billing.

---

## Billable Units

### Primary Metrics
| Unit | Description | Typical Price |
|------|-------------|---------------|
| Task | One ticket execution | $1.00 |
| Token | Claude API tokens | Pass-through + 20% |
| Compute Min | Execution time | $0.01/min |

### Secondary Metrics
- Storage (memory, logs)
- API calls
- Team seats
- Projects

---

## Pricing Models

### 1. Usage-Based (Implemented for MVP)
```
$0 base + $1.00/task + tokens at cost + 20%
```

### 2. Tiered Subscription
```
Free:  10 tasks/month
Pro:   100 tasks/month @ $29/mo
Team:  500 tasks/month @ $99/mo
```

### 3. Enterprise
```
Custom pricing, volume discounts, SLA
```

---

## Implementation

### Phase 1: Metering ✅
- Track all billable events via `usage_events` table
- Store task, token, compute, storage, API call events
- Support user and project filtering

### Phase 2: Reporting ✅
- CLI commands: `pilot usage summary|daily|projects|events|export`
- JSON/CSV export for accounting
- Usage threshold alerts (foundation)

### Phase 3: Integration (Future)
- Stripe integration
- Invoice generation
- Payment processing

---

## Files Changed

- `internal/memory/metering.go` - NEW: Metering types, cost calculations, store methods
- `internal/memory/metering_test.go` - NEW: Tests for metering functionality
- `internal/memory/store.go` - Extended: Added usage_events table migration
- `cmd/pilot/usage.go` - NEW: CLI usage commands (summary, daily, projects, events, export)
- `cmd/pilot/main.go` - Extended: Registered usage command

---

## Data Model

```sql
CREATE TABLE usage_events (
  id TEXT PRIMARY KEY,
  timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
  user_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  event_type TEXT NOT NULL,  -- 'task', 'token', 'compute', 'storage', 'api_call'
  quantity INTEGER DEFAULT 0,
  unit_cost REAL DEFAULT 0.0,
  total_cost REAL DEFAULT 0.0,
  metadata TEXT,  -- JSON for extra data
  execution_id TEXT  -- Links to executions table
);
```

---

## CLI Commands

```bash
# Show usage summary (billing-ready)
pilot usage summary --days 30

# Show daily breakdown
pilot usage daily --days 7

# Show usage by project
pilot usage projects --days 30

# Show raw events
pilot usage events --type task --limit 50

# Export to JSON/CSV
pilot usage export --format json -o usage.json
pilot usage export --format csv -o usage.csv
```

---

## API Functions

```go
// Record usage event
store.RecordUsageEvent(event)

// Record complete task usage (task + tokens + compute)
store.RecordTaskUsage(executionID, userID, projectID, durationMs, tokensInput, tokensOutput)

// Get aggregated summary
store.GetUsageSummary(query)

// Get daily breakdown
store.GetDailyUsage(query)

// Get by project
store.GetUsageByProject(query)

// Get raw events
store.GetUsageEvents(query, limit)

// Check thresholds
store.CheckUsageThresholds(userID)
```

---

## Pricing Constants

```go
const (
    PricePerTask               = 1.00   // $1.00 per task
    TokenInputPricePerMillion  = 3.60   // $3.00 + 20% margin
    TokenOutputPricePerMillion = 18.00  // $15.00 + 20% margin
    PricePerComputeMinute      = 0.01   // $0.01 per minute
    PricePerGBMonth            = 0.10   // $0.10 per GB storage
    PricePerAPICall            = 0.001  // $0.001 per API call
)
```

---

## Commit

```
feat(billing): add usage metering and billing foundation (TASK-16)
```

---

**Dependencies**: TASK-13 (Execution Metrics) ✅
**Enables**: TASK-17 (Stripe Integration), Customer Billing
