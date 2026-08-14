# TASK-478: Console rail implementation program — 11 approved designs → shipped surfaces

**Status**: 🚀 ACTIVE — t0–t3 MERGED + REVIEWED, all APPROVE (console PR#119/121/123/125 · ui PR#51/53/55/57). **Console chain DONE except founder-gated CON-5** (Stripe inputs). t4 dispatched: UI-5 bell (ui#58). Remaining after: UI-6..12, all UI-repo.
**Created**: 2026-08-14
**Plan of record**: approved plan 2026-08-14 (research pass: console bff0520 + ui inventory; both at origin/main)

## Program

Implement every approved design page (`pilot-console-ui/design/`: dashboard-v4 · board-v1 · issue-v1 · connections-v1 · instances-v1 · onboarding-v1 · settings-v1 · approvals-v1 · chat-v1 · mobile-v1; docs-v1 deferred to TASK-466) as Pilot-executed issues. 17 issues: 5 console + 12 ui. Backend is nearly complete already — C14 approvals, C17 chat relay, v4 dashboard aggregate, and daemon execution-events (#4749, closed) are all live; most legs are UI against existing contracts.

**Rules**: one issue = one PR · UI legs dispatch only after their console contract merges, bodies re-anchored to merged contracts (never design assumptions) · every new adapter method ships Wire* types + mock + fixtures in lockstep · UI merges gate on `sops/quality/real-stack-verify-gates-ui-merges.md`.

## Legs + status

### Console chain (`qf-studio/pilot-console`)

| Leg | Title | Size | Gate | Status |
|-----|-------|------|------|--------|
| CON-1 | `GET /api/v1/approvals` list + `GET /api/v1/instances/{id}/events` | S/M | — | ✅ PR#119 merged+reviewed 08-14 |
| CON-2 | Activity journal v2 (migration 0011, typed `kind`, dispatch/status/decision writers) | L 🔒 | CON-1 merged (chain) | ✅ PR#121 merged+reviewed 08-14 |
| CON-3 | Proxy allowlist tails `executions/{id}/events` + `tasks/{taskId}/events` (#4749 shipped) | S | CON-2 (chain) | ✅ PR#123 merged+reviewed 08-14 |
| CON-4 | `PUT /api/v1/org` rename | S | CON-3 (chain) | ✅ PR#125 merged+reviewed 08-14 |
| CON-5 | `POST /api/v1/billing/portal-session` | S | **founder Stripe inputs** — floats | 📋 |

### UI chain (`qf-studio/pilot-console-ui`)

| Leg | Title | Size | Gate | Status |
|-----|-------|------|------|--------|
| UI-1 | v4 shell: icon rail + header + chat region + token sync | L 🔒 | — | ✅ PR#51 merged+reviewed 08-14 |
| UI-2 | Wire `decideCard` + decision error UX (kills 501 stub + pinning test) | S/M | UI-1 (chain) | ✅ PR#53 merged+reviewed 08-14 |
| UI-3 | Chat side panel + ⤢ expanded overlay | L 🔒 | UI-2 (chain) | ✅ PR#55 merged+reviewed 08-14 (pre-merge; size-held) |
| UI-4 | Board restyle per board-v1 | M/L | UI-3 (chain) | ✅ PR#57 merged+reviewed 08-14 (pre-merge; size-held) |
| UI-5 | Bell popover (needs-you + activity glance; fixes lossy activity mapping) | M | CON-1 + CON-2 ✅ | 🚀 [#58](https://github.com/qf-studio/pilot-console-ui/issues/58) |
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

## Dispatched / merged

- CON-1: console#118 → **PR#119 merged 14:18Z**, post-merge review APPROVE (notes: DTO nullables are `omitempty`-absent — UI legs treat absent as unjoined; N+1 card lookup fine at approval cardinality)
- UI-1: ui#50 → **PR#51 merged 14:20Z**, post-merge review APPROVE (notes: max-width wrapper gone — pages full-width until restyle legs; avatar menu lacks click-outside close)
- CON-2: console#120 → **PR#121 merged 15:00Z**, review APPROVE (double-journal dead — test asserts zero sync_conflicts writes; interim: new-kind rows render blandly through the SPA's lossy mapping until UI-5)
- UI-2: ui#52 → **PR#53 merged 14:53Z**, review APPROVE (pinning test replaced with 6 wire tests; notes: 400/401/503 rungs still rethrow silently — practically unreachable; **real-stack verify PENDING**, batch with UI-5's — decision fixtures are source-derived)
- CON-3: console#122 → **PR#123 merged 15:17Z**, review APPROVE (traversal-safe wildcard matcher; per-tail query forwarding correct; no notes)
- UI-3: ui#54 → **PR#55 pre-merge reviewed APPROVE + manually merged ~15:5xZ** (1,926-line diff was size-held awaiting a human; issue #54 auto-closed after merge). Follow-up candidates, not filed: overlay lacks a Tab-cycle trap · persistent 502/503 keeps status line at "box awake" (reconnecting-state candidate) · real-stack verify pending (batch with UI-2/UI-5)
- CON-4: console#124 → **PR#125 merged 16:07Z**, review APPROVE (create-parity validation tested; name-column-only proven e2e; no notes)
- UI-4: ui#56 → **PR#57 pre-merge reviewed APPROVE, merged ~16:2xZ** (size-held; daemon completed the merge concurrently with the release). Fence held completely — nothing faked; decision ladder EXTRACTED to `useCardDecision`, drawer + strip both consume it. Notes: tile face drops priority/labels/assignee (design-conformant, drawer retains) · per-caller composable state = strip/drawer don't share in-flight lock (server 409 covers it)
- UI-5: ui#58 (2026-08-14) — bell on merged CON-1/CON-2 anchors; ask rows explicitly OUT (no backend concept); AppShell inert-bell pinning test explicitly ordered replaced; reuses useCardDecision + formatRelativeTime

**Contract anchor from merged CON-3** (for UI-11): via `GET /api/v1/instances/{instanceID}/pilot/...` — `executions/{id}/events` → `[{stage, occurredAt, detail}]` (ASC, `stage` opaque) · `tasks/{taskId}/events?project=` → `{executionId, status, events:[...]}` (newest, C8 pick-newest rule; query forwarded).

**Contract anchors from merged CON-2** (for UI-5): `activityDTO{id, kind, cardId?, createdAt}` + per-kind fields — conflict `{field, boardValue?, remoteValue?, winner}` · dispatch `{provider, sequenceId}` · status `{from, to}` · decision `{decision, by}`.

**Contract anchors from merged CON-1** (for UI-5/UI-7 authoring): `approvalDTO{requestId, taskId, executionId?, projectPath?, prNumber?, prUrl?, requestedAt, cardId?, cardTitle?}` (nullable = absent) · `eventDTO{id, eventType, detail, createdAt}` · both under `{"approvals":[...]}` / `{"events":[...]}` envelopes, limit default 50 max 200.

## Refs

- Designs: `pilot-console-ui/design/*.html` (all approved 2026-08-14)
- Contracts: C14 console PR#111 · C17 console PR#117 · daemon #4749 (closed) · `internal/dashboardapi/handlers.go`
- Program plan: `~/.claude/plans/great-we-need-to-imperative-dijkstra.md` (session 2026-08-14)
