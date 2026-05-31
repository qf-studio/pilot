# TASK-339 (C3): ExecuteGraphQL has no retry — Projects V2 board operations are non-resilient

**Wave:** 2 · **Pilot** · **Severity:** HIGH (reliability) · **Issue:** #3330 · **Audit ref:** TASK-322 finding C3 (TASK-319 board trio)

---

## Problem
`doRequest` wraps every REST call in `WithRetryVoid(..., c.retryOpts)`, retrying 429/5xx/network errors.
`ExecuteGraphQL` does a raw `c.httpClient.Do` with **no retry wrapper**. Every Projects V2 operation goes
through it: project-ID resolution, field/option resolution, board-source pagination (`project_source.go`),
and board status write-back (`project_board.go setItemFieldValue`). A single transient 502/timeout therefore
(a) aborts an entire board-source candidate fetch mid-pagination, discarding collected pages; and (b) makes
best-effort board write-backs fail on the first blip, silently stranding the card in the wrong column.

## Approach
Wrap the body of `ExecuteGraphQL` in `WithRetryVoid(ctx, func() error {...}, c.retryOpts)` exactly like
`doRequest`. Treat GraphQL responses with HTTP 200 + a rate-limit/transient error message (body contains
`RATE_LIMITED` or `was submitted too quickly`) as retryable in `isRetryableError`.

## Files to modify
- `internal/adapters/github/client.go:787-833`
- `internal/adapters/github/retry.go` (predicate) + test

## Test Strategy
- Transport fails first GraphQL attempt with 502 then succeeds → `ExecuteGraphQL` returns success (one retry).
- 200 + `RATE_LIMITED` body → retried.

## Effort
S–M. One PR. Distinct file from TASK-338/340.

## Out of Scope / coordinate
- If TASK-330's 403/Retry-After work (#3318) already restructured `isRetryableError`, **build on it** — don't revert.
