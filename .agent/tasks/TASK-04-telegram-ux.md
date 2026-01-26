# TASK-04: Telegram UX Improvements

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Assignee**: Pilot (self-improvement)

---

## Context

**Problem**:
Current Telegram bot executes ALL messages as code tasks, including:
- Greetings ("Hi there") → 43s wasted execution
- Questions ("What is our next issue?") → Should answer, not execute
- Internal signals leak to user (`EXIT_SIGNAL: true`, `LOOP COMPLETE`)
- No confirmation before potentially destructive tasks
- No result details shown (just "Duration: 20s")

**Goal**:
Make Telegram bot intelligent - detect intent, confirm tasks, show clean output.

**Success Criteria**:
- [x] Greetings/casual messages get friendly response (no execution)
- [x] Questions answered via Claude (read-only, no code changes)
- [x] Tasks confirmed before execution (with cancel option)
- [x] Clean output without internal signals
- [x] Result shows what was actually created/changed

---

## Implementation Summary

### Phase 1: Intent Detection ✅
- `intent.go`: `DetectIntent()` classifies messages into:
  - `greeting` - hi, hello, hey, etc.
  - `question` - what is, how do, where is, ends with ?
  - `task` - create, add, fix, update, etc.
  - `command` - /help, /status, /cancel
- Pattern matching with regex for task references (TASK-xx, #123, etc.)
- Tests: 32 cases covering all intent types

### Phase 2: Response Handlers ✅
- `handleGreeting()` - Friendly response with username
- `handleQuestion()` - Read-only Claude prompt, answers about codebase
- `handleTask()` - Confirmation flow before execution
- `handleCommand()` - /help, /status, /cancel support

### Phase 3: Confirmation Flow ✅
- Inline keyboard: [✅ Execute] [❌ Cancel]
- Pending task storage with 5-minute expiry
- Callback query handling for button clicks
- Text fallback: "yes/no" replies also work

### Phase 4: Clean Output ✅
- `formatter.go`: Strips internal signals
  - EXIT_SIGNAL, LOOP COMPLETE, NAVIGATOR_STATUS blocks
  - Phase:, Progress:, Iteration: lines
- Extracts file changes (created/modified/added/deleted)
- Shows commit SHA (short) and PR link when available

---

## Files Created/Modified

| File | Action | Description |
|------|--------|-------------|
| `internal/adapters/telegram/intent.go` | Created | Intent detection with pattern matching |
| `internal/adapters/telegram/intent_test.go` | Created | 32 test cases for intent detection |
| `internal/adapters/telegram/handler.go` | Modified | Response handlers, confirmation flow |
| `internal/adapters/telegram/client.go` | Modified | Callback query support |
| `internal/adapters/telegram/formatter.go` | Created | Clean output formatting |
| `internal/adapters/telegram/formatter_test.go` | Created | 19 test cases for formatting |

---

## Test Results

```
=== RUN   TestDetectIntent (32 cases)
--- PASS: TestDetectIntent
=== RUN   TestCleanInternalSignals (8 cases)
--- PASS: TestCleanInternalSignals
=== RUN   TestExtractSummary (6 cases)
--- PASS: TestExtractSummary
=== RUN   TestFormatTaskResult (4 cases)
--- PASS: TestFormatTaskResult
... (51 total tests)
PASS
ok  	github.com/alekspetrov/pilot/internal/adapters/telegram
```

---

## Example Interactions

**Greeting:**
```
User: Hi there
Bot: 👋 Hey there! I'm Pilot bot.

Send me a task to execute, or ask me a question about the codebase.

*Examples:*
• `Create a hello.py file`
• `What files handle auth?`
• `/help` for more info
```

**Question:**
```
User: What files handle authentication?
Bot: 🔍 *Looking into that...*
Bot: Authentication is handled in:
     • internal/auth/handler.go
     • internal/middleware/auth.go
```

**Task:**
```
User: Add a logout endpoint
Bot: 📋 *Confirm Task*

`TG-1706270400`

*Task:* Add a logout endpoint
*Project:* `/pilot`

Execute this task?

[✅ Execute] [❌ Cancel]

User: [clicks Execute]
Bot: 🚀 *Executing*
`TG-1706270400`

Add a logout endpoint

Bot: ✅ *Task completed*
`TG-1706270400`

⏱ Duration: 45s
📝 Commit: `abc1234`

📄 *Summary:*
📁 Created: `logout_handler.go`
📝 Modified: `routes.go`
```

---

## Done

- [x] `internal/adapters/telegram/intent.go` exports `DetectIntent()`
- [x] Greetings get friendly response (no execution)
- [x] Questions answered via read-only Claude
- [x] Tasks show confirmation with buttons
- [x] Output is clean (no internal signals)
- [x] All tests pass (51 tests)

---

## Completion Checklist

- [x] Implementation finished
- [x] Tests written and passing (51 tests)
- [x] Lint clean (0 issues in telegram package)
- [x] Documentation updated

---

**Last Updated**: 2026-01-26
