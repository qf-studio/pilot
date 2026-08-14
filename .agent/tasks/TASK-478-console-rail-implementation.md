# TASK-478: Console rail implementation program — 11 approved designs → shipped surfaces

**Status**: 🚀 ACTIVE — plan approved 2026-08-14; t0 legs dispatching (CON-1 console · UI-1 shell)
**Created**: 2026-08-14
**Plan of record**: approved plan 2026-08-14 (research pass: console bff0520 + ui inventory; both at origin/main)

## Program

Implement every approved design page (`pilot-console-ui/design/`: dashboard-v4 · board-v1 · issue-v1 · connections-v1 · instances-v1 · onboarding-v1 · settings-v1 · approvals-v1 · chat-v1 · mobile-v1; docs-v1 deferred to TASK-466) as Pilot-executed issues. 17 issues: 5 console + 12 ui. Backend is nearly complete already — C14 approvals, C17 chat relay, v4 dashboard aggregate, and daemon execution-events (#4749, closed) are all live; most legs are UI against existing contracts.

**Rules**: one issue = one PR · UI legs dispatch only after their console contract merges, bodies re-anchored to merged contracts (never design assumptions) · every new adapter method ships Wire* types + mock + fixtures in lockstep · UI merges gate on `sops/quality/real-stack-verify-gates-ui-merges.md`.

## Legs + status

### Console chain (`qf-studio/pilot-console`)

| Leg | Title | Size | Gate | Status |
|-----|-------|------|------|--------|
| CON-1 | `GET /api/v1/approvals` list + `GET /api/v1/instances/{id}/events` | S/M | — | 🚀 dispatched |
| CON-2 | Activity journal v2 (migration 0011, typed `kind`, dispatch/status/decision writers) | L 🔒 | CON-1 merged (chain) | 📋 |
| CON-3 | Proxy allowlist tails `executions/{id}/events` + `tasks/{taskId}/events` (#4749 shipped) | S | CON-2 (chain) | 📋 |
| CON-4 | `PUT /api/v1/org` rename | S | CON-3 (chain) | 📋 |
| CON-5 | `POST /api/v1/billing/portal-session` | S | **founder Stripe inputs** — floats | 📋 |

### UI chain (`qf-studio/pilot-console-ui`)

| Leg | Title | Size | Gate | Status |
|-----|-------|------|------|--------|
| UI-1 | v4 shell: icon rail + header + chat region + token sync | L 🔒 | — | 🚀 dispatched |
| UI-2 | Wire `decideCard` + decision error UX (kills 501 stub + pinning test) | S/M | UI-1 (chain) | 📋 |
| UI-3 | Chat side panel + ⤢ expanded overlay | L 🔒 | UI-2 (chain) | 📋 |
| UI-4 | Board restyle per board-v1 | M/L | UI-3 (chain) | 📋 |
| UI-5 | Bell popover (needs-you + activity glance; fixes lossy activity mapping) | M | CON-1 + CON-2 | 📋 |
| UI-6 | Dashboard v4 (existing `GET /api/v1/dashboard` + C13 proxy metrics tiles) | M/L | UI-3 | 📋 |
| UI-7 | Instances v1 (vitals via proxy, events timeline, deprovision, provision gate) | M | CON-1 | 📋 |
| UI-8 | Connections v1 | M | — (slack leg) | 📋 |
| UI-9 | Onboarding v1 (signup+org merge, get-started checklist) | M/L | — (slack leg) | 📋 |
| UI-10 | Settings v1 (rename / members read-only / plan+past_due / danger zone) | M/L | CON-4 (portal btn: CON-5) | 📋 |
| UI-11 | Issue page (routed detail + run timeline via CON-3) | L 🔒 | CON-3 | 📋 |
| UI-12 | Mobile v1 (media queries + 4-tab bar; 5 frames) | M/L 🔒 | all surfaces | 📋 |

🔒 = `<!-- pilot:no-decompose -->`

## Deferred (no issues authored)

Docs page (TASK-466 — daemon contract research first) · comments read-model · invites · org delete · connection delete · SSE streaming · sleep/wake wake-on-send · real plan pricing (founder gate; $299 placeholder ships).

## Risks (watch during review)

1. UI-1 scope bleed into view internals — fence + full-suite-green acceptance.
2. CON-2 decision-route double-journal — migrate the existing writer (`decision.go:136`), don't add a second.
3. Three-repo chain #4749→CON-3→UI-11 — quote merged shapes verbatim downstream.
4. 501 pinning tests (`httpAdapter.spec.ts:696`) — UI-2/UI-11 bodies explicitly order replacement.
5. UI-9 touches session bootstrap + router guards — review carefully.

## Dispatched

- CON-1: https://github.com/qf-studio/pilot-console/issues/118 (2026-08-14)
- UI-1: https://github.com/qf-studio/pilot-console-ui/issues/50 (2026-08-14)

## Refs

- Designs: `pilot-console-ui/design/*.html` (all approved 2026-08-14)
- Contracts: C14 console PR#111 · C17 console PR#117 · daemon #4749 (closed) · `internal/dashboardapi/handlers.go`
- Program plan: `~/.claude/plans/great-we-need-to-imperative-dijkstra.md` (session 2026-08-14)
