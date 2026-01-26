# TASK-12: Pilot Cloud (Hosted)

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Self-hosted Pilot requires infrastructure setup, Claude Code CLI installation, and maintenance. Many teams want AI-powered development without ops burden. A hosted version removes friction and enables features not possible with self-hosted (multi-tenant patterns, managed compute).

**Goal**:
Build Pilot Cloud - a hosted SaaS version of Pilot with managed infrastructure, OAuth-based onboarding, and usage-based pricing.

**Success Criteria**:
- [ ] Teams can sign up and connect repos without self-hosting
- [ ] OAuth flow for Linear/GitHub/Jira
- [ ] Secure sandboxed execution environment
- [ ] Usage-based billing
- [ ] Multi-tenant architecture

---

## Research

### Competitive Landscape

| Product | Model | Pricing | Differentiation |
|---------|-------|---------|-----------------|
| **Devin** | Hosted AI engineer | $500/mo | Full agent, expensive |
| **Cursor** | IDE + AI | $20/mo | Code-focused, not autonomous |
| **GitHub Copilot Workspace** | Hosted planning | ~$19/mo | Planning only, no execution |
| **Pilot Cloud** | Hosted autonomous | Usage-based | Full autonomy, affordable |

### Architecture Options

| Component | Self-Hosted | Cloud |
|-----------|-------------|-------|
| Gateway | User's machine | Managed K8s |
| Executor | Local Claude CLI | Sandboxed containers |
| Storage | SQLite | PostgreSQL + S3 |
| Auth | API keys | OAuth 2.0 |
| Webhooks | User's URL | Managed endpoints |

### Execution Environment

| Option | Pros | Cons |
|--------|------|------|
| AWS Lambda | Serverless, scales | Cold start, time limits |
| ECS Fargate | Container, flexible | More management |
| Fly.io Machines | Fast start, GPUs | Smaller ecosystem |
| Modal | GPU-optimized | Python-centric |

### Security Requirements

- Code never persists beyond task execution
- Network isolation between tenants
- Secrets encrypted at rest and in transit
- Audit logging for compliance
- SOC 2 / GDPR compliance path

---

## Implementation Plan

### Phase 1: Multi-Tenant Architecture
**Goal**: Support multiple organizations on shared infrastructure

**Tasks**:
- [ ] Design tenant isolation model
- [ ] Add org/user hierarchy to data model
- [ ] Implement tenant-aware queries
- [ ] Add org admin roles and permissions
- [ ] Create org onboarding flow

**Files**:
- `cloud/internal/tenants/` - Multi-tenant logic
- `cloud/internal/auth/` - Authentication
- `cloud/migrations/` - Schema changes

### Phase 2: OAuth Integration
**Goal**: One-click connection to project management tools

**Tasks**:
- [ ] Implement OAuth 2.0 flows for Linear, GitHub, Jira
- [ ] Store tokens securely (encrypted, rotatable)
- [ ] Handle token refresh
- [ ] Add connection management UI
- [ ] Support multiple integrations per org

**Files**:
- `cloud/internal/oauth/` - OAuth handlers
- `cloud/internal/integrations/` - Integration management

### Phase 3: Sandboxed Executor
**Goal**: Secure, isolated code execution

**Tasks**:
- [ ] Create execution container image (Claude CLI, git, common tools)
- [ ] Implement per-task container lifecycle
- [ ] Add resource limits (CPU, memory, time)
- [ ] Network isolation (egress controls)
- [ ] Log streaming to user

**Files**:
- `cloud/executor/Dockerfile` - Execution image
- `cloud/internal/sandbox/` - Container management
- `cloud/internal/streaming/` - Log streaming

### Phase 4: Billing & Usage
**Goal**: Usage-based pricing model

**Tasks**:
- [ ] Track task execution time and resources
- [ ] Implement usage metering
- [ ] Integrate Stripe for billing
- [ ] Add usage dashboards
- [ ] Create pricing tiers

