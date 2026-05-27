# TASK-310: OpenRouter spike — validate engine premise before rewrite

**Status**: ✅ complete — findings in `system/openrouter-quirks.md`, decision recorded (drop BYOK, single OR-pool engine path), TASK-312 unblocked and shipping
**Branch**: `feat/custom-engine`
**Created**: 2026-05-26
**Blocks**: TASK-311 (mock parity harness), TASK-312 (`engine.go` rewrite)

---

## Why this is step 0

Removing every other backend and committing to OpenRouter is a one-way door. Before we rewrite `backend_anthropic.go` to target `openrouter.ai/api/v1`, we must validate that OR actually preserves the behaviors the current engine depends on. The current Python engine + Go backend rely on Anthropic-native features (cache_control, extended thinking with explicit budget_tokens, tool_use blocks) — OpenRouter normalizes to OpenAI shape and some of these features pass through with caveats.

**If the spike surfaces a dealbreaker (e.g. cache_control silently dropped or thinking budget unconfigurable), we revisit the OpenRouter decision before sinking days into the rewrite.**

---

## Deliverables

1. **`cmd/or-spike/main.go`** — throwaway Go program (~150 LOC) that exercises OR end-to-end. Deleted after TASK-312 lands.
2. **`.agent/system/openrouter-quirks.md`** — Navigator system doc capturing what works, what doesn't, what we need to work around. This becomes the reference for the `engine.go` rewrite.

## Hard questions the spike must answer

| # | Question | How we'll test | Pass criterion |
|---|---|---|---|
| 1 | Auth + basic completion against `anthropic/claude-opus-4-7` | POST `/chat/completions` with single user message | 200 OK, `choices[0].message.content` populated |
| 2 | Model slug for current Opus — `claude-opus-4-7` or different? | List `/api/v1/models?author=anthropic`, grep | Slug confirmed |
| 3 | Tool calling: send OpenAI-shape `tools`, receive OpenAI-shape `tool_calls`? | Send `bash` tool, force a call | `tool_calls[].function.name == "bash"` |
| 4 | Parallel tool calls supported? | Prompt that needs `bash` + `read_file` same turn | Multiple entries in `tool_calls[]` |
| 5 | Streaming: SSE deltas parse cleanly? | `stream: true`, parse `data: {...}` lines | All chunks reassemble into final response |
| 6 | **Prompt caching** — Anthropic `cache_control: {type: "ephemeral"}` passthrough on system block? | Send large system prompt with `cache_control` twice within 5min, compare usage | `usage.cache_read_input_tokens > 0` on second call (or whatever OR exposes) |
| 7 | **Extended thinking** — does Anthropic's `thinking: {budget_tokens: 10000}` work? Does `reasoning: {effort: "high"}` work? Does either surface reasoning content? | Try both. Send a hard problem, inspect response | Some form of thinking is observable in either usage or content |
| 8 | **Provider routing** — `provider: {order: ["anthropic"], allow_fallbacks: false}` actually pins to Anthropic? | Set order, induce a 529 (or check headers), confirm no silent fallback | Header / response says Anthropic only |
| 9 | Cost reporting — in usage? Or only via `/api/v1/generation?id=<id>` lookup? | Inspect every response field; do follow-up GET | Cost retrievable per call |
| 10 | 429 / 529 response shape — what's the retry signal? | Hammer with parallel reqs until rate-limited | Document headers (`Retry-After`?) and body |
| 11 | Markup vs direct Anthropic price for `claude-opus-4-7`? | Compare to Anthropic listed $/M tokens | Numeric delta |
| 12 | `provider: {require_parameters: true}` — does it filter out providers that don't support `thinking` / `cache_control`? | Send Anthropic-specific params, vary the flag | Behavior documented |

## Out of scope for the spike

- Anything in the production codebase (`internal/executor/`) — read-only this step
- Refactoring or porting claw-code logic — that's TASK-313/314
- Removing other backends — that's TASK-315
- Anthropic Admin Usage API (separate decision: do we want cost-reconciliation via Anthropic's admin endpoint, or fully via OR? Decide *after* knowing what OR reports)

## Inputs

- `OPENROUTER_API_KEY` — user-provided, ambient env only, never written to disk or passed via `--ae`
- ~$2 spend cap on the spike (parallel requests will burn some Opus tokens; capped via tight `max_tokens`)

## Acceptance

- All 12 questions have documented answers in `system/openrouter-quirks.md`
- `cmd/or-spike/main.go` runs clean against live OR with the key
- Spend stays under $2
- One of:
  - **Green**: every dealbreaker passes → TASK-312 proceeds as planned
  - **Yellow**: workable with caveats → quirks doc enumerates them, TASK-312 incorporates workarounds
  - **Red**: dealbreaker (e.g. no cache_control, no thinking control) → escalate, reconsider OR-only decision

## Sequencing after this

Spike result → write `openrouter-quirks.md` → file TASK-311 (parity harness port) and TASK-312 (engine.go rewrite) with the actual constraints folded in.

---

**Last updated**: 2026-05-26
