# TASK-294: Move `WithRetry` into `doRequest` of GitHub client + wire `RecordAPIError`

**Wave:** 2 (S) · **Parallel-safe with TASK-292/293/295** · **Audit ref:** §2 Action #4, §3.2 P1 (CS-9), §3.4 P1

---

## Problem

`internal/adapters/github/client.go:119-164` (`doRequest`) makes the raw `httpClient.Do(req)` call with no retry. `internal/adapters/github/retry.go` defines `WithRetry`/`WithRetryVoid`/`extractRetryAfter` (handling 429 + `Retry-After`) but only 20 of 51 client methods wrap their calls in `WithRetry`. Endpoints not wrapped — `GetPullRequest`, `GetCombinedStatus`, `ListIssues`, `ListReleases`, `GetTagForSHA`, `GetJobLogs`, and 25+ others — surface transient 429/5xx as terminal errors.

Simultaneously, `Metrics.RecordAPIError` is dead code (defined `internal/autopilot/metrics.go:118`, called only from tests). `pilot_api_errors_total` always emits zero; `AlertTypeAPIErrorRateHigh` never fires.

## Approach

### Step 1 — Wrap doRequest with retry (S, ~45 min)

- `internal/adapters/github/client.go:119-164`: wrap the `httpClient.Do(req)` + status-check loop with `WithRetry`
- Pass endpoint label (derived from `req.URL.Path` pattern, e.g. `"/repos/:owner/:repo/issues"`) into `WithRetry` for log context

### Step 2 — Thread metrics recorder (S, ~30 min)

- Add `metricsRecorder MetricsRecorder` field to `Client` struct
- Add option `WithMetricsRecorder(m MetricsRecorder) ClientOption` (small interface — only needs `RecordAPIError(endpoint string)`)
- In `doRequest`, on non-2xx response after retries exhausted, call `metricsRecorder.RecordAPIError(endpointPattern)`
- `cmd/pilot/main.go` startup: pass `c.metrics` into `github.NewClient(WithMetricsRecorder(c.metrics))`

### Step 3 — Remove redundant call-site wrappers (S, ~30 min)

- Find all 20 callers currently doing `WithRetry(ctx, func(){…c.doRequest(…)…})`
- Replace with direct `c.doRequest(...)` since retry now lives in doRequest
- Verify no behavior change (the inner `WithRetry` did the same thing the outer one will do)

### Step 4 — Tests (S, ~45 min)

- `internal/adapters/github/client_test.go`:
  - `TestClient_DoRequest_RetriesOn429` — `httptest.Server` returns 429 with `Retry-After: 1`, then 200; assert `doRequest` succeeds
  - `TestClient_DoRequest_RetriesOn5xx` — same pattern with 503
  - `TestClient_DoRequest_RecordsAPIErrorOn5xx` — inject stub recorder, assert `RecordAPIError` called with endpoint label
  - `TestClient_DoRequest_NoRetryOn4xx` (other than 429) — assert single attempt

## Files to modify

- `internal/adapters/github/client.go`
- `internal/adapters/github/client_test.go`
- ~20 caller sites in `internal/adapters/github/*.go` (remove redundant `WithRetry`)
- `cmd/pilot/main.go` (wire metrics recorder)

## Test Strategy

- Unit: httptest-based retry tests as above
- Manual: induce a 429 (or wait for one); confirm retry in logs, `pilot_api_errors_total{endpoint="..."} > 0` in `/metrics`

## Effort

S (~2.5h total). One PR. ~80 LOC net delta.

## Out of Scope

- Wiring `RecordAPIError` in non-GitHub adapters (gitlab/azuredevops/linear/etc. have their own client patterns; separate task per adapter)
- Adding `pilot_github_api_latency_seconds` histogram (mentioned in audit §3.4 P2) — separate task
- Replacing `IsRateLimitError` substring matching with typed errors (audit §3.4 P3) — separate task
