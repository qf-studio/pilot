# feat(board): C14 console leg — approval mirror ingest, NeedsYou, decision proxy

> **✅ COMPLETE — archived 2026-08-19.** Backend leg: console#109 → PR#111 merged 2026-08-06. UI leg (decideCard 501 kill): ui#52 merged. Sole residual — the #109 coordination note's `project`-field preference (pilot#4773) was never wired into ingest — filed as [console#182](https://github.com/qf-studio/pilot-console/issues/182), dispatched 2026-08-19. The 08-19 marker's "S4 wave-4 remainder: TASK-452" entry was stale.

## Problem

The daemon now exposes approvals over HTTP (pilot PR#4752, merged 2026-08-06: `GET /api/v1/approvals` + `POST /api/v1/approvals/{requestId}/decision`), but the console has no approval surface at all: no `approval_mirror` table, no ingest, `cardDTO.NeedsYou` hardcoded `false` (`internal/boardapi/handlers.go:119`), and the UI's `decideCard` throws a local 501 (`pilot-console-ui` `src/lib/api/httpAdapter.ts:630-633`). The UI read path is already live (`needsYou` comes off the wire, Needs-You lane + approve/reject buttons fully built) — this leg is the entire backend.

All console/UI facts verified at origin/main 2026-08-06 (research pass). Daemon API surface verified against merged PR#4752.

## Context (verified)

- **Upstream read**: `GET /api/v1/approvals` → JSON array of `{requestId, executionId|null, taskId, projectPath|null, prNumber|null, prUrl|null, requestedAt}`; empty list serializes as `[]`. Rows with `executionId: null` exist (linkage is best-effort) — mirror them; see the decision caveat below.
- **Upstream write**: `POST /api/v1/approvals/{requestId}/decision` body `{"decision":"approve"|"reject","by":"<identity>"}` → 200 `{requestId, decision, by, decidedAt}` · 404 unknown/expired · 409 already decided · 500 when the request has no linked execution (upstream defect, pilot follow-up filed — map to 502 and keep the mirror row). The response `decision` echoes the verb; the daemon *persists* `approved`/`rejected` — never compare the two forms.
- **Instance reach**: reuse the proxy's resolution recipe — `GatewayTokenGetter` (`internal/proxy/proxy.go:96`, SSM `PILOT_GATEWAY_TOKEN` per org, 60s cache) + `fleet.PrivateIPFromDescribe` + port 9090, `Authorization: Bearer <token>`, 10s timeout; `proxy.go:274-336` shows the full sequence including the ObservedRunning gate. The public instance-proxy route stays GET-only — the decision call is a server-side upstream call from boardapi, NOT a new proxy tail (`proxy.go:1-7` fences this deliberately).
- **Mirror pattern**: migrations are numbered, next is `0010_*` (+`.down.sql`), auto-embedded via `internal/db/migrate.go:19-20`; store methods go on the narrow consumer interfaces (`syncingest.boardStore` `worker.go:58-72`, `boardapi.BoardStore` `routes.go:38-60`), not the concrete store; worker conventions in `internal/syncingest/worker.go` (single-replica assumption, inflight set, ticker).
- **Card→task join**: `handleExecution` (`internal/boardapi/dispatch.go:478-543`) already joins card→daemon execution — the approval join MUST use the same card→task identity rule so the two surfaces agree. Instance resolution: `resolveOrgInstance` (`dispatch.go:551-578`, newest-wins).
- **Route pattern to copy**: `mux.Handle("POST /api/v1/board/cards/{id}/dispatch", authenticate(bff.CSRFGuard(...)))` (`internal/boardapi/routes.go:140-155`); handler ladder per `dispatch.go:478+` (disabled→principal→org/board→card, 404-never-403). The UI already sends `X-Requested-With: pilot-console` + cookies on every mutating call — zero UI plumbing needed for CSRF.

## Acceptance

1. **Migration `0010`**: `approval_mirror` — unique `(org_id, request_id)`; `instance_id`, `task_id`, `execution_id NULL`, `project_path NULL`, `pr_number NULL`, `pr_url NULL`, `requested_at`, `first_seen_at`, `last_seen_at`. Down migration included.
2. **Ingest**: for each org with a running instance, poll `GET /api/v1/approvals` on an interval (default 60s, config-shaped like syncingest); upsert the current pending set, delete mirror rows no longer pending upstream. Instance unreachable → keep existing rows, warn (stale-but-visible beats flapping).
3. **NeedsYou**: `toCardDTO` sets `NeedsYou=true` when a live `approval_mirror` row joins the card via the same card→task rule as `handleExecution`. Unjoinable mirror rows light no card (harmless).
4. **Decision route**: `POST /api/v1/board/cards/{id}/decision` body `{"decision":"approve"|"reject"}` (anything else → 400). Auth + CSRF per house pattern; `by` sent upstream = authenticated principal (Email, falling back to Subject), never client-supplied. No live mirror row for the card → 404. Instance not running → 409. Upstream 200 → delete the mirror row, surface the decision in the activity feed the same way `handleDispatch` journals (the UI calls `refreshActivity()` after deciding and expects an entry — mock reference `mockAdapter.ts:939-953`), return the refreshed cardDTO. Upstream 404/409 → 409 + delete the mirror row (it is decided/gone). Upstream 5xx/timeout → 502, keep the row.
5. **Tests**: ingest upsert/delete cycle against a fake gateway server · NeedsYou join · decision-route ladder (401 · 403 CSRF · 404 unknown card/no mirror row · 400 bad verb · 409 not-running/already-decided · 502 upstream failure · success side effects) · migration up/down. Any new metric threads org positionally (C15 lesson: assert position, not presence).
6. CI green.

## Scope fence

No UI-repo changes (the `decideCard` httpAdapter implementation is a separate ui leg gated on this merging — the 501-pinning test `httpAdapter.spec.ts:696-703` changes there, not here) · public instance proxy untouched (stays GET-only; no `allowedTails` change) · no approval-policy logic (any authenticated org member may decide — same trust level as dispatch) · no daemon-side changes (the 500-on-unlinked upstream defect is a pilot-repo follow-up).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap §S4 (`.agent/system/saas-roadmap.md:37`) — "C14 full: `approval_pending` → `approval_mirror` mirroring feeding 'Needs You' + decision endpoint on `DecisionRecorder` seam"
- Daemon surface: pilot PR#4752 (issue #4748 / TASK-449)
- Research pass 2026-08-06 (console + ui at origin/main; PR#4752 post-merge review)

- **Dispatched**: https://github.com/qf-studio/pilot-console/issues/109
