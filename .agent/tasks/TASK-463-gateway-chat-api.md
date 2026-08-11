# TASK-463: C17 pilot leg — gateway operator chat API (web transport for the comms brain)

**Status**: 🚀 Dispatched 2026-08-11 → [pilot#4835](https://github.com/qf-studio/pilot/issues/4835) (`pilot` + `no-decompose`)
**Created**: 2026-08-11
**Assignee**: Pilot

## Summary

Web transport for the existing `internal/comms` adapter brain (GH-2143): `WebMessenger` implementing `comms.Messenger` with a per-conversation seq-numbered event buffer, one new `BuildHandler` assembly (Platform `web`, ContextID `web:<conversationID>`), and two gateway routes on `apiMux` (bearer auth GH-4784 inherited): `POST /api/v1/chat/messages` (202, dispatch on daemon ctx) + `GET /api/v1/chat/conversations/{id}/events?after=<seq>`.

## Key decisions (research 2026-08-11, three-agent pass)

- **Poll-drain, NOT SSE/WS**: gateway `http.Server` WriteTimeout 15s kills SSE; WS endpoints sit outside bearer auth (pre-existing gap). Poll-drain also lets the console proxy reuse the C14 idiom verbatim (no Flusher/reqlog work console-side).
- **Approvals never route through the chat brain** (pitfall GH-4411/GH-4431) — console panel calls `POST /api/v1/approvals/{requestId}/decision` (#4748) directly.
- **Two gateway construction sites** must be wired (polling mode main.go + gateway mode internal/pilot/pilot.go) — GH-4784 precedent.
- **Scoped out**: shared CommandHandler degradations (`/run`, `/tasks`, `/brief`, `/nopr`, `/pr` funcs unwired in `comms.NewHandler` — web inherits Slack parity, not Telegram parity). Follow-up issue if the panel needs them.
- Buffer bounds: 500 events/conversation drop-oldest, 1h inactivity expiry; seq resets on daemon restart (client treats `after` > latest as reset).

## Refs

- Console leg: [console#115](https://github.com/qf-studio/pilot-console/issues/115) (TASK-464 — GATED on #4835 merging, unlabeled until then)
- Design: `design/dashboard-v4-spec.md` @ pilot-console-ui · marker `2026-08-11_design-program-v4-approved-t459-t461-complete.md` §6 (division of labor)
