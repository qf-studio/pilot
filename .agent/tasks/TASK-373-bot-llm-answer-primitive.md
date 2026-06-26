# TASK-373: Bot Module Phase 1 — `internal/llm` direct-Anthropic Answer primitive

**Status**: 🚧 Dispatched to Pilot (with TASK-374) → [#3665](https://github.com/qf-studio/pilot/issues/3665)
**Created**: 2026-06-26
**Assignee**: Pilot
**Parent plan**: `/Users/aleks.petrov/.claude/plans/there-is-a-problem-inherited-fiddle.md`

---

## Context

**Problem**: Pilot's chat/question paths spin up the full Claude Code executor
(15–30s) to respond conversationally. The fast direct-HTTP Anthropic client
(`internal/intent/classifier.go` `AnthropicClient`) only has `Classify()` — there is
**no direct-LLM answer path**. This phase adds that missing primitive.

**Goal**: A small, dependency-light `internal/llm` package exposing a generic
"answer" call that returns raw text, reusing the exact HTTP request shape already
proven in `AnthropicClient.Classify`.

---

## Acceptance Criteria

- [ ] New package `internal/llm` with a `Client` type and constructor `NewClient(apiKey string) *Client`.
- [ ] Method `Answer(ctx context.Context, model, system string, history []intent.ConversationMessage, user string) (string, error)`.
- [ ] `SetAPIURL(url string)` and default model overridable per call (model passed as arg).
- [ ] Request shape matches `classifier.go:104-147`: headers `x-api-key`, `anthropic-version: 2023-06-01`, `Content-Type: application/json`; body includes `model`, `max_tokens`, `system`, `messages`, and `output_config.effort` (default `"low"`, overridable).
- [ ] Returns the concatenated `content[].text` from the API response; errors on non-200 / empty content.
- [ ] Uses an `*http.Client` with a sane timeout (default 30s; configurable).
- [ ] No new third-party deps — stdlib `net/http` + `encoding/json` only.

---

## Implementation

### Phase 1: `internal/llm/client.go`
**Goal**: Generic direct Anthropic Messages client returning text.

**Tasks**:
- [ ] Define `Client{apiKey, httpClient, apiURL}`.
- [ ] Implement `Answer(...)`: build `messages` from `history` (cap last N, e.g. 10) + the `user` turn; POST; decode `{content:[{text}]}`; join text.
- [ ] Accept `model` per call so the Responder (TASK-374) can choose Haiku vs Sonnet.
- [ ] Keep `intent.ConversationMessage` as the history type to avoid a new shared type (import `internal/intent`). No import cycle: `intent` does not import `llm`.

**Files**:
- `internal/llm/client.go` (created)
- `internal/llm/client_test.go` (created)

---

## Out of Scope

- Refactoring `intent.AnthropicClient` to delegate to `llm.Client` (future cleanup).
- Streaming responses.
- Retrieval, persona, chat wiring — those are TASK-374+.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| History type | new `llm.Message` vs reuse `intent.ConversationMessage` | reuse `intent.ConversationMessage` | Already the conversation-store type; no cycle (`intent` doesn't import `llm`) |
| Effort | fixed vs configurable | configurable, default `low` | Chat is cheap; grounded Q&A may want higher |

---

## Verify

```bash
go test ./internal/llm/...
go build ./...
make lint
```

Unit test uses `httptest.NewServer` returning a canned Messages response — **no
network, no real API key**. Assert: correct headers sent, request body has
`system`/`messages`/`model`, and `Answer` returns the stubbed text.

---

## Done

- [ ] `internal/llm/client.go` exports `Client` + `Answer`.
- [ ] `go test ./internal/llm/...` passes (stub-server test, no network).
- [ ] `go build ./...` + `make lint` clean.

---

## Refs

- Parent plan: `there-is-a-problem-inherited-fiddle.md`
- Reuse source: `internal/intent/classifier.go:104-147`
- Next: TASK-374 (Responder + fast chat path) consumes this.

---

**Last Updated**: 2026-06-26
