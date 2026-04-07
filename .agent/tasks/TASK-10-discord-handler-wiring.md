# TASK-10: Wire Discord Handler into Polling Mode

**Status**: ✅ Completed
**Created**: 2026-03-05
**Completed**: 2026-03-13

---

## What Was Built

Wired Discord adapter into Pilot's polling mode so `pilot start --discord` connects to Discord Gateway and processes messages. Fixed two critical bugs found during first live test.

---

## Implementation

### Phase 1: Config + Poller Registration
**Completed**: 2026-03-05

- Config section already existed in `configs/pilot.example.yaml`
- `poller_discord.go` already registered in poller registry
- `main.go` also had a duplicate handler init (TASK-10 original PR)

### Phase 2: First Live Test — Two Bugs Found
**Completed**: 2026-03-13

**Bug 1 — Bot self-loop (v2.79.5)**:
- `msg.Author.ID == "bot"` was a placeholder — compared against literal string
- Discord sends `author.bot: true` for bot users
- Fix: Added `Bot bool` to `User` struct, check `msg.Author.Bot`
- Commit: `3baaba8b`

**Bug 2 — Duplicate handler (v2.79.6)**:
- Discord handler created in both `main.go` AND `poller_discord.go`
- Two WebSocket connections → every message processed twice
- Fix: Removed `main.go` copy, poller registry is single owner
- Commit: `f9cae29b`

### Phase 3: Alerts + Gateway — Deferred
Deferred to TASK-12 (production hardening).

---

## Files Modified

- `internal/adapters/discord/types.go` — Added `Bot bool` field to `User` struct
- `internal/adapters/discord/handler.go` — Fixed bot message filter to use `Author.Bot`
- `internal/adapters/discord/handler_test.go` — Updated test for `Bot` field
- `cmd/pilot/main.go` — Removed duplicate Discord handler init

---

## Done

- [x] `pilot start --discord` connects to Discord Gateway
- [x] Bot receives and processes messages
- [x] Task confirmation flow works (Execute/Cancel buttons)
- [x] Bot ignores its own messages
- [x] Single handler instance (no duplicates)
- [x] Build and tests pass
- [ ] Alerts engine wiring → TASK-12
- [ ] Gateway mode support → TASK-12

---

## Follow-up

**TASK-12** (GH-2116): Discord production hardening — reconnection, mention stripping, project resolution, rate limits, and 10 more issues.

---

**Last Updated**: 2026-03-13
