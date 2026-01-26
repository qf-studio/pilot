# TASK-05: Bot Singleton Detection

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Priority**: Low

---

## Context

**Problem**:
When running `pilot telegram` while another instance is already running, the bot spams errors:
```
telegram API error: Conflict: terminated by other getUpdates request;
make sure that only one bot instance is running (code: 409)
```

User has to manually `pkill -f "pilot telegram"` first.

**Goal**:
Detect existing instance and handle gracefully.

---

## Implementation (Option B + C)

### Added to `internal/adapters/telegram/client.go`:
- `ErrConflict` - Sentinel error for 409 conflicts
- `CheckSingleton(ctx)` - Calls getUpdates with timeout=0 to detect conflicts

### Added to `internal/adapters/telegram/handler.go`:
- `CheckSingleton(ctx)` - Wrapper method for handler

### Added to `cmd/pilot/main.go`:
- `--replace` flag for telegram command
- `killExistingTelegramBot()` - Finds and kills existing process
- Singleton check before starting poll loop

### New test file `internal/adapters/telegram/client_test.go`:
- Tests for `CheckSingleton` behavior
- Tests for `ErrConflict` sentinel error

---

## Usage

```bash
# Normal start (fails if another instance running)
pilot telegram -p ~/Projects/myproject

# Auto-replace existing instance
pilot telegram --replace -p ~/Projects/myproject
```

**Error message when conflict detected:**
```
❌ Another bot instance is already running

   Options:
   • Kill it manually:  pkill -f 'pilot telegram'
   • Auto-replace:      pilot telegram --replace
```

---

## Acceptance Criteria

- [x] Clear error message on conflict (not spam)
- [x] `--replace` flag kills existing and starts new
- [x] Single attempt, then exit (no retry loop on 409)

---

**Last Updated**: 2026-01-26
