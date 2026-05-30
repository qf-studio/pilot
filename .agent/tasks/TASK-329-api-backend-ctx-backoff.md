# TASK-329: Context-aware retry backoff in the API backends

**Wave:** 2 (S) · **Pilot** · **Severity:** HIGH · **Audit ref:** TASK-322 §high
"API backend retry backoff uses time.Sleep, ignoring context cancellation"

---

## Problem

`AnthropicBackend.callAPI` and `OpenAIBackend.callAPI` retry on HTTP/5xx/429/overloaded errors with a
direct `time.Sleep(backoffs[...])` inside the loop. `backoffs` grows to 180s over `apiMaxRetries`
attempts, so a single call can sit in unconditional sleeps for minutes. `callAPI(ctx)` receives the
long-lived task execution context; on daemon shutdown / task timeout / cancellation, `ctx.Done()` fires
but the goroutine keeps sleeping — cancellation is silently deferred until the sleep elapses. The GitHub
client already does this correctly (`internal/adapters/github/retry.go:64-70`).

## Approach
- `internal/executor/backend_anthropic.go` (~353, 365, 386) and `internal/executor/backend_openai.go`
  (~263, 273): replace each `time.Sleep(wait)` in the retry loop with a context-aware wait:
  ```go
  select {
  case <-ctx.Done():
      return nil, ctx.Err()
  case <-time.After(wait):
  }
  ```
- Preserve the existing backoff schedule; only the wait becomes cancellable.

## Files to modify
- `internal/executor/backend_anthropic.go`
- `internal/executor/backend_openai.go`
- corresponding `*_test.go`

## Test Strategy
- Unit: cancel the ctx mid-backoff and assert `callAPI` returns `ctx.Err()` promptly (well under the
  backoff duration) rather than after the full sleep. Use a fake transport that forces a retryable error.

## Effort
S (~1.5h). One PR.

## Out of Scope
- Changing the backoff schedule or max-retry count.
