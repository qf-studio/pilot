---
name: openrouter-quirks
description: What works and what doesn't when calling Anthropic models through OpenRouter — empirical findings from the TASK-310 spike. Critical: cache_control and thinking silently stripped on the public credit path.
type: project
---

# OpenRouter quirks for the Pilot engine

**Date**: 2026-05-26
**Source**: TASK-310 spike against live OR with sk-or-v1-* (non-BYOK, OR credits)
**Status**: spike halted partway — cache + thinking findings are dealbreakers worth a decision before continuing

## TL;DR

OR works perfectly for the *commodity* parts of the API surface (auth, completions, tools, streaming, cost reporting, ~0% markup observed on Opus 4.7). But for our specific use case — high-turn agent loops with stable tool schemas — it has one critical hole on the public (non-BYOK) credit path:

1. **`cache_control` silently stripped** — confirmed across 5+ variants including `require_parameters: true`, `transforms: []`, `anthropic-beta: prompt-caching-*` header, both endpoints, all cache_control placements (system / messages / tools). Anthropic upstream always reports `cache_creation_input_tokens=0`, `cache_read_input_tokens=0`. Identical cost on repeated identical prefixes.
2. **`thinking: {budget_tokens}` stripped** — response `content` returns only `[{type:"text"}]`, no `thinking` block.

Cost impact: 5–10× billing penalty on tool-heavy workloads vs Anthropic-direct (cache misses on ~2K tool schemas per turn × 60 turns).

## Two different "caching" concepts — don't confuse them

| | OR response cache (new finding) | Anthropic prompt cache (what we need) |
|---|---|---|
| Cache key | Full identical request | Common prefix of system + messages + tools |
| Hit billing | **Free** (`prompt_tokens: 0`, `total_tokens: 0`) | 10% of normal input rate |
| Enable | `X-OpenRouter-Cache: true` header | `cache_control: {type:"ephemeral"}` on content blocks |
| TTL | 300s default, configurable 1s–86400s | 5 min (ephemeral) or 1h (with beta) |
| Detected via | `X-OpenRouter-Cache-Status: HIT` response header | `usage.cache_read_input_tokens > 0` |
| Works for | All models, model-agnostic, OR-side | Anthropic native (stripped by OR) |
| Useful for our agent loop? | ❌ No — each turn has new messages, full request differs | ✅ Yes — system + tools stable, only messages grow |
| Per docs | "Caching operates at the OpenRouter layer before the request reaches any provider" + "Provider caching is separate from OpenRouter response caching and the two can be used together" | Stripped by OR; only works via BYOK |

**OR's response cache is real and useful for plain repeated queries (e.g. completion APIs hit by many users with identical prompts). It's not what an agent loop needs.**

## The escape hatch — BYOK (bring-your-own-key)

OR's docs and the 429 error message both point at BYOK as the fix. With BYOK:
- You provision an Anthropic API key in OR's settings under Integrations
- OR forwards requests using your Anthropic key (responses include `usage.is_byok: true`)
- Anthropic sees your account → prompt caching engages (very likely; not yet retested)
- Rate limits accumulate to your Anthropic account, not OR's shared pool
- BYOK endpoints get automatic prioritization in routing

**Still untested in this spike** (key budget exhausted before BYOK retest).

## Empirical findings

### ✅ Works (verified)

