# TASK-04: Telegram UX Improvements

**Status**: 🚧 In Progress
**Created**: 2026-01-26
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
- [ ] Greetings/casual messages get friendly response (no execution)
- [ ] Questions answered via Claude (read-only, no code changes)
- [ ] Tasks confirmed before execution (with cancel option)
- [ ] Clean output without internal signals
- [ ] Result shows what was actually created/changed

---

## Implementation Plan

### Phase 1: Intent Detection
**Goal**: Classify incoming messages into categories

**Tasks**:
- [ ] Add `detectIntent(message string) Intent` function
- [ ] Categories: `greeting`, `question`, `task`, `command`
- [ ] Pattern matching for greetings (hi, hello, hey, etc.)
- [ ] Pattern matching for questions (what, how, why, where, ?)
- [ ] Default to `task` for action words (create, add, fix, update, etc.)

**Files**:
- `internal/adapters/telegram/intent.go` (new)
- `internal/adapters/telegram/handler.go` (integrate)

### Phase 2: Response Handlers
**Goal**: Handle each intent type appropriately

**Tasks**:
- [ ] `handleGreeting()` - Friendly response, no execution
- [ ] `handleQuestion()` - Use Claude to answer (read-only prompt)
- [ ] `handleTask()` - Confirm then execute
- [ ] `handleCommand()` - Already exists (/help, /status)

**Files**:
- `internal/adapters/telegram/handler.go`

### Phase 3: Confirmation Flow
**Goal**: Confirm before executing tasks

**Tasks**:
- [ ] Send confirmation message with task summary
- [ ] Add inline keyboard: [✅ Execute] [❌ Cancel]
- [ ] Handle callback queries
- [ ] Store pending tasks (map[chatID]pendingTask)
- [ ] Timeout after 5 minutes (auto-cancel)

**Files**:
- `internal/adapters/telegram/handler.go`
- `internal/adapters/telegram/client.go` (add callback handling)

### Phase 4: Clean Output
**Goal**: User-friendly result messages

**Tasks**:
- [ ] Strip internal signals (EXIT_SIGNAL, LOOP COMPLETE, NAVIGATOR_STATUS)
- [ ] Extract file changes from result
- [ ] Format clean completion message
- [ ] Show: files created/modified, PR link, brief summary

**Files**:
- `internal/adapters/telegram/handler.go`
- `internal/adapters/telegram/formatter.go` (new)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Intent detection | LLM-based, regex, keyword | Keyword + patterns | Fast, no API cost, sufficient accuracy |
| Confirmation UI | Reply keyboard, inline keyboard, text reply | Inline keyboard | Better UX, buttons stay with message |
| Question handling | Full Claude, read-only Claude, simple answers | Read-only Claude prompt | Can answer codebase questions without changes |

---

## Message Flow

```
User sends message
        ↓
detectIntent(message)
        ↓
┌───────┴───────┐───────────┐───────────┐
greeting      question      task       command
    ↓              ↓           ↓            ↓
friendlyMsg   askClaude   confirm→exec   /help,/status
```

---

## Example Interactions

**Greeting:**
```
User: Hi there
Bot: 👋 Hey! I'm Pilot bot. Send me a task or use /help.
```

**Question:**
```
User: What files handle authentication?
Bot: 🔍 Looking...
Bot: Authentication is handled in:
     • internal/auth/handler.go
     • internal/middleware/auth.go
```

**Task:**
```
User: Add a logout endpoint
Bot: 📋 Task: Add a logout endpoint
     Project: /pilot

     [✅ Execute] [❌ Cancel]

User: [clicks Execute]
Bot: 🚀 Executing...
Bot: ✅ Done (45s)
     📁 Modified: internal/auth/handler.go
     ➕ Added: LogoutHandler function
```

---

## Verify

```bash
# Run tests
make test

# Test intent detection
go test ./internal/adapters/telegram/... -v

# Manual test via Telegram
# Send: "Hi" → Should get greeting
# Send: "What is X?" → Should get answer
# Send: "Add file.txt" → Should get confirmation
```

---

## Done

- [ ] `internal/adapters/telegram/intent.go` exports `DetectIntent()`
- [ ] Greetings get friendly response (no execution)
- [ ] Questions answered via read-only Claude
- [ ] Tasks show confirmation with buttons
- [ ] Output is clean (no internal signals)
- [ ] All tests pass

---

## Completion Checklist

- [ ] Implementation finished
- [ ] Tests written and passing
- [ ] Manual testing via Telegram
- [ ] Commit and push

---

**Last Updated**: 2026-01-26
