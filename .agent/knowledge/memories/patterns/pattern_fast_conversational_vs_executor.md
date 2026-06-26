---
name: fast-conversational-vs-executor-path
description: When bot.enabled is true, chat/Q&A/issue-intake use a direct Anthropic API call (~1-2s); when false/nil, they fall through to the Claude Code executor (~15-30s). The same comms.Handler serves both paths — the responder field is nil when disabled.
metadata:
  type: pattern
---

# Pattern: Fast Conversational Path vs Executor Path

## When bot is enabled (`bot.enabled: true`)

The `comms.Handler` holds a non-nil `*Responder` that calls the Anthropic API directly:

| Intent          | Fast path                       | Latency    |
|-----------------|---------------------------------|------------|
| `IntentChat`    | `responder.Chat()`              | ~1-2s      |
| `IntentQuestion`| `responder.Answer()` + retrieval| ~2-4s      |
| `IntentIssueIntake` | `responder.DraftIssue()`   | ~2-3s      |
| `IntentGreeting`| `responder.Greeting()` (static) | <1ms       |

All three LLM calls (`Chat`, `Answer`, `DraftIssue`) honor `bot.persona` via the
`Responder.systemPrompt()` / `answerSystemPrompt()` / `draftIssueSystemPrompt()` methods.
The persona is a string prefix injected into the system prompt; if unset the default
"You are Pilot, a helpful AI development assistant." is used.

## When bot is disabled (`bot.enabled: false` or block absent)

The `Responder` is nil. Each handler falls back to the executor path:

- `handleChat` → spawns Claude Code executor (60s timeout)
- `handleQuestion` → spawns Claude Code executor (90s timeout)
- `handleIssueIntake` → returns "bot module required" message (no executor fallback)
- `handleGreeting` → static string (no LLM, unaffected)

This zero-regression fallback is the key invariant: disabling the bot must restore
pre-bot behavior exactly. Maintained by nil-checks at every fast-path entry point.

## Voice text flow

Transcribed voice messages already flow through the same intent→responder path:
`IncomingMessage.VoiceText` is populated and sanitized in `HandleMessage`; the
Telegram adapter sets `Text = VoiceText` when routing transcribed audio to
`commsHandler.HandleMessage`. The `bot.voice.enabled` flag is a scaffold for
future call-transport wiring (Telegram calls, Slack huddles) — not currently
functional beyond this field existing in config.

## Rate limiting

`comms.Handler` has a single `*RateLimiter` for all messages (applied before intent
dispatch in `HandleMessage`). When `bot.rate_limit` is configured, `main.go` maps it
to `HandlerDeps.RateLimit` at startup, overriding the per-adapter rate_limit. This
means bot-operator–set limits apply to ALL traffic, not just LLM-path traffic.

## Test-at-the-seam lesson ([[bug-sdk-command-action-dropped]])

The fast path must be verified by testing through `HandleMessage` with a real `*Responder`
and a mock LLM client — NOT by testing `Responder.Chat` in isolation. The responder
being nil vs non-nil is the switch; testing the switch means crossing the
Handler→Responder boundary. See mem-036 for the same verification-layer lesson applied
to the slash-command path.

## Key files

- `internal/comms/handler.go` — `HandleMessage`, `handleChat`, `handleQuestion`
- `internal/comms/responder.go` — `Chat`, `Answer`, `Greeting`, `systemPrompt`, `answerSystemPrompt`
- `internal/comms/issue_intake.go` — `DraftIssue`, `draftIssueSystemPrompt`
- `internal/comms/factory.go` — `BuildResponder`, `BuildHandler`, `BotConfig`
- `internal/config/config.go` — `BotConfig`, `BotVoiceConfig`, `BotRateLimitConfig`
- `cmd/pilot/main.go` — Telegram/Slack bot wiring
- `cmd/pilot/poller_discord.go` — Discord bot wiring
