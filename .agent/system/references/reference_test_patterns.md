---
name: Test Patterns
description: Pilot Go test conventions — httptest mocks, wiring harness, test tokens
type: reference
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
**Mock servers:** Controller tests use `httptest.NewServer` with `switch on r.URL.Path`. GitHub client has `NewClientWithBaseURL` for test servers. Default mock pattern: `default: w.WriteHeader(http.StatusOK)` returns empty body; `doRequest` skips JSON unmarshal for empty body and returns zero-value struct.

**Test tokens:** Always import from `internal/testutil/tokens.go` — never hardcode realistic-looking secrets (GitHub push protection blocks them, even in test files).

**Wiring harness (v2.44.0):** `internal/wiring/` mirrors main.go's two init paths — `NewPollingHarness()` / `NewGatewayHarness()` with `t.TempDir()` SQLite + mock GitHub. 17 `Has*` accessors on Runner for nil-check inspection without reflect. Parity test runs 7 config combos × 17 fields = 119 assertions. Catches missing `Set*` calls in either init path or config flag not wiring a component. `make test-wiring` runs in the `go test ./...` gate.

**Why kept:** Test patterns are reference-level — same conventions across many files. Easier to recall here than re-derive.

**How to apply:** When writing new tests, follow these patterns rather than inventing new ones. When adding wiring (e.g., new pattern context, learning loop), update the wiring harness parity test or the polling/gateway path will silently diverge.
