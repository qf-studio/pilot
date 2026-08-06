# feat(gateway): C14 pilot leg — approval read surface + POST decision endpoint on the DecisionRecorder seam

## Problem

S4 C14 ("Needs You") requires the console to see pending approvals and record decisions. Today neither is possible: the gateway's `/api/v1` route table (`internal/gateway/server.go:231-238`) is GET-only with no approval data in any response, and `approval_request_id` — the key `DecisionRecorder` decisions are recorded against — is exposed on no HTTP surface. This is the "pilot (1 endpoint)" leg the roadmap §S4 row names (it is actually one read route + one write route).

All facts verified at origin/main 2026-08-06 (research pass).

## Context (verified)

- **Seam**: `DecisionRecorder` (`internal/approval/types.go:80-82`) — `RecordDecision(ctx, requestID string, decision Decision, by string) error`. Implemented by `*approval.Manager.RecordDecision` (`internal/approval/manager.go:412-436`) → `memory.Store.SetApprovalDecision` (`internal/memory/store.go:1472-1496`), keyed on `executions.approval_request_id`. Wired today only to Slack/Telegram handlers (`WithDecisionRecorder`, call sites `cmd/pilot/main.go:609,636,1697,1718`, `internal/pilot/pilot.go:236`).
- Restart-survival of pending approvals is real and integration-tested (`internal/autopilot/restart_approval_*_integration_test.go`) — the rehydration path has a pending-approvals predicate (grep log msg `"rehydrated pending approvals"`); reuse that predicate, do not invent a new one.
- `handleAutopilot` (`server.go:555`) already serves PR `stage` incl. `awaiting_approval`, but not `approval_request_id`, and the console proxy can't reach it (console-side allowlist — NOT this task's problem).
- The gateway has an `Authenticator` — **verify how sibling `/api/v1` routes are wrapped before wiring; the new routes must be authed identically** (the decision route is a state-changing action; it must never be weaker-authed than the rest of the surface).

## Acceptance

1. **Read**: `GET /api/v1/approvals` → pending approval requests, JSON array of `{requestId, executionId, taskId, projectPath, prNumber, prUrl, requestedAt}` (fields nullable where the ledger lacks them). Pending = the same predicate the approval rehydration path uses. Project-scoped by the same mechanism as sibling dashboard routes (`dashboardProjectPath` filter where applicable).
2. **Write**: `POST /api/v1/approvals/{requestId}/decision` body `{"decision":"approve"|"reject","by":"<caller identity>"}` → delegates to the `DecisionRecorder` seam (the `Manager`, NOT a direct store write — channel notification/cleanup behavior must match a Slack/Telegram decision). 404 unknown/already-decided requestId · 400 bad decision value · same auth as sibling routes. Response echoes the recorded decision.
3. Wiring: gateway gets the recorder via a setter (`SetDecisionRecorder`), installed in `cmd/pilot/main.go` next to the existing `WithDecisionRecorder` call sites — both gateway-mode and polling-mode paths (the GH-4738 lesson: wire BOTH, and add a composed-wiring test, not just unit tests).
4. Tests: pending list matches seeded store · decision through the endpoint persists identically to a channel decision (assert `approval_decision/_at/_by` columns) · double-decide 404/409 · auth rejected without token · composed wiring test through the real mux.
5. `make build` / `make test` / `make lint` green; no changes to Slack/Telegram approval flows.

## Scope fence

No console/UI changes (console leg is a separate follow-up gated on this merging) · no proxy allowlist changes (console repo) · no approval-policy changes (who may decide is the caller's concern; `by` is recorded verbatim) · no new auth mechanism.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap §S4 row (`.agent/system/saas-roadmap.md:37`) — "C14 full … decision endpoint on `DecisionRecorder` seam"; sync design §9
- Research pass 2026-08-06 (marker `2026-08-05_wave3-...md` § 08-06 session)

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4748
- **Shipped**: PR#4752 merged 2026-08-06 12:38Z. Post-merge review: SQL/indexing/race-vs-executor clean; 17 gateway + 3 store tests. Follow-ups filed: pilot#4756 (gateway auth never enabled in production wiring — first mutating endpoint on an unauthenticated surface; blocks non-loopback exposure), pilot#4757 (TOCTOU double-decide + unlinked-request 500-forever trap). API-surface traps recorded in TASK-452 (console leg, console#109): response echoes `approve/reject` while DB persists `approved/rejected`; `decidedAt` is gateway clock, not DB timestamp.
