# TASK-45: Wire Up testutil Package

**Status**: 🔲 Not Started
**Created**: 2026-01-28
**Priority**: P2 High
**Category**: Testing/DX

---

## Context

**Problem**:
`internal/testutil/` provides safe test tokens to avoid GitHub push protection blocks, but it's **never imported anywhere** despite being recommended in CLAUDE.md.

From CLAUDE.md:
```go
import "github.com/anthropics/pilot/internal/testutil"
token := testutil.FakeSlackBotToken
```

**Reality**: Zero test files use this. Tests still use inline fake tokens.

**Goal**:
Migrate all tests to use testutil constants for consistency and to prevent future push protection issues.

---

## Current State

### What Exists
- `internal/testutil/tokens.go` - Safe fake tokens:
  - `FakeSlackBotToken`
  - `FakeSlackAppToken`
  - `FakeGitHubToken`
  - `FakeOpenAIKey`
  - `FakeLinearAPIKey`
  - etc.

### What's Missing
- Zero imports of `github.com/anthropics/pilot/internal/testutil`
- Tests use inconsistent inline fake tokens

---

## Implementation Plan

### Phase 1: Audit Test Files
Find all test files with inline tokens:
```bash
grep -r "test.*token\|fake.*key\|mock.*secret" --include="*_test.go"
```

### Phase 2: Replace with testutil Constants
```go
// Before
token := "fake-slack-token"

// After
import "github.com/anthropics/pilot/internal/testutil"
token := testutil.FakeSlackBotToken
```

### Phase 3: Add Missing Constants
If tests need tokens not in testutil, add them:
```go
// internal/testutil/tokens.go
const (
    FakeTelegramBotToken = "test-telegram-bot-token"
    FakeJiraAPIToken     = "test-jira-api-token"
    // etc.
)
```

---

## Files to Modify

| Pattern | Action |
|---------|--------|
| `internal/adapters/slack/*_test.go` | Use testutil.FakeSlackBotToken |
| `internal/adapters/linear/*_test.go` | Use testutil.FakeLinearAPIKey |
| `internal/adapters/github/*_test.go` | Use testutil.FakeGitHubToken |
| `internal/config/*_test.go` | Use testutil constants |

---

## Acceptance Criteria

- [ ] All `*_test.go` files import testutil for tokens
- [ ] No inline fake tokens in test files
- [ ] `make check-secrets` passes
- [ ] Tests still pass after migration
- [ ] CLAUDE.md recommendation actually works
