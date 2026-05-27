# TASK-312: `engine.go` — OpenRouter-only execution engine

**Status**: ✅ complete — engine.go + tests landed, smoke verified against live OR
**Branch**: `feat/custom-engine`
**Created**: 2026-05-26
**Completed**: 2026-05-26
**Depends on**: TASK-310 (spike findings → `system/openrouter-quirks.md`)
**Blocks**: backend deletes, bench wiring, factory collapse

## Outcome

- `internal/executor/engine.go` (~700 LOC) — new Backend implementation targeting `openrouter.ai/api/v1/chat/completions`
- `internal/executor/engine_test.go` (~500 LOC, 22 tests) — full coverage of construction, factory wiring, SSE parsing (incl. parallel tool calls + stream errors), HTTP retry on 429 with backoff override, end-to-end via `httptest.Server`, tool dispatch, smoke gated by `OPENROUTER_API_KEY`
- `BackendTypeOpenRouter = "openrouter"` registered in `backend_factory.go` (additive — existing backends untouched in this PR per the agreed phasing)
- Live smoke against OR pool key: ✅ HTTP+SSE round-trip, cost reported, `cache_read=0` (confirms spike finding on non-BYOK path)
- All existing executor tests still green (`ok  internal/executor  99.596s`)

## Key implementation details
- Cache_control on system content block with `ttl:"1h"`
- No default `provider.order` (preserves OR sticky cache routing)
- `reasoning: {effort}` field, progressive: high for first 8 turns, medium after; opts.Effort overrides
- One-time WARN log if `cached_tokens` stays 0 for 5+ turns (nudge for BYOK setup)
- Exit fast on 402 (credit cap) with `ErrorType: "api_error"`
- Tools: bash, read_file, write_file, edit_file — ported from `backend_anthropic.go` with `engine*` prefix to avoid package-local collision (will rename cleanly in TASK-315 when old backends are deleted)

## Follow-ups
- TASK-313 — port claw-code `bash_validation` (6 submodules) into `engineExecBash`
- TASK-314 — port claw-code `file_ops` guards into `engineExecReadFile`/`WriteFile`/`EditFile`
- TASK-315 — delete `backend_claudecode.go`, `backend_opencode.go`, `backend_qwencode.go`, `backend_openai.go`, `backend_anthropic.go`; collapse factory; rename `engine*` helpers to clean names
- TASK-316 — bench wiring: strip CC install, flip `_build_config` to `openrouter`
- TASK-317 — delete Python `engine.py`
- TASK-318 — memory updates (`feedback_subprocess_not_api.md`, `bench_cost_safety.md`)

---

## Goal

Single Pilot execution engine that talks to OpenRouter's `/api/v1/chat/completions`. One auth surface (`OPENROUTER_API_KEY`), multi-provider model access via slugs, OpenAI-shape API for maximum compatibility.

This becomes Pilot's only Backend — current `claude-code`, `opencode`, `qwencode`, `openai-api`, `anthropic-api` all get deleted in follow-up tasks.

## Scope of this task

**In scope:**
- New file `internal/executor/engine.go` implementing `Backend` interface against OR
- Register `BackendTypeOpenRouter = "openrouter"` in `backend_factory.go` (additive — existing backends untouched in this PR)
- Unit tests for the engine (HTTP fixtures, no live OR calls)
- One smoke test against live OR gated by `OPENROUTER_API_KEY` env var (skip in CI)
- Update `BackendConfig` with `OpenRouterConfig` sub-struct (API key resolution, attribution headers, optional `provider` overrides)

**Out of scope (follow-ups):**
- Deleting other backends (TASK-315)
- Collapsing factory (TASK-315)
- Bench wiring (TASK-316)
- Python `engine.py` deletion (TASK-317)
- Memory updates (TASK-318)

## Implementation outline

