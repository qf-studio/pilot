# TASK-464: C17 console leg — operator chat proxy to the tenant daemon (C14 idiom)

**Status**: ✅ **MERGED 2026-08-11** → console PR#117 (18:20Z, released from awaiting_approval by founder; console#115 closed-completed). Full same-day chain: gate cleared (pilot#4835 → PR#4838) → body re-anchored to merged contract → labeled → implemented → held → approved → merged. Post-merge review agent ran ~18:30Z — check PR#117 comments (key question: does `latestSeq` from pilot PR#4848 pass through the relay). Archive after review verdict is read.
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
