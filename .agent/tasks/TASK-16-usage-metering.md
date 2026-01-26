# TASK-16: Usage Metering & Billing Foundation

**Status**: 📋 Planned
**Created**: 2026-01-26
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
| Task | One ticket execution | $0.50-2.00 |
| Token | Claude API tokens | Pass-through + 20% |
| Compute Min | Execution time | $0.01/min |

### Secondary Metrics
- Storage (memory, logs)
- API calls
- Team seats
- Projects

---

## Pricing Models

### 1. Usage-Based (Recommended for MVP)
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

### Phase 1: Metering
- Track all billable events
- Store in metering table
- Daily/monthly aggregation

### Phase 2: Reporting
- Usage dashboard
- Export for accounting
- Alerts at thresholds

### Phase 3: Integration
- Stripe integration
- Invoice generation
- Payment processing

---

## Data Model

```sql
CREATE TABLE usage_events (
  id TEXT PRIMARY KEY,
  timestamp DATETIME,
  user_id TEXT,
  project_id TEXT,
  event_type TEXT,  -- 'task', 'token', 'compute'
  quantity INTEGER,
  unit_cost REAL,
  metadata JSON
);
```

---

**Dependencies**: TASK-13 (Execution Metrics)
