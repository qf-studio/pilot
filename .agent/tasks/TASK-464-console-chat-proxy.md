# TASK-464: C17 console leg — operator chat proxy to the tenant daemon (C14 idiom)

**Status**: ⛔ **GATED — filed UNLABELED 2026-08-11** → [console#115](https://github.com/qf-studio/pilot-console/issues/115). **Add the `pilot` + `no-decompose` labels only after [pilot#4835](https://github.com/qf-studio/pilot/issues/4835) merges** (spec freezes on its contract; re-anchor the body first if #4835's shape changed in review).
**Created**: 2026-08-11
**Assignee**: Pilot (after gate)

## Summary

Console passthrough for the operator chat panel, following the **C14 idiom** (typed client + API-package route — NOT a C13 allowlist widening): new `internal/chat` client cloned from `approvals.Client.do` (running check → gateway token 60s cache → EC2 private IP → bearer → `StatusError`), new `internal/chatapi` routes `POST /api/v1/chat/messages` (authenticate + CSRFGuard, C14 ladder verbatim, `sender` from principal) and `GET /api/v1/chat/conversations/{id}/events` (relay verbatim). Org-scoped via `resolveOrgInstance` (bind-once), not instanceID-in-path.

## Key decisions

- No streaming work needed console-side — daemon API is poll-drain (see TASK-463). The reqlog `statusRecorder` Flusher gap stays untouched (recorded for any future SSE leg: `reqlog.go:59-67` lacks `Flush`/`Unwrap`).
- Pure passthrough — no console-side chat history/mirror table; daemon owns conversation state.
- Approvals stay on the board decision route.

## Refs

- Daemon leg: pilot#4835 (TASK-463) — contract source of truth
- Idiom: C14 — `internal/approvals/client.go` + `internal/boardapi/decision.go` (console PR #111)