| # | Feature | Notes |
|---|---|---|
| 1 | Auth via `Authorization: Bearer sk-or-v1-*` | Standard |
| 2 | Model slug | `anthropic/claude-opus-4.7` (**dots, not dashes** — existing code uses `claude-opus-4-6` dashed) |
| 3 | `POST /api/v1/chat/completions` (OpenAI-shape) | Returns OpenAI-shape; `tool_calls[].id` preserves Anthropic-native `toolu_*` |
| 4 | `POST /api/v1/messages` (Anthropic-native shape) | Returns Anthropic-shape (`content: [{type:"text"}]`, `stop_reason`, `usage.cache_*` fields, etc.) |
| 5 | Parallel tool calls | Multiple entries in single `tool_calls[]` array |
| 6 | SSE streaming | Heartbeats `: OPENROUTER PROCESSING`, final chunk carries `usage`, terminator `data: [DONE]` |
| 7 | Provider routing | `provider: {order:["anthropic"], allow_fallbacks: false}` pins to `"Anthropic"`. Without pin, may route to `"Google"` (Vertex-hosted Anthropic models). |
| 8 | Cost in response | `usage.cost` and `usage.cost_details.upstream_inference_cost` per call. **0% observed markup** on Opus 4.7. |
| 9 | Retroactive cost lookup | `GET /api/v1/generation?id=<gen_id>` returns `tokens_prompt`, `tokens_completion`, `native_tokens_*`, `total_cost`, `provider_responses[]` (upstream msg ID) |
| 10 | 429 handling | Error body: `metadata.retry_after_seconds`, `metadata.headers.Retry-After`, `metadata.provider_name` |
| 11 | Cancellation/credit-cap | HTTP 402, message includes max-tokens-we-can-afford figure |

### ❌ Broken / stripped (non-BYOK, all endpoints + headers tried)

| # | Feature | Symptom | Tests run |
|---|---|---|---|
| 6 | `cache_control: {type:"ephemeral"}` | `cache_creation_input_tokens=0` and `cache_read_input_tokens=0` on EVERY call, regardless of position (system / messages / tools), endpoint (/chat/completions or /messages), `transforms: []`, `usage: {include: true}`, or `anthropic-beta: prompt-caching-2024-07-31` header. Identical cost on repeated identical requests. | 4 variants exhausted before halting. |
| 7 | `thinking: {type:"enabled", budget_tokens: N}` | Response `content` contains only `[{type:"text"}]` — no `thinking` block. `output_tokens` reflects only visible text. No `reasoning_tokens` accounted. | 1 test (key ran out before more variants) |

### ⚠️ Confirmed broken (additional tests after first round)

- `provider: {require_parameters: true}` — does NOT preserve `cache_control`. Anthropic upstream still reports 0/0 on cache fields. OR strips the field before applying the require_parameters filter.

### ⏳ Still untested (key budget exhausted)

- **BYOK passthrough**: does configuring an Anthropic key under OR settings restore `cache_control` + `thinking`?
- 529 (overloaded) response shape (only saw 429 + 402)
- Anthropic Admin Usage API integration (`/v1/organizations/usage_report/messages`) — separate from OR, requires `sk-ant-admin-*`
- Verifying behavior of `~anthropic/claude-opus-latest` alias slug
- Reasoning via OpenAI-shape `reasoning: {effort: "high"}` parameter
- OR's own response cache (`X-OpenRouter-Cache: true`) — works but not useful for our agent loop pattern

## Cost impact quantified

Concrete scenario: 60-turn agent loop, 2000-token tool schemas, modest system prompt.

| Path | Input tokens billed | Cost (Opus 4.7 @ $5/M in) |
|---|---|---|
| **No caching (OR non-BYOK observed)** | 2000 × 60 = 120,000 | $0.60 just on tool schemas |
| **Caching engaged (Anthropic native)** | 2000 cache write + 2000 × 59 × 10% = 13,800 effective | $0.069 |
| **Delta** | — | **8.7× more expensive without caching** |

Production agent loops also re-bill the system prompt every turn. Effective penalty in real bench runs likely **5–10× the cached-portion cost**.

## OR-specific request quirks (worth knowing)

- Use header `HTTP-Referer: <repo url>` and `X-Title: <app name>` for app attribution in OR dashboards.
- Provider names in `provider.order` accept lowercase (`"anthropic"`) but echo back capitalized (`"Anthropic"`).
- `usage.is_byok: false` field present — flips to `true` under BYOK.
- `model` returned is full snapshot ID (`anthropic/claude-4.7-opus-20260416`), not the requested slug. Useful for caching responses by exact model.
- The `~` prefix in slugs (e.g. `~anthropic/claude-opus-latest`) denotes auto-aliased latest. Pricing identical to current concrete version.
- Generation IDs (`gen-*`) and upstream provider message IDs (`msg_*`) are different — both surfaced via `/api/v1/generation?id=`.