### Endpoint and request shape
- `POST https://openrouter.ai/api/v1/chat/completions`
- Headers: `Authorization: Bearer $OPENROUTER_API_KEY`, `Content-Type: application/json`, `HTTP-Referer: https://github.com/qf-studio/pilot`, `X-OpenRouter-Title: pilot`
- Body: OpenAI-shape with the OR-specific extensions:
  - `model: <slug>` (e.g. `anthropic/claude-opus-4.7`)
  - `messages: [{role, content: [{type, text, cache_control?}]}]`
  - `tools: [{type:"function", function:{name, description, parameters}}]` (no cache_control on tool wrappers — see quirks doc)
  - `reasoning: {effort: "low"|"medium"|"high"}` for thinking
  - `stream: true`
  - `usage: {include: true}`
  - **NO `provider.order` by default** (preserves sticky routing for cache)

### Engine loop
Mirror the proven semantics from existing `backend_anthropic.go` and Python `engine.py`:
- Max 60 turns
- Progressive reasoning effort: high for first 8 turns, medium after (mapped from previous `thinkingHigh/Low` budgets)
- Tool dispatch: `bash`, `read_file`, `write_file`, `edit_file` (port from `backend_anthropic.go` for now; claw-code bash_validation + file_ops guards come in TASK-313/314)
- Tool output cap: 50KB
- Context prune at ~150K tokens
- 5× retry with exp backoff (30/60/90/120/180s) on 429/529/5xx
- Honor `metadata.retry_after_seconds` and `Retry-After` headers on 429

### Cost capture
- Read `usage.cost` from final response (non-stream) or final streaming chunk
- Optionally `GET /api/v1/generation?id=<gen_id>` for detailed cost reconciliation

### Cache strategy
- Place `cache_control: {type:"ephemeral", ttl:"1h"}` on:
  - The system message content block
  - The last cacheable content block in `messages` (auto-mode pattern from OR docs)
- Detect engagement via `usage.prompt_tokens_details.cached_tokens > 0`
- Log a one-time INFO-level message if cached_tokens stays 0 across 5+ turns (suggests user hasn't set up BYOK and pool path isn't caching for them — surface as nudge later)

### Streaming SSE parsing
- Skip `: OPENROUTER PROCESSING` heartbeat comments
- Parse `data: {...}` lines, reassemble `choices[0].delta` into final message
- Terminate on `data: [DONE]`
- Capture `usage` from the chunk that carries it (last data chunk, by convention)

### Model routing config

`BackendConfig.OpenRouter` carries:
```go
type OpenRouterConfig struct {
    APIKey            string            // explicit override; falls back to env
    BaseURL           string            // default https://openrouter.ai/api/v1
    AttributionURL   string            // HTTP-Referer header
    AttributionTitle string            // X-OpenRouter-Title header
    DefaultModel     string            // fallback slug if router returns empty
    ProviderOrder    []string          // optional, advanced override (disables sticky cache)
    AllowFallbacks   *bool             // optional
    Effort           string            // override for progressive (default: progressive)
}
```

Existing `ModelRouting` in BackendConfig maps complexity → slug. Reuses Pilot's `EffortClassifier` and `ModelRouter`.

## Acceptance

- `go build ./...` clean
- `go test ./internal/executor/...` clean (mock-based)
- `OPENROUTER_API_KEY=... go test -run TestEngineSmoke ./internal/executor/` exits 0 against live OR (manual run, gated)
- Engine reaches "SPIKE OK" round-trip in <10s on default model
- 3-turn tool-using loop completes without panic; `usage.cached_tokens` ≥ 0 observed; total cost printed to log
- File size ≤ ~900 LOC including header + tests

## Sequencing

After this lands, immediate follow-ups:
- TASK-313 — port claw-code `bash_validation` (6 submodules) into the engine's `bash` tool
- TASK-314 — port claw-code `file_ops` guards into `read_file`/`write_file`/`edit_file`
- TASK-315 — delete `backend_claudecode.go`, `backend_opencode.go`, `backend_qwencode.go`, `backend_openai.go`, `backend_anthropic.go`; collapse factory switch
- TASK-316 — bench: strip CC install from `install-pilot-agent.sh.j2`, flip `agent.py._build_config` to `engine`
- TASK-317 — delete Python `pilot-bench/pilot_agent/engine.py`
- TASK-318 — supersede `feedback_subprocess_not_api.md` and `bench_cost_safety.md` memories; write new decision memory

---

**Last updated**: 2026-05-26
