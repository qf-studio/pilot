# TASK-374: Bot Module Phase 2 — comms Responder + fast chat path

**Status**: ✅ SHIPPED 2026-06-26 — #3665 (P1+P2 combined) closed `pilot-done`; responder path live
**Created**: 2026-06-26
**Assignee**: Pilot
**Parent plan**: `/Users/aleks.petrov/.claude/plans/there-is-a-problem-inherited-fiddle.md`
**Depends on**: TASK-373 (`internal/llm`) — ships in the same PR.

---

## Context

**Problem**: `handleChat` (`internal/comms/handler.go:487`) and `handleGreeting`
(`:293`) route through the heavy Claude Code executor (60s timeout) for what is a
pure conversation needing zero code reading. This is the biggest, lowest-risk UX win.

**Goal**: A `Responder` (direct-LLM via `internal/llm`, configurable model + persona)
wired into the comms intent handlers so chat/greeting answer in ~1–2s. When the bot
is disabled or no API key is present, behavior is **identical to today** (executor
fallback) — safe incremental rollout.

---

## Acceptance Criteria

- [ ] `internal/comms/responder.go`: `Responder` wrapping `*llm.Client`, holding `model`, `answerModel`, `persona`. Method `Chat(ctx, history []intent.ConversationMessage, msg string) (string, error)` builds a persona system prompt and calls `llm.Answer` with `model`.
- [ ] `internal/config`: root-level `BotConfig` parsed from a `bot:` YAML block (fields: `enabled`, `model`, `answer_model`, `api_key`, `persona`; nested `retrieval`/`issue_intake`/`voice` may be stubbed now, used later). Default `model` = `claude-haiku-4-5-20251001`.
- [ ] `internal/comms/factory.go`: `BuildResponder(cfg *BotConfig)` mirroring `BuildClassifier` (api-key resolution: `cfg.APIKey` → `ANTHROPIC_API_KEY`; returns nil when disabled/no key). Extend `HandlerDeps` with `Bot *BotConfig`; wire the built `Responder` into `HandlerConfig`/`Handler`.
- [ ] `internal/comms/handler.go`: add `responder *Responder` field. `handleChat`: if `responder != nil` → `Chat(...)`, send reply, record to `convStore`; **else** current executor path unchanged. `handleGreeting`: persona greeting via responder when available, else current static text.
- [ ] `cmd/pilot/main.go`: thread `cfg.Bot` into every adapter's `HandlerDeps` (Slack ~line 2608 + Telegram/Discord equivalents — the existing `*ClassifierConfig` wiring sites).
- [ ] `configs/pilot.example.yaml`: root `bot:` block (see plan).

---

## Implementation

### Phase 2a: Responder + config
- [ ] `BotConfig` struct + parse; `responder.go`; `BuildResponder`.

### Phase 2b: Wire handlers
- [ ] `handleChat` / `handleGreeting` use responder with executor fallback.
- [ ] Thread `cfg.Bot` through `HandlerDeps` at all adapter call sites.

**Files**:
- `internal/comms/responder.go` (created)
- `internal/comms/factory.go` (modified — `BuildResponder`, `HandlerDeps.Bot`)
- `internal/comms/handler.go` (modified — `responder` field, `handleChat`, `handleGreeting`)
- `internal/config/config.go` (modified — `BotConfig`)
- `cmd/pilot/main.go` (modified — thread `cfg.Bot`)
- `configs/pilot.example.yaml` (modified — `bot:` block)
- `internal/comms/responder_test.go`, `internal/comms/handler_chat_test.go` (created)

---

## Out of Scope

- Grounded Q&A retrieval / `handleQuestion` rewrite → TASK-375.
- Issue intake → TASK-376.
- Voice wiring (flag only) → persona/docs TASK-377.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| `bot:` placement | per-adapter vs root | root-level | Transport-agnostic; one block, threaded to all adapters |
| Disabled behavior | error vs fallback | fallback to executor | Zero-regression rollout; `responder == nil` short-circuits |
| Persona | hardcoded vs config | config `bot.persona` | Pilot's "voice" is user-tunable |

---

## Verify

```bash
go test ./internal/comms/... ./internal/config/...
go build ./... && make lint
```

Responder test: mock/stub `llm.Client` → assert system prompt includes persona and
the executor is never invoked. Handler test: with responder set, `handleChat`
produces a reply via responder and **does not** call `runner.Execute`; with
responder nil, the existing executor path is taken (regression guard).

**Live** (after merge, `bot.enabled: true`): send "what do you think about Go
generics?" on Slack → persona reply in ~1–2s; confirm no `CHAT-…` executor task and
no "Creating isolated worktree" in daemon logs.

---

## Done

- [ ] Chat + greeting answer via direct LLM when `bot.enabled`; executor fallback when not.
- [ ] All adapter call sites pass `cfg.Bot`.
- [ ] Tests pass; build + lint clean.

---

## Refs

- Parent plan: `there-is-a-problem-inherited-fiddle.md`
- Depends on TASK-373 (`internal/llm`).
- Followups: TASK-375 (grounded Q&A), TASK-376 (issue intake), TASK-377 (persona/docs).

---

**Last Updated**: 2026-06-26
