---
name: bot's direct LLM client omitted required max_tokens (and a test asserted the buggy output_config) → every chat/Q&A/intake call 400'd
description: internal/llm/client.go (the bot module's direct Anthropic Messages client) built its request body with model+system+messages+output_config{effort:low} but NO max_tokens, which the Anthropic Messages API REQUIRES. Every Responder.Chat/Answer/DraftIssue call returned HTTP 400 "max_tokens: Field required", so the bot replied "Sorry, I couldn't process that." live on Slack. It shipped GREEN because TestAnswer_RequestShape asserted output_config was PRESENT and never checked max_tokens — the test encoded the bug. output_config{effort:low} is also non-standard (copied from the never-enabled classifier) and isn't a valid Messages param. Fixed v2.200.1 (#3700): add max_tokens (2048), drop output_config, and make the tests assert max_tokens present + output_config absent.
type: pitfall
---
The conversational bot replied **"Sorry, I couldn't process that. Try rephrasing?"**
to every Slack message. That string is `handleChat`'s non-timeout error branch —
i.e. `Responder.Chat()` returned an error. The error was the Anthropic API call.

**Root cause:** `internal/llm/client.go` `Answer()` built the request body as
```go
body := map[string]interface{}{
    "model": model, "system": system, "messages": messages,
    "output_config": map[string]interface{}{"effort": "low"},   // non-standard
    // NO max_tokens
}
```
The Anthropic **Messages API requires `max_tokens`**. Omitting it →
`HTTP 400 {"type":"invalid_request_error","message":"max_tokens: Field required"}`.
`output_config{effort:low}` is **not** a Messages-API field — it was copied from
`internal/intent/classifier.go`, whose classifier was never enabled (`llm_classifier: null`),
so the bogus shape was never exercised until the bot used it for real.

**Why it shipped green (the verification gap, again):** `TestAnswer_RequestShape`
**asserted `output_config` was present** and `effort=="low"`, and **never checked
`max_tokens`**. The test codified the broken request, so CI stayed green. This is the
same lesson as [[bug_sdk_command_action_dropped]] (mem-036): *assert the thing the bug
is about*. A request-shape test must assert the fields the API actually requires.

**How it was diagnosed (fast):** reproduce the exact request with `curl` using the
real key —
- request as the code sent it (no max_tokens, with output_config) → **400 "max_tokens: Field required"**
- same request + `max_tokens` − `output_config` → **200**, real answer.
That isolates "the key/model/auth are fine; the client request shape is wrong" in two
calls, without needing daemon logs (the daemon logs to the `--dashboard` TUI, ungreppable).

**Fix (v2.200.1, PR #3700):**
```go
body := map[string]interface{}{
    "model": model, "max_tokens": maxTokens /*2048*/, "system": system, "messages": messages,
}
```
plus tests now assert `max_tokens` is a positive number in the body and `output_config`
is absent.

**How to apply:**
- Any Anthropic **Messages API** request MUST include `max_tokens`. There is no default.
- Don't copy `output_config`/`effort` into Messages requests — it's not a Messages field;
  control output with `max_tokens` + model choice.
- A "request-shape" unit test must assert the **required** fields (max_tokens, model,
  messages), not just the optional ones. A test that asserts a field the API rejects is
  worse than no test — it makes CI vouch for the bug.
- When a chat/LLM feature errors with a generic user-facing fallback, reproduce the exact
  outbound request with `curl` before touching code — it separates request-shape bugs from
  auth/model/runtime bugs in ~2 calls.