## Decision matrix

| Strategy | Cache | Thinking | Multi-provider | Engine complexity |
|---|---|---|---|---|
| OR non-BYOK only | ❌ | ❌ | ✅ wide | Low |
| OR BYOK only | ❓ (likely ✅) | ❓ (likely ✅) | ✅ wide | Medium — key per provider in OR settings |
| Anthropic-direct only | ✅ | ✅ | ❌ Anthropic only | Low |
| Hybrid: Anthropic-direct + OR for non-Anthropic | ✅ on Anthropic | ✅ on Anthropic | ✅ via OR | Medium — two clients |

## Decision (2026-05-26)

**Ship OR-only. Drop BYOK from the plan.**

Rationale:
- One auth surface (`OPENROUTER_API_KEY`), one engine code path, multi-provider model selection — matches user's product vision
- OR's docs explicitly support `cache_control`; my spike result (0 cached tokens) was likely caused by (a) wrong placement on tool-wrapper instead of content blocks, and (b) explicit `provider.order` disabling OR's sticky-routing-for-cache
- BYOK is documented as an OPTIONAL user-side optimization, not something Pilot's engine needs to know about. If a user pastes their Anthropic key under OR Integrations, they get caching with 5% markup; if not, they pay OR pool rate (no markup observed but no cache either on the spike). Same engine code either way.
- A power-user nudge ("you've spent $X; adding your Anthropic key under OR Integrations would have saved $Y") can be a future feature based on `usage.is_byok` flag in responses

## Engine implementation guidelines (from spike + search findings)

1. **Endpoint**: `POST https://openrouter.ai/api/v1/chat/completions` (OpenAI-compat shape; max model coverage)
2. **Auth**: `Authorization: Bearer $OPENROUTER_API_KEY` + attribution headers (`HTTP-Referer`, `X-OpenRouter-Title`)
3. **Cache_control placement**: at **content-block level**, not on tool wrappers. Example: `system: [{type:"text", text:"...", cache_control:{type:"ephemeral", ttl:"1h"}}]`. Use `ttl: "1h"` (not default 5m) for agent loops that idle on tool waits
4. **Provider routing**: **do not set `provider.order` by default** — it disables OR's sticky routing which keeps the cache warm across turns. Let OR pick. Pin only when there's a specific reason
5. **Thinking**: send `reasoning: {effort: "low"|"medium"|"high"}` (OR's normalized field for /chat/completions). For Anthropic models this maps to native `thinking: {budget_tokens}` on the wire
6. **Tool format**: OpenAI function-calling (`tools: [{type:"function", function:{...}}]`). OR translates to Anthropic `tool_use` transparently and preserves Anthropic-native `toolu_*` IDs in `tool_calls[].id`
7. **Streaming**: SSE. Skip `: OPENROUTER PROCESSING` heartbeat lines. Terminator `data: [DONE]`. Final chunk carries `usage`
8. **Cost capture**: read `usage.cost` from non-streaming or final streaming chunk. For retroactive lookup: `GET /api/v1/generation?id=<gen_id>`
9. **Cache hit detection**: `usage.prompt_tokens_details.cached_tokens > 0` (OR's name) or `usage.cache_read_input_tokens > 0` (native Anthropic name passed through)
10. **Retry**: 5× exponential backoff (30/60/90/120/180s) on 429/529/5xx. Honor `metadata.retry_after_seconds` on 429
11. **Model slugs use dots not dashes**: `anthropic/claude-opus-4.7`, `anthropic/claude-sonnet-4.6`, `anthropic/claude-haiku-4.5`. Alias slugs (`~anthropic/claude-opus-latest`) auto-resolve to current

## Throwaway artifact

No `cmd/or-spike/main.go` was written — curl + python was faster for the surface we tested and didn't justify a Go file. All requests fit in one shell session; raw responses are in the transcript.
