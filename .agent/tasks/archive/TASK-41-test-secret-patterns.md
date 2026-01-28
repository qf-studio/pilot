# TASK-41: Prevent Fake Secrets in Tests from Triggering Push Protection

**Status**: ✅ Complete
**Priority**: High (P1)
**Created**: 2026-01-28

---

## Context

**Problem**:
Pilot wrote tests with realistic-looking fake tokens (e.g., `xoxb-123456789012-1234567890123-abcdefghijklmnopqrstuvwx`). GitHub's push protection blocked all pushes because these patterns match real secret formats.

**Impact**:
- 9 branches blocked for hours
- Required manual intervention to unblock
- Lost time debugging "why can't I push?"

**Goal**:
Prevent this from happening again with automated checks and clear guidelines.

---

## Solution

### 1. Pre-commit Hook (Immediate)

Add a pre-commit hook that scans for common secret patterns in test files.

```bash
#!/bin/bash
# .git/hooks/pre-commit

# Check for realistic-looking secrets in test files
patterns=(
  'xoxb-[0-9]{10,}-[0-9]{10,}'  # Slack bot token
  'xoxa-[0-9]{10,}-[0-9]{10,}'  # Slack app token
  'sk-[a-zA-Z0-9]{32,}'          # OpenAI API key
  'ghp_[a-zA-Z0-9]{36}'          # GitHub PAT
  'gho_[a-zA-Z0-9]{36}'          # GitHub OAuth
)

for pattern in "${patterns[@]}"; do
  if git diff --cached --name-only | xargs grep -l '_test\.go$' | xargs grep -E "$pattern" 2>/dev/null; then
    echo "❌ Found realistic-looking secret pattern in test file"
    echo "   Use obviously fake tokens like 'test-token' or 'fake-slack-token'"
    exit 1
  fi
done
```

### 2. CI Check (Safety Net)

Add to `.github/workflows/ci.yml`:

```yaml
- name: Check for secret patterns in tests
  run: |
    if grep -rE 'xoxb-[0-9]{10,}|sk-[a-zA-Z0-9]{32,}|ghp_[a-zA-Z0-9]{36}' --include='*_test.go' .; then
      echo "::error::Found realistic secret patterns in test files"
      exit 1
    fi
```

### 3. Test Token Constants (Best Practice)

Create a shared test utilities file with safe fake tokens:

```go
// internal/testutil/tokens.go
package testutil

const (
    // FakeSlackToken is a test token that won't trigger secret scanning
    FakeSlackToken = "test-slack-bot-token"

    // FakeGitHubToken is a test token that won't trigger secret scanning
    FakeGitHubToken = "test-github-token"

    // FakeOpenAIKey is a test token that won't trigger secret scanning
    FakeOpenAIKey = "test-openai-api-key"
)
```

### 4. CLAUDE.md Guidelines

Add to project CLAUDE.md:

```markdown
## Test Guidelines

When writing tests that need API tokens:
- ❌ DON'T use realistic patterns: `xoxb-123456789012-1234567890123-abc`
- ✅ DO use obviously fake tokens: `test-slack-token`, `fake-api-key`
- ✅ DO use constants from `internal/testutil/tokens.go`

GitHub's push protection blocks realistic-looking secrets even in tests.
```

---

## Implementation

### Files to Create/Modify

| File | Change |
|------|--------|
| `.github/workflows/ci.yml` | Add secret pattern check |
| `internal/testutil/tokens.go` | New file with safe test tokens |
| `scripts/install-hooks.sh` | Install pre-commit hook |
| `CLAUDE.md` | Add test token guidelines |
| `Makefile` | Add `make install-hooks` target |

### Migration

Update existing test files to use safe tokens:
- `internal/adapters/slack/client_test.go` ✅ (already fixed)
- `internal/adapters/slack/notifier_test.go` ✅ (already fixed)
- Scan for any others

---

## Acceptance Criteria

- [x] Pre-commit hook blocks commits with realistic secret patterns
- [x] CI fails if realistic patterns found in test files
- [x] `internal/testutil/tokens.go` exists with safe constants
- [x] CLAUDE.md documents the guideline
- [x] `make install-hooks` installs the pre-commit hook
- [x] All existing tests use safe token patterns

---

## Testing

1. Try to commit a test file with `xoxb-123456789012-123456789`
2. Pre-commit hook should block
3. CI should fail if hook bypassed
4. Verify existing tests pass with safe tokens

---

## Notes

- GitHub allows specific secrets via their UI (we did this as workaround)
- But prevention is better than cure
- This also helps with other secret types (AWS keys, etc.)
