# feat(approval): project identity on approval records — Request.Project, approval_pending.project, GET surface

## Problem

Approval records carry no project identity: `approval.Request` (`internal/approval/types.go:32-55`) has no project field and `approval_pending` (`internal/memory/store.go:341-352`) no project column. Consequences today: the gateway approvals surface (PR#4752) attributes rows only via the best-effort `executions.approval_request_id` join and **drops unlinked rows entirely in project-scoped mode** (`internal/gateway/approvals.go:92-113`); the console approval mirror (console#109, in flight) inherits that degraded attribution; and per-project routing (roadmap leg B3) has no persisted project on the record.

## Context (verified 2026-08-06, origin/main)

- Sole production submit site: `submitAsyncApprovalRequest` (`internal/autopilot/controller.go:2772-2790`) — the controller already knows its project: `c.projectPath` (wired via `WithProjectPath`, `cmd/pilot/main.go:1979` default repo, `:2023` projects loop).
- Canonicalize the path before persisting (the #4297 cross-project collision lesson: project scoping keys on canonicalized paths).
- Migration idiom: append `ALTER TABLE approval_pending ADD COLUMN project TEXT DEFAULT ''` to the migrations list — the runner tolerates duplicate-column re-runs (`internal/memory/store.go:459-462`).
- Row write sites (post-TASK-454 rebase): handler inserts at `internal/approval/telegram.go:371-383` and `slack.go:265-277`; `memory.PendingApproval` struct at `internal/memory/approval_store.go:13-24`.
- GET surface: `handleApprovals` (`internal/gateway/approvals.go:62`) — response DTO gains `project`; scoped filtering prefers the column when non-empty, keeping the executions join as fallback for pre-migration rows. This FIXES the scoped-mode row-dropping for rows that have a project but no execution linkage.
- Coordination: console#109's ingest consumes this GET — the new field is additive JSON; a coordination note is posted on console#109 (done by the dispatcher, not this task).

## Acceptance

1. `Request.Project` populated (canonicalized `c.projectPath`) at the submit site; both handlers copy it into the row; `PendingApproval.Project` round-trips through insert/load.
2. Migration appended per the store idiom; idempotent on second boot.
3. GET `/api/v1/approvals` emits `project` (JSON `project`, nullable/omitted when empty). Scoped mode: rows with a matching `project` column are included even when the executions join misses; empty-project legacy rows keep today's join-based behavior exactly.
4. Tests: submit→insert→GET carries project · legacy empty-project row still attributes via join · scoped filtering by column · migration idempotency (existing `store_test.go` seeding patterns).
5. `make build` / `make test` / `make lint` green.

## Scope fence

No routing/gating changes (leg B3) · no console-repo changes · no changes to the POST decision path · no backfill of existing rows (they expire within 24h anyway) · rebase on TASK-454 if it is in flight (same handler functions).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap: `.agent/system/approval-architecture-roadmap.md` (leg B2 — unblocks console#109 attribution)
- PR#4752 post-merge review (scoped-mode row-dropping finding)

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4773