**Files**:
- `cloud/internal/billing/` - Billing logic
- `cloud/internal/usage/` - Usage tracking

### Phase 5: Dashboard
**Goal**: Web UI for Pilot Cloud

**Tasks**:
- [ ] Project/task management UI
- [ ] Real-time task progress
- [ ] PR/commit history
- [ ] Settings and integrations
- [ ] Team management

**Files**:
- `cloud/web/` - Frontend (React/Next.js)

### Phase 6: Infrastructure
**Goal**: Production-ready deployment

**Tasks**:
- [ ] Kubernetes manifests / Terraform
- [ ] PostgreSQL (RDS/Cloud SQL)
- [ ] Redis for queues
- [ ] S3/GCS for artifacts
- [ ] CDN for web UI
- [ ] Monitoring (Datadog/Grafana)

**Files**:
- `cloud/infra/` - IaC configurations

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Execution | Lambda, ECS, Fly | Fly Machines | Fast start, good DX |
| Database | SQLite, Postgres, Planetscale | PostgreSQL | Reliable, multi-tenant ready |
| Billing | Stripe, Paddle | Stripe | Most flexible, good APIs |
| Frontend | React, Vue, Svelte | Next.js | SSR, API routes, ecosystem |
| Queue | Redis, SQS, Kafka | Redis + BullMQ | Simple, good for this scale |

---

## Pricing Model

| Tier | Tasks/mo | Price | Features |
|------|----------|-------|----------|
| **Free** | 10 | $0 | Single project, community support |
| **Pro** | 100 | $49/mo | 5 projects, priority support |
| **Team** | 500 | $199/mo | Unlimited projects, SSO, API |
| **Enterprise** | Custom | Custom | Self-hosted option, SLA, dedicated |

Usage overage: $0.50/task

---

## Configuration

```yaml
# Cloud deployment config
cloud:
  region: "us-east-1"

  executor:
    image: "pilot/executor:latest"
    timeout: 600  # 10 min max
    memory: "2Gi"
    cpu: "1"

  billing:
    provider: "stripe"
    webhook_secret: "${STRIPE_WEBHOOK_SECRET}"

  database:
    host: "${DB_HOST}"
    name: "pilot_cloud"
```

---

## Security

| Layer | Implementation |
|-------|----------------|
| Network | VPC isolation, private subnets |
| Execution | gVisor/Firecracker sandboxing |
| Data | Encryption at rest (AES-256) |
| Transit | TLS 1.3 everywhere |
| Auth | OAuth 2.0 + short-lived tokens |
| Audit | All actions logged |

---

## Dependencies

**Requires**:
- [ ] Core Pilot features stable
- [ ] Cloud infrastructure budget
- [ ] Legal/compliance review

**Blocks**:
- Commercial launch
- Enterprise sales

---

## Verify

```bash
# Local development
cd cloud && make dev

# Deploy to staging
make deploy-staging

# Run E2E tests
make e2e-test
```

---

## Done

Observable outcomes that prove completion:

- [ ] User can sign up at pilotdev.ai
- [ ] OAuth connects Linear/GitHub/Jira
- [ ] Tasks execute in sandboxed environment
- [ ] PRs created on user's repos
- [ ] Usage metered and billed
- [ ] Multi-tenant isolation verified
- [ ] Security audit passed

---

## Milestones

| Milestone | Target | Status |
|-----------|--------|--------|
| Alpha (internal) | Q2 2026 | Planned |
| Beta (waitlist) | Q3 2026 | Planned |
| GA | Q4 2026 | Planned |

---

## References

- [Fly.io Machines](https://fly.io/docs/machines/)
- [Stripe Billing](https://stripe.com/docs/billing)
- [gVisor Security](https://gvisor.dev/docs/)
- [SOC 2 Compliance](https://www.aicpa.org/soc4so)

---

**Last Updated**: 2026-01-26
