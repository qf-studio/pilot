# TASK-08: Docs Website Coverage Gaps (v2.38.11 → v2.53.0)

**Status**: 🚧 In Progress
**Created**: 2026-03-04

---

## Context

**Problem**:
Docs site was last comprehensively updated at v2.38.11. Multiple features shipped since then have no or minimal documentation.

**Related issues**:
- GH-1994: ARCHITECTURE.md rewrite (internal docs)
- GH-1999: Version sync across docs site (version strings only)
- This task: NEW content for undocumented features

---

## Gap Analysis

### New Pages Needed

| # | Page | Features to Cover | Priority |
|---|------|-------------------|----------|
| 1 | `features/self-healing.mdx` | CI error pattern learning (v2.49.0), retry-with-decomposition, merged PR guard (v2.53.0) | P1 |
| 2 | `concepts/adapters.mdx` | Common adapter registry (v2.30.0), ProcessedStore, unified interface, adapter lifecycle | P2 |
| 3 | `features/execution-modes.mdx` | Sequential/parallel/auto modes, scope-overlap guard, union-find grouping | P2 |
| 4 | `features/web-dashboard.mdx` | HTTP API at `/api/v1/*`, WebSocket streaming, gateway in polling mode | P2 |

### Existing Pages to Enhance

| # | Page | Section to Add | Priority |
|---|------|----------------|----------|
| 5 | `features/quality-gates.mdx` | AC verification in self-review (v2.49.0) — add to self-review section | P1 |
| 6 | `features/autopilot.mdx` | Expand auto-rebase section — conflict detection flow, UpdateBranch API, fallback to close-and-retry | P2 |
| 7 | `features/dashboard.mdx` | Web dashboard API section — REST endpoints, WebSocket log streaming, SSE | P2 |

---

## GitHub Issues

### Issue 1: `features/self-healing.mdx` — CI Pattern Learning & Merged PR Guard
```
New docs page covering Pilot's self-healing capabilities:
- CI error pattern learning: how Pilot learns from failure logs (PatternExtractor, confidence boosting)
- Error categories: compilation, test failures, lint, dependency, runtime
- Retry with decomposition: signal:killed → DecomposeForRetry()
- Merged PR guard: poller checks for merged PRs before retry dispatch
- Configuration: retry settings, pattern learning config
```

### Issue 2: `concepts/adapters.mdx` — Common Adapter Registry
```
New docs page explaining the adapter architecture:
- Common Adapter interface (Register, ProcessedStore, parallel exec)
- Supported adapters: GitHub, Linear, Jira, Asana, GitLab, AzureDevOps, Discord, Plane
- ProcessedStore: persistent dedup across restarts
- State transitions: how adapters move issues through lifecycle
- Adding a new adapter: interface requirements
```

### Issue 3: `features/execution-modes.mdx` — Execution Mode Auto-Switching
```
New docs page for execution modes:
- Sequential mode: one task at a time, wait for PR merge
- Parallel mode: concurrent execution with semaphore
- Auto mode: scope-based switching via groupByOverlappingScope()
- Scope overlap guard: prevents file conflicts between parallel tasks
- Configuration: orchestrator.execution_mode, max_concurrent
```

### Issue 4: `features/web-dashboard.mdx` — Web Dashboard & API
```
New docs page for the web dashboard:
- HTTP gateway at /dashboard (embedded React frontend)
- REST API: /api/v1/tasks, /api/v1/autopilot, /api/v1/history
- WebSocket log streaming
- Gateway in polling mode (background HTTP server)
- Desktop app connection via /health endpoint
```

### Issue 5: Enhance `quality-gates.mdx` — AC Verification
```
Add section to existing quality-gates.mdx:
- Acceptance criteria extraction from issue body
- Self-review verification step: checks each AC was implemented
- How ACs flow: issue body → prompt_builder → execution → self-review check
```

### Issue 6: Enhance `autopilot.mdx` — Auto-Rebase Details
```
Expand auto-rebase section in autopilot.mdx:
- Conflict detection flow in handleMergeConflict()
- GitHub UpdatePullRequestBranch API call
- Fallback: close PR → new branch from latest main → retry
- CI fix dependency annotations (Depends on: #N)
```

### Issue 7: Enhance `dashboard.mdx` — Web API Section
```
Add web API section to existing dashboard.mdx:
- /api/v1/* REST endpoints
- WebSocket log streaming endpoint
- SSE for real-time updates
- Gateway background server in polling mode
```

---

**Last Updated**: 2026-03-04
