# fix(gateway): chat API — gateway-mode wiring hole + latestSeq for reset detection

**Status**: ✅ Merged 2026-08-11 → PR#4848 (15:13Z). Post-merge review agent ran at ~18:30Z — check PR#4848 comments (incl. whether latestSeq shape matches console#115 §3).
**Created**: 2026-08-11
**Assignee**: Pilot

## Context

PR#4838 (GH-4835) shipped the C17 operator chat API (poll-drain web transport for the shared comms brain). Post-merge review confirmed the contract but found two defects and two riders. The console leg (pilot-console#115) anchors on this endpoint — D2 blocks it from implementing the documented reset rule.

- **D1 (medium-high)** — chat silently absent in gateway mode when no polling adapter is enabled: `needsPollingInfra` (cmd/pilot/main.go:663) omits chat, so `gwRunner` is nil → `WithChatHandler(nil, …)` (cmd/pilot/main.go:1072) → guard at internal/pilot/pilot.go:815 skips `SetChatAPI` → routes 404 while cmd/pilot/main.go:1073 logs "Chat API enabled in gateway mode". A webhooks-only gateway daemon with the console chat panel hits this.
- **D2 (medium)** — reset detection unimplementable: the documented rule says a client seeing `after` > latest seq must re-poll from 0, but the GET events response never exposes the latest seq — internal/adapters/web/api.go:124 discards it. After daemon restart or 1h conversation expiry, stale-cursor clients poll into silence, then get partial replay with no signal. Same gap hides drop-oldest truncation past the 500-event cap.
- **D3 (low)** — `events` serializes as `null` (not `[]`) for unknown/expired conversations (internal/gateway/messenger.go:241 returns nil slice).
- **D4 (low)** — `TestAPI_Dispatch_UsesGivenContextNotCancelledOne` (internal/adapters/web/api_test.go:429) never cancels a context; the spec's cancellation-survival behavior is unasserted.

## Implementation

1. **D1**: make chat count toward runner construction — include chat in `needsPollingInfra` (or build the chat runner independently of polling infra); a gateway-mode daemon with chat enabled and zero polling adapters must serve both chat routes. Kill the false "enabled" log on any path that doesn't wire the handler.
2. **D2**: add `latestSeq` to `chatEventsResponse` (internal/gateway/chat.go:120-124) and stop discarding it in internal/adapters/web/api.go:124. Document: `after` > `latestSeq` ⇒ client resets to 0; `after` < oldest retained seq ⇒ events resume from oldest (truncation is observable via gap to `latestSeq`).
3. **D3**: return `[]` not `null` for the events array in all cases.
4. **D4**: make the cancellation test actually cancel the request context and assert dispatch completes.

Out of scope: streaming/SSE (poll-drain is the decided transport — gateway WriteTimeout 15s); auth changes; comms-brain behavior; console-side work (pilot-console#115 consumes this).

## Acceptance

- Integration-style test: gateway mode, chat enabled, NO polling adapters → POST returns 202 and GET drains events (was 404).
- GET response carries `latestSeq`; a stale-cursor scenario test (restart-simulated fresh buffer, client `after`=N>0) shows the client can detect reset from the response alone.
- Unknown conversation returns `{"events": []}` with the JSON array present.
- All existing chat/web tests and `-race` stay green.

## Refs

- Review verdict: https://github.com/qf-studio/pilot/pull/4838#issuecomment-5253756860 (D1–D4)
- Spec lineage: GH-4835 / .agent/tasks/TASK-463-gateway-chat-api.md · console leg: pilot-console#115
