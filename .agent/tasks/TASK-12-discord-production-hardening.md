# TASK-12: Discord Production Hardening

**Status**: 🚧 In Progress
**Created**: 2026-03-13
**GitHub Issue**: [GH-2118](https://github.com/qf-studio/pilot/issues/2118) (replaces GH-2116/2117 which failed from direct-to-main conflict)
**Assignee**: Pilot (auto-pick via `pilot` label)

---

## Context

**Problem**:
Discord adapter connects and responds but has 13 issues found during first live test that will break under production use. No reconnection, mention strings leak into task descriptions, hardcoded project path, mutex deadlocks under rate limits, global progress callback races.

**Goal**:
Make Discord adapter production-grade with parity to Telegram/GitHub adapters.

**Success Criteria**:
- [ ] Bot survives WebSocket disconnects (auto-reconnect + resume)
- [ ] Task descriptions are clean (no `<@BOT_ID>` mentions)
- [ ] Tasks execute in correct project directory
- [ ] No deadlocks under Discord rate limits
- [ ] Concurrent tasks get independent progress updates

---

## Implementation Plan

### Phase 1: Critical Fixes (must-have)

**Goal**: Fix the 5 issues that will break in production

**Tasks**:

#### 1.1 Reconnection (transport.go)
- [ ] Add reconnection loop in `StartListening` with exponential backoff
- [ ] Use `Resume()` for resumable close codes (4000-4009)
- [ ] Full re-`Connect()` for non-resumable codes
- [ ] Remove dead close code detection at line 196 (gorilla/websocket surfaces these as `ReadJSON` errors)
- [ ] Max reconnect attempts before giving up (configurable, default 10)

**Files**: `internal/adapters/discord/transport.go`, `internal/adapters/discord/handler.go`

#### 1.2 Mention Stripping (handler.go)
- [ ] Strip `<@BOT_ID>` prefix from `msg.Content` before `handleTask()`
- [ ] Get bot user ID from READY event payload (`user.id` field)
- [ ] Store bot ID in `GatewayClient` or `Handler`
- [ ] Strip pattern: `<@{botUserID}>` + optional leading/trailing whitespace

**Files**: `internal/adapters/discord/handler.go`, `internal/adapters/discord/transport.go`

#### 1.3 Project Resolution (handler.go)
- [ ] Accept `ProjectPath` in `HandlerConfig` (from config default project)
- [ ] Wire `projectPath` from `PollerDeps` in `poller_discord.go`
- [ ] Optionally detect project from message content (like Telegram)
- [ ] Fall back to default project from config

**Files**: `internal/adapters/discord/handler.go`, `cmd/pilot/poller_discord.go`

#### 1.4 Mutex Deadlock Fix (handler.go)
- [ ] `cleanupExpiredTasks`: collect expired task IDs under lock, release lock, then send messages
- [ ] Same pattern for any future code that sends API calls under lock

**Files**: `internal/adapters/discord/handler.go`

#### 1.5 Per-Task Progress Callback (handler.go)
- [ ] Replace global `h.runner.OnProgress()` with per-task callback
- [ ] Option A: Use `task.OnProgress` field if runner supports it
- [ ] Option B: Multiplex in handler — register by taskID, dispatch in callback
- [ ] Ensure cleanup on task completion (no leaked callbacks)

**Files**: `internal/adapters/discord/handler.go`

### Phase 2: Significant Fixes

**Goal**: Fix reliability and correctness issues

**Tasks**:

#### 2.1 Rate Limit Handling (client.go)
- [ ] Parse `X-RateLimit-Remaining`, `X-RateLimit-Reset` headers in `doRequest()`
- [ ] On 429: read `Retry-After` header, sleep, retry
- [ ] Wire `RateLimitConfig` from types.go (currently defined but unused)
- [ ] Max retries on rate limit (default 3)

**Files**: `internal/adapters/discord/client.go`

#### 2.2 Task ID Collision (handler.go)
- [ ] Change `time.Now().Unix()` → `time.Now().UnixNano()` or use atomic counter
- [ ] Format: `DISCORD-{timestamp}-{counter}` for guaranteed uniqueness

**Files**: `internal/adapters/discord/handler.go`

#### 2.3 Interaction Response Type (handler.go)
- [ ] Change type 4 (`CHANNEL_MESSAGE_WITH_SOURCE`) → type 6 (`DEFERRED_UPDATE_MESSAGE`)
- [ ] Removes visible "Processing..." message on button click

**Files**: `internal/adapters/discord/handler.go`

#### 2.4 Double-Close Panic (handler.go, transport.go)
- [ ] Wrap `close(h.stopCh)` in `sync.Once` in `Handler.Stop()`
- [ ] Wrap `close(g.stopCh)` in `sync.Once` in `GatewayClient.Close()`

**Files**: `internal/adapters/discord/handler.go`, `internal/adapters/discord/transport.go`

#### 2.5 ProcessedStore Integration
- [ ] Add Discord to common ProcessedStore (like other adapters)
- [ ] Persist pending task state across restarts
- [ ] Dedup: don't create duplicate pending tasks for same message

**Files**: `internal/adapters/discord/handler.go`

### Phase 3: Parity + Polish

**Goal**: Feature parity with Telegram/GitHub adapters

**Tasks**:

#### 3.1 Startup Banner (poller_discord.go)
- [ ] Add `fmt.Println("🎮 Discord bot started")` in `CreateAndStart`
- [ ] Conditional on `!dashboardMode` (check how other pollers handle this)

**Files**: `cmd/pilot/poller_discord.go`

#### 3.2 DM Support (handler.go)
- [ ] In `isAllowed()`: if `guildID` is empty (DM), skip guild check
- [ ] Only check channel allowlist for DMs

**Files**: `internal/adapters/discord/handler.go`

#### 3.3 Alerts Engine Wiring
- [ ] Register Discord as alert channel type
- [ ] Add `Discord *DiscordChannelConfig` to alert channel config
- [ ] Follow Telegram/Slack pattern in alerts engine setup

**Files**: `cmd/pilot/main.go`, `internal/alerts/`

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Reconnection | Library (discordgo) vs hand-rolled | Hand-rolled | Already have transport.go, just needs loop + backoff |
| Bot ID source | Config vs READY event | READY event | Zero-config, Discord provides it automatically |
| Progress callback | Global vs per-task | Per-task (mux in handler) | Avoids runner API change, scoped to Discord |
| Rate limiting | Token bucket vs header-based | Header-based | Discord tells you exact limits, no guessing |

---

## Dependencies

**Requires**:
- TASK-10 completed (basic wiring) ✅
- Discord bot with MESSAGE_CONTENT intent enabled ✅

**Blocks**:
- Discord becoming a reliable production adapter

---

## Verify

```bash
# Build
make build

# Unit tests
go test ./internal/adapters/discord/ -v -count=1

# Integration (manual)
# 1. Kill network mid-task → bot reconnects
# 2. Send "@Pilot do X" → description is "do X"
# 3. Task runs in correct project dir
# 4. Rapid messages → no deadlock
# 5. Two channels submit tasks → both get progress
# 6. Button click → no "Processing..." message
# 7. Restart Pilot → no panic
```

---

## Done

Observable outcomes:
- [ ] Bot reconnects after WebSocket drop within 30s
- [ ] `<@BOT_ID>` stripped from all task descriptions
- [ ] Tasks execute in configured project directory
- [ ] `cleanupExpiredTasks` doesn't hold lock during API calls
- [ ] Concurrent tasks in different channels get independent progress
- [ ] No 429 errors under normal usage
- [ ] `Stop()` safe to call multiple times
- [ ] All tests pass

---

**Last Updated**: 2026-03-13
