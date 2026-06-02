# TASK-330: Honor GitHub 403 secondary rate-limits + the `Retry-After` header

**Wave:** 2 (S) · **Pilot** · **Severity:** HIGH · **Audit ref:** TASK-322 §high
"GitHub 403 secondary/primary rate limits are not retried and the Retry-After HTTP header is never read"

---

## Problem

GitHub signals secondary rate limits (and primary REST limits) with **HTTP 403** plus a `Retry-After`
or `X-RateLimit-Reset` **header** — not 429, not body text. `isRetryableError`
(`internal/adapters/github/retry.go:95`) explicitly excludes 403, so secondary-limit 403s fail
immediately. Separately, `doRequest` builds the error from `resp.StatusCode` + body bytes only
(`client.go:165-166`) and discards `resp.Header`; `extractRetryAfter` (`retry.go:141`) then regex-scans
the error **string** for `Retry-After: N`, but GitHub puts it in the HTTP header, so the regex never
matches a real response. Net: under bursty load (poller label churn, board writes) Pilot hits secondary
limits and surfaces immediate hard failures — which the poller can mislabel `pilot-failed`.

## Approach
- `internal/adapters/github/client.go doRequest`: when status is 403 or 429, read
  `resp.Header.Get("Retry-After")`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` and encode them into a
  typed error (e.g. `RateLimitError{RetryAfter time.Duration}`).
- `internal/adapters/github/retry.go`: make `isRetryableError` treat a 403 secondary rate-limit
  (body contains "secondary rate limit" / "rate limit exceeded", or `X-RateLimit-Remaining: 0`) as
  retryable; have `WithRetry` honor the parsed header duration instead of regexing the message string.
- Fix the inaccurate doc comment on `retry.go:28`.

## Files to modify
- `internal/adapters/github/client.go`
- `internal/adapters/github/retry.go`
- `internal/adapters/github/retry_test.go` / `client_test.go`

## Test Strategy
- Unit: a 403 response with `Retry-After: 1` + secondary-rate-limit body → classified retryable, retried
  after ~1s (use a fake transport); a genuine non-rate-limit 403 → still NOT retried.

## Effort
S (~2h). One PR.

## Out of Scope
- `ExecuteGraphQL` retry (TASK-319 board follow-up C3) — separate task, but coordinate since both touch
  `client.go`/`retry.go`; land this first.
