# GH-349: Add Telegram Polling to Gateway Mode

**Status**: ✅ Complete
**Created**: 2026-02-02
**Completed**: 2026-02-02
**Priority**: P1 (blocking user workflow)

---

## Context

**Problem**:
When running `pilot start` with `--linear` or `--jira` flags, the system uses gateway mode (HTTP server for webhooks). In this mode, Telegram polling does NOT start even if `--telegram` flag is provided. Users who need both Linear webhooks AND Telegram bot interaction cannot use them together.

**Current Behavior**:
```bash
# This works (polling mode - no Linear)
pilot start --telegram --github --dashboard
# → Telegram polling starts ✓

# This FAILS (gateway mode - has Linear)
pilot start --telegram --github --linear --dashboard
# → Telegram polling does NOT start ✗
```

**Root Cause**:
- `cmd/pilot/main.go:300` - Mode selection logic:
  ```go
  if noGateway || (!hasLinear && !hasJira && (hasTelegram || hasGithubPolling)) {
      return runPollingMode(...)  // Telegram Handler created here
  }
  // Gateway mode - no Telegram Handler
  ```
- `runPollingMode()` creates `telegram.Handler` and calls `StartPolling()`
- Gateway mode (`pilot.New()`) only creates `telegram.Client` for alerts, not the Handler

**Goal**:
Enable Telegram polling in gateway mode when `--telegram` flag is provided alongside `--linear` or `--jira`.

**Success Criteria**:
- [x] `pilot start --telegram --linear` starts Telegram polling
- [x] `pilot start --telegram --jira` starts Telegram polling
- [x] Telegram commands work in gateway mode
- [x] No regression in polling-only mode
- [x] Dashboard shows "📱 Telegram polling active" in gateway mode

---

## Implementation Summary

### Changes Made

1. **internal/pilot/pilot.go**:
   - Added `telegramHandler *telegram.Handler` field to Pilot struct
   - Added `telegramRunner *executor.Runner` field for task execution
   - Added `Option` functional options pattern for configuration
   - Added `WithTelegramHandler()` option to enable Telegram in gateway mode
   - Updated `New()` to accept options and initialize Telegram handler
   - Updated `Start()` to call `telegramHandler.StartPolling()`
   - Updated `Stop()` to call `telegramHandler.Stop()`

2. **cmd/pilot/main.go**:
   - Create runner when Telegram polling is enabled in gateway mode
   - Configure runner with quality gates and model routing
   - Pass `pilot.WithTelegramHandler()` option to `pilot.New()`
   - Show "📱 Telegram polling active" in startup banner

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Handler location | cmd/pilot vs internal/pilot | internal/pilot | Keeps gateway mode self-contained, matches other adapters |
| Runner access | Pass to New() vs create in Pilot | Pass via Option | Runner is created in cmd with quality gates configured, Pilot shouldn't create its own |
| Option pattern | Direct params vs functional options | Functional options | Clean API, backward compatible, extensible |

---

## Verify

```bash
# Test 1: Gateway mode with Telegram
pilot start --telegram --linear --dashboard
# Expected: Dashboard shows "📱 Telegram polling active"

# Test 2: Send message to bot
# Expected: Bot responds, can execute tasks

# Test 3: Clean shutdown
# Ctrl+C
# Expected: "Telegram polling stopped" in logs

# Test 4: Regression - polling mode still works
pilot start --telegram --github --dashboard
# Expected: Works as before
```

---

## Done

Observable outcomes that prove completion:

- [x] `pilot start --telegram --linear` logs "Telegram polling started in gateway mode"
- [x] Bot responds to messages in gateway mode
- [x] Headless mode shows "📱 Telegram polling active"
- [x] All tests pass
- [x] Build succeeds

---

## Notes

Related issue: User reported this during v0.11.11 testing (SQLite fix release).

The workaround (no longer needed):
```bash
# Terminal 1: Linear webhooks
pilot start --linear

# Terminal 2: Telegram + GitHub polling
pilot start --telegram --github --dashboard --no-gateway
```

---

**Last Updated**: 2026-02-02
