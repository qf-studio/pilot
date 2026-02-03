# GH-355: Telegram LLM-Based Intent Detection with Conversation Context

**Status**: 🚧 In Progress
**Created**: 2026-02-03
**Branch**: `pilot/TG-1770110241` (partial work exists)

---

## Context

**Problem**:
Pilot's Telegram handler uses regex pattern matching for intent detection, causing misclassifications:
1. "Wow, let's commit changes first" → classified as Task (matches "commit")
2. "Check if it was executed" → classified as Task (no `?`, has action word)
3. No conversation memory → each message processed independently
4. No context passed to task execution → Claude Code doesn't know conversation history

**Goal**:
Add LLM-based intent classification using Claude Haiku with conversation context, following OpenClaw's approach.

**Success Criteria**:
- [ ] Conversation history stored per chat (max 10 messages, 30m TTL)
- [ ] LLM classifier uses Haiku for intent detection with conversation context
- [ ] Regex fallback on API failure
- [ ] Conversation context passed to task execution
- [ ] Unit tests for conversation store and classifier

---

## Implementation Plan

### Phase 1: Complete Handler Integration ✅ PARTIAL

**Files created** (need verification):
- `internal/adapters/telegram/conversation.go` ✅
- `internal/adapters/telegram/classifier.go` ✅

**Files modified** (partial):
- `internal/adapters/telegram/handler.go` - needs wrapper methods
- `internal/adapters/telegram/notifier.go` - Config updated ✅
- `internal/executor/runner.go` - ConversationContext field added ✅

**Remaining work**:
1. Add wrapper methods to handler.go that store assistant responses:
   - `handleGreetingWithHistory()`
   - `handleQuestionWithHistory()`
   - `handleResearchWithHistory()`
   - `handlePlanningWithHistory()`
   - `handleChatWithHistory()`
   - `handleTaskWithContext()`

2. Stop conversation store in handler cleanup (Stop method)

### Phase 2: Tests

**Create**:
- `internal/adapters/telegram/conversation_test.go`
- `internal/adapters/telegram/classifier_test.go`

**Test cases**:
```go
// Conversation store tests
{"add_user_message", ...}
{"add_assistant_message", ...}
{"get_history", ...}
{"max_size_limit", ...}
{"ttl_cleanup", ...}
{"get_context_summary", ...}

// Classifier tests (mocked API)
{"casual_commit", "Wow, let's commit changes first", IntentChat}
{"check_question", "Check if it was executed", IntentQuestion}
{"clear_task", "Add a logout button to the header", IntentTask}
{"context_yes", "yes", IntentTask}  // After task suggestion in history
{"api_failure_fallback", ...}
```

### Phase 3: Wire Config Loading

**File**: `cmd/pilot/start.go` or wherever Telegram handler is created

Ensure `LLMClassifier` config is passed from `config.Adapters.Telegram.LLMClassifier` to `HandlerConfig`.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| LLM Model | Haiku, Sonnet | Haiku | Fast (~500ms), cheap (~$0.0002/call) |
| API Timeout | 1s, 2s, 5s | 2s | Balance speed vs reliability |
| History Size | 5, 10, 20 | 10 | Enough context without token bloat |
| History TTL | 15m, 30m, 1h | 30m | Conversations rarely span >30m |
| Fallback | Error, Regex | Regex | Never block on API failure |

---

## Files Summary

| File | Action | Status |
|------|--------|--------|
| `internal/adapters/telegram/conversation.go` | Create | ✅ Done |
| `internal/adapters/telegram/conversation_test.go` | Create | ❌ TODO |
| `internal/adapters/telegram/classifier.go` | Create | ✅ Done |
| `internal/adapters/telegram/classifier_test.go` | Create | ❌ TODO |
| `internal/adapters/telegram/handler.go` | Modify | 🚧 Partial |
| `internal/adapters/telegram/notifier.go` | Modify | ✅ Done |
| `internal/executor/runner.go` | Modify | ✅ Done |

---

## API Key Handling

**ANTHROPIC_API_KEY** from environment (same key Claude Code uses).

```go
func NewLLMClassifier(cfg *LLMClassifierConfig) *LLMClassifier {
    apiKey := os.Getenv("ANTHROPIC_API_KEY")
    if apiKey == "" {
        // Classifier disabled - falls back to regex
        return &LLMClassifier{enabled: false}
    }
    // ...
}
```

---

## Config Example

```yaml
adapters:
  telegram:
    llm_classifier:
      enabled: true
      model: claude-3-haiku-20240307
      timeout: 2s
      max_history: 10
      history_ttl: 30m
```

---

## Verify

```bash
# Build
make build

# Run tests
go test ./internal/adapters/telegram/... -v

# Manual test
pilot start --telegram
# Send: "Wow, let's commit changes first" → should be Chat, not Task
# Send: "Check if it was executed" → should be Question, not Task
```

---

## Done

- [ ] All wrapper methods added to handler.go
- [ ] Conversation store cleanup on handler Stop()
- [ ] conversation_test.go passes
- [ ] classifier_test.go passes (with mocked API)
- [ ] `make test` passes
- [ ] Manual verification of problematic messages

---

**Last Updated**: 2026-02-03
