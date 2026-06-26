---
name: Fast Conversational Path vs Executor Path
description: Bot module dual-path architecture — direct LLM (1-2s) vs Claude Code executor (15-30s) for different intent categories
type: pattern
created: 2026-06-26
---

The comms Handler dispatches intents down one of two paths depending on whether a
`Responder` is wired (i.e. `bot.enabled: true`):

## Fast path (direct LLM, ~1-2s)
Intents handled directly via `Responder` without spawning a Claude Code executor:

| Intent | Handler | Notes |
|--------|---------|-------|
| Greeting | `handleGreeting` → `Responder.Greeting()` | Static, no LLM call |
| Chat | `handleChat` → `Responder.Chat()` | Conversational, ≤400 words |
| Question | `handleQuestion` → `Responder.Answer()` | Bounded retrieval + LLM; falls back to executor if too broad |
| IssueIntake | `handleIssueIntake` → `Responder.DraftIssue()` | JSON-structured GitHub issue draft |

## Executor path (Claude Code, ~15-30s)
Intents that always spawn a `executor.Runner.Execute()` worktree:

| Intent | Handler | Notes |
|--------|---------|-------|
| Task | `handleTask` | Full PR + worktree + CI |
| Research | `handleResearch` | Read-only, LocalMode |
| Planning | `handlePlanning` | LocalMode, returns plan for confirmation |
| Operational | `handleOperational` | Store-backed queue summary; no executor |

## Fallback guarantee
When `bot.enabled: false` (or Responder is nil), every fast-path handler falls
through to the executor. The code pattern is:

```go
if h.responder != nil {
    // fast path
    return
}
// fallback: executor path (unchanged behavior)
```

This keeps the regression surface zero: disabling the bot is identical to before.

## Rate limiting
`AllowMessage(contextID)` is called at the top of `HandleMessage` — before intent
dispatch — so all paths (fast and executor) are rate-limited by the same
per-context token bucket (`comms.RateLimiter`). Configure via
`adapters.telegram.rate_limit` / `adapters.slack.rate_limit`.

## Voice seam
`IncomingMessage.VoiceText` carries the transcript from the adapter's transcription
middleware. `HandleMessage` merges it into `text` when `Text == ""`, so voice
messages flow through the same intent→responder path as regular text.
Actual call/audio wiring is a future phase.

## Test-at-the-seam lesson (ref mem-036)
Tests for this architecture must cross the adapter→comms boundary (not just test
`comms` in isolation with pre-processed text). The `/help`-creates-a-task incident
(mem-036) showed that an inner-layer test can pass green while the live boundary is
broken. Write end-to-end HandleMessage tests that start from raw IncomingMessage
values, not pre-dispatched intents.
