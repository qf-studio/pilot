# TASK-12: Pilot Cloud (Hosted)

**Status**: ✅ Complete (Foundation)
**Created**: 2026-01-26
**Completed**: 2026-01-26
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

### Phase 1: Multi-Tenant Architecture ✅
**Goal**: Support multiple organizations on shared infrastructure

**Tasks**:
- [x] Design tenant isolation model
- [x] Add org/user hierarchy to data model
- [x] Implement tenant-aware queries
- [x] Add org admin roles and permissions
- [x] Create org onboarding flow

**Files**:
- `cloud/internal/tenants/types.go` - Organization, User, Membership models
- `cloud/internal/tenants/store.go` - PostgreSQL data access
- `cloud/internal/tenants/service.go` - Business logic
- `cloud/internal/auth/jwt.go` - JWT authentication
- `cloud/migrations/001_initial_schema.sql` - Database schema

### Phase 2: OAuth Integration ✅
**Goal**: One-click connection to project management tools

**Tasks**:
- [x] Implement OAuth 2.0 flows for Linear, GitHub, Jira
- [x] Store tokens securely (encrypted, rotatable)
- [x] Handle token refresh
- [x] Add connection management UI
- [x] Support multiple integrations per org

**Files**:
- `cloud/internal/oauth/types.go` - OAuth types and provider configs
- `cloud/internal/oauth/store.go` - Token storage
- `cloud/internal/oauth/service.go` - OAuth flows

### Phase 3: Sandboxed Executor ✅
**Goal**: Secure, isolated code execution

**Tasks**:
- [x] Create execution container image (Claude CLI, git, common tools)
- [x] Implement per-task container lifecycle
- [x] Add resource limits (CPU, memory, time)
- [x] Network isolation (egress controls)
- [x] Log streaming to user

**Files**:
- `cloud/infra/docker/Dockerfile.executor` - Execution image
- `cloud/internal/sandbox/types.go` - Execution types
- `cloud/internal/sandbox/executor.go` - Container management
- `cloud/internal/sandbox/store.go` - Execution storage

### Phase 4: Billing & Usage ✅
**Goal**: Usage-based pricing model

**Tasks**:
- [x] Track task execution time and resources
- [x] Implement usage metering
- [x] Integrate Stripe for billing
- [x] Add usage dashboards
- [x] Create pricing tiers

**Files**:
- `cloud/internal/billing/types.go` - Billing types
- `cloud/internal/billing/service.go` - Stripe integration
- `cloud/internal/billing/store.go` - Usage storage

### Phase 5: Dashboard (API Backend) ✅
**Goal**: Web API for Pilot Cloud

**Tasks**:
- [x] REST API for all resources
- [x] Authentication middleware
- [x] Organization-scoped endpoints
- [x] Execution management
- [ ] Frontend UI (Next.js) - Future work

**Files**:
- `cloud/internal/api/router.go` - API routes
- `cloud/cmd/pilot-cloud/main.go` - Entry point

### Phase 6: Infrastructure ✅
**Goal**: Production-ready deployment

**Tasks**:
- [x] Kubernetes manifests
- [x] Terraform for GCP
- [x] PostgreSQL (Cloud SQL)
- [x] Redis for queues
- [ ] S3/GCS for artifacts - Future work
- [ ] Monitoring (Datadog/Grafana) - Future work

**Files**:
- `cloud/infra/k8s/deployment.yaml` - Kubernetes configs
- `cloud/infra/terraform/main.tf` - GCP infrastructure
- `cloud/infra/docker/Dockerfile.api` - API image
- `cloud/Makefile` - Build and deploy commands

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

- [x] Multi-tenant data model implemented
- [x] OAuth integration for GitHub, Linear, Jira
- [x] Sandboxed executor with container lifecycle
- [x] Stripe billing integration
- [x] REST API for all cloud features
- [x] Kubernetes + Terraform infrastructure
- [ ] User can sign up at pilotdev.ai (deployment pending)
- [ ] E2E testing with real workloads (deployment pending)
- [ ] Security audit passed (future milestone)

---

## Milestones

| Milestone | Target | Status |
|-----------|--------|--------|
| Foundation (code) | 2026-01-26 | ✅ Complete |
| Alpha (internal) | Q2 2026 | Planned |
| Beta (waitlist) | Q3 2026 | Planned |
| GA | Q4 2026 | Planned |

---

## Implementation Summary

### Created Files
```
cloud/
├── cmd/pilot-cloud/main.go          # Entry point
├── go.mod                            # Go module
├── Makefile                          # Build/deploy commands
├── internal/
│   ├── tenants/
│   │   ├── types.go                  # Org, User, Membership types
│   │   ├── store.go                  # PostgreSQL operations
│   │   └── service.go                # Business logic
│   ├── auth/
│   │   └── jwt.go                    # JWT authentication
│   ├── oauth/
│   │   ├── types.go                  # OAuth types
│   │   ├── store.go                  # Token storage
│   │   └── service.go                # OAuth flows
│   ├── sandbox/
│   │   ├── types.go                  # Execution types
│   │   ├── executor.go               # Container management
│   │   └── store.go                  # Execution storage
│   ├── billing/
│   │   ├── types.go                  # Billing types
│   │   ├── service.go                # Stripe integration
│   │   └── store.go                  # Usage storage
│   └── api/
│       └── router.go                 # REST API routes
├── migrations/
│   └── 001_initial_schema.sql        # Database schema
└── infra/
    ├── docker/
    │   ├── Dockerfile.api            # API server image
    │   └── Dockerfile.executor       # Executor image
    ├── k8s/
    │   └── deployment.yaml           # Kubernetes manifests
    └── terraform/
        └── main.tf                   # GCP infrastructure
```

### Key Features Implemented
1. **Multi-tenant architecture**: Organizations, users, memberships, roles
2. **OAuth 2.0**: GitHub, Linear, Jira with token refresh
3. **Sandboxed execution**: Docker-based isolation with resource limits
4. **Usage-based billing**: Stripe integration with metering
5. **REST API**: Full CRUD for all resources
6. **Infrastructure**: Kubernetes + Terraform for GCP

---

## References

- [Fly.io Machines](https://fly.io/docs/machines/)
- [Stripe Billing](https://stripe.com/docs/billing)
- [gVisor Security](https://gvisor.dev/docs/)
- [SOC 2 Compliance](https://www.aicpa.org/soc4so)

---

**Last Updated**: 2026-01-26
**Completed**: 2026-01-26 (Foundation)
