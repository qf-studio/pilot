# GH-286: Fix PR Creation False Failure Bugs

**Status**: 🚧 In Progress
**Created**: 2026-01-31
**Priority**: P1 (causes user confusion, false failure reports)

---

## Context

**Problem**:
Pilot reports "PR Failed" even when PR is successfully created. This happens when:
1. `gh pr create` returns "PR already exists" (exit code 1) but includes the existing PR URL
2. The `pilot-done` label is added even when `result.Success == false`

**Impact**:
- User sees "PR Failed" in dashboard but PR actually exists
- Issue gets `pilot-done` label despite reported failure
- Confusing UX, loss of trust in Pilot reliability

**Root Cause Analysis** (from GH-284 investigation):
- PR #285 was created successfully at 14:56:45Z
- Dashboard showed "PR Failed: failed to create PR..."
- Issue #284 has `pilot-done` label (incorrectly added)

---

## Implementation Plan

### Phase 1: Fix CreatePR to Handle "Already Exists"

**Goal**: Extract PR URL from "already exists" error message instead of failing

**File**: `internal/executor/git.go`

**Current code** (lines 80-96):
```go
func (g *GitOperations) CreatePR(ctx context.Context, title, body, baseBranch string) (string, error) {
    cmd := exec.CommandContext(ctx, "gh", "pr", "create", ...)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return "", fmt.Errorf("failed to create PR: %w: %s", err, output)
    }
    prURL := strings.TrimSpace(string(output))
    return prURL, nil
}
```

**Fix**:
```go
func (g *GitOperations) CreatePR(ctx context.Context, title, body, baseBranch string) (string, error) {
    cmd := exec.CommandContext(ctx, "gh", "pr", "create", ...)
    output, err := cmd.CombinedOutput()
    outputStr := string(output)

    if err != nil {
        // Check if PR already exists - gh returns exit 1 but includes URL
        if strings.Contains(outputStr, "already exists") {
            // Extract URL from message like:
            // "a pull request for branch ... already exists:\nhttps://github.com/..."
            if url := extractPRURL(outputStr); url != "" {
                return url, nil
            }
        }
        return "", fmt.Errorf("failed to create PR: %w: %s", err, output)
    }

    prURL := strings.TrimSpace(outputStr)
    return prURL, nil
}

// extractPRURL extracts GitHub PR URL from gh CLI output
func extractPRURL(output string) string {
    // Match https://github.com/{owner}/{repo}/pull/{number}
    re := regexp.MustCompile(`https://github\.com/[^/]+/[^/]+/pull/\d+`)
    if match := re.FindString(output); match != "" {
        return match
    }
    return ""
}
```

**Tasks**:
- [ ] Add `extractPRURL` helper function
- [ ] Update `CreatePR` to handle "already exists" case
- [ ] Add test for "PR already exists" scenario

### Phase 2: Fix pilot-done Label Logic

**Goal**: Only add `pilot-done` when `result.Success == true`

**File**: `cmd/pilot/main.go`

**Current code** (lines 1188-1191):
```go
} else if result != nil {
    if err := client.AddLabels(..., []string{github.LabelDone}); err != nil {
```

**Fix** (check `result.Success`):
```go
} else if result != nil && result.Success {
    if err := client.AddLabels(..., []string{github.LabelDone}); err != nil {
```

**Locations to fix**:
- `handleGitHubIssueWithResult` (~line 1188)
- `handleGitHubIssueWithMonitor` (check if same pattern exists)

**Tasks**:
- [ ] Fix `handleGitHubIssueWithResult` label logic
- [ ] Fix `handleGitHubIssueWithMonitor` label logic (if applicable)
- [ ] Add `pilot-failed` label when `result.Success == false`

### Phase 3: Add Tests

**Goal**: Prevent regression

**Tasks**:
- [ ] Add `TestCreatePR_AlreadyExists` in `git_test.go`
- [ ] Add test for label logic with `result.Success = false`

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| URL extraction | Regex vs string split | Regex | More robust, handles URL variations |
| Error handling | Return existing URL vs new error type | Return URL | Simpler, PR exists is success state |
| Label fix scope | Just done label vs add failed label | Both | Complete state handling |

---

## Files to Modify

| File | Change |
|------|--------|
| `internal/executor/git.go` | Add `extractPRURL`, update `CreatePR` |
| `internal/executor/git_test.go` | Add "PR already exists" test |
| `cmd/pilot/main.go` | Fix label logic in both handlers |

---

## Verify

```bash
# Run tests
make test

# Test PR already exists handling manually
# (Create PR, then try to create again)
gh pr create --title "test" --body "test" --base main

# Check for label logic
grep -n "result != nil" cmd/pilot/main.go | grep -i label
```

---

## Done

- [ ] `CreatePR` returns existing PR URL when "already exists"
- [ ] `pilot-done` label only added when `result.Success == true`
- [ ] `pilot-failed` label added when `result.Success == false`
- [ ] All tests pass
- [ ] Build succeeds

---

## Notes

**gh pr create output when PR exists**:
```
a pull request for branch "pilot/GH-284" into branch "main" already exists:
https://github.com/alekspetrov/pilot/pull/285
Exit code: 1
```

The URL is on the second line after "already exists:".

---

**Last Updated**: 2026-01-31
