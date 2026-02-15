# GH-1316: Fix Qwen Code Backend Bugs

**Status**: 🚧 In Progress
**Created**: 2026-02-15
**Assignee**: Pilot

---

## Context

**Problem**:
Qwen Code backend (shipped v1.9.0, GH-1315) has 5 bugs found during post-ship review:
1. Pricing data for Qwen3-Coder-Plus is 5x underpriced vs actual Alibaba Cloud pricing
2. No CLI version validation — `--output-format stream-json` requires v0.1.0+
3. Missing test coverage for `content` field in tool_result blocks (primary Qwen path untested)
4. No `session_not_found` error type — `--resume` failure gets classified as `Unknown`
5. No `--resume` failure fallback (Claude backend retries without `--from-pr`, Qwen doesn't)

**Goal**:
Fix all 5 issues in a single PR.

**Success Criteria**:
- [ ] Qwen pricing matches Alibaba Cloud published rates
- [ ] `IsAvailable()` validates CLI version supports stream-json
- [ ] Test exists for `content` field in tool_result blocks
- [ ] `session_not_found` error type added to QwenCodeError
- [ ] `--resume` fallback implemented when session not found
- [ ] All existing tests pass
- [ ] `go build ./...` succeeds

---

## Implementation Plan

### Fix 1: Correct Qwen Pricing Data

**File**: `internal/executor/runner.go` (~line 3251-3263)

Current (WRONG):
```go
case strings.Contains(modelLower, "480b") || strings.Contains(modelLower, "plus"):
    inputPrice = 0.22  // Qwen3-Coder-480B / Plus
    outputPrice = 1.00
```

Correct values (Alibaba Cloud International, 0-32K tier):
```go
case strings.Contains(modelLower, "480b") || strings.Contains(modelLower, "plus"):
    inputPrice = 1.00  // Qwen3-Coder-Plus (International)
    outputPrice = 5.00
```

Flash pricing ($0.30/$1.50) is already correct. Keep default/Next as-is.

### Fix 2: Add CLI Version Check to `IsAvailable()`

**File**: `internal/executor/backend_qwencode.go` (~line 172-176)

Current:
```go
func (b *QwenCodeBackend) IsAvailable() bool {
    _, err := exec.LookPath(b.config.Command)
    return err == nil
}
```

Change to:
```go
func (b *QwenCodeBackend) IsAvailable() bool {
    path, err := exec.LookPath(b.config.Command)
    if err != nil {
        return false
    }
    // Verify version supports --output-format stream-json (v0.1.0+)
    out, err := exec.Command(path, "--version").Output()
    if err != nil {
        b.log.Warn("qwen-code: could not determine version", "error", err)
        return true // Assume available if version check fails
    }
    // Parse version from output, warn if too old
    version := strings.TrimSpace(string(out))
    b.log.Info("qwen-code: detected version", "version", version)
    return true // Log-only for now, don't hard-gate
}
```

Approach: **Warn, don't hard-gate**. Version parsing is fragile with preview releases. Log the version for debugging, let execution fail naturally if incompatible.

### Fix 3: Add Test for `content` Field in tool_result

**File**: `internal/executor/backend_qwencode_test.go`

Add test case in the stream parsing test table for tool_result with `content` field:
```go
{
    name: "tool_result with content field",
    input: `{"type":"user","message":{"content":[{"type":"tool_result","content":"file contents here","is_error":false}]}}`,
    expected: StreamEvent{Type: EventTypeToolResult, ToolResult: "file contents here"},
},
```

This tests lines 514-518 of `backend_qwencode.go` — the primary code path for Qwen tool results.

### Fix 4: Add `session_not_found` Error Type

**File**: `internal/executor/backend_qwencode.go`

Add constant:
```go
QwenErrorTypeSessionNotFound QwenCodeErrorType = "session_not_found"
```

Update `classifyQwenCodeError()` to detect session-not-found patterns in stderr:
```go
case strings.Contains(stderrLower, "session not found") ||
     strings.Contains(stderrLower, "session expired") ||
     strings.Contains(stderrLower, "invalid session"):
    return QwenErrorTypeSessionNotFound
```

### Fix 5: Add `--resume` Failure Fallback

**File**: `internal/executor/backend_qwencode.go`

In `Execute()`, after the process exits with error, check if it's a session_not_found and `--resume` was used:
```go
if qErr.Type == QwenErrorTypeSessionNotFound && opts.ResumeSessionID != "" {
    b.log.Warn("qwen-code: session not found, retrying without --resume",
        "session_id", opts.ResumeSessionID)
    opts.ResumeSessionID = "" // Clear resume, retry
    return b.Execute(ctx, opts)
}
```

This mirrors `backend_claudecode.go` lines 190-206 (the `--from-pr` fallback pattern).

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Version check behavior | Hard gate vs warn-only | Warn-only | Preview release version strings are unpredictable; better to log and let execution fail naturally |
| Pricing tier | International vs China | International (0-32K) | Most users will use international API; tiered pricing too complex for cost estimation |
| Resume fallback | Single retry vs retrier | Single retry in Execute() | Matches Claude backend pattern; avoids infinite recursion with depth=1 guard |

---

## Files Modified

- `internal/executor/runner.go` — Fix Plus pricing ($0.22→$1.00 input, $1.00→$5.00 output)
- `internal/executor/backend_qwencode.go` — Version check in IsAvailable(), session_not_found error type, --resume fallback
- `internal/executor/backend_qwencode_test.go` — Add content field test case, version check test

---

## Verify

```bash
go build ./...
go test ./internal/executor/... -v -count=1
go vet ./internal/executor/...
```

---

## Done

- [ ] Plus pricing corrected to $1.00/$5.00
- [ ] `IsAvailable()` logs Qwen CLI version
- [ ] Test covers `content` field in tool_result
- [ ] `QwenErrorTypeSessionNotFound` constant exists
- [ ] `--resume` fallback retries without session ID
- [ ] All tests pass
- [ ] Build succeeds

---

**Last Updated**: 2026-02-15
