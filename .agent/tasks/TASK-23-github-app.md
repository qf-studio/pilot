# TASK-23: GitHub App Integration

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Integrations

---

## Context

**Problem**:
Limited GitHub integration - only issues adapter. No PR comments, status checks, or deep integration.

**Goal**:
Full GitHub App with rich PR experience.

---

## Features

### PR Enhancements
- Auto-assign reviewers
- Add labels based on changes
- Post implementation summary as comment
- Link back to original ticket

### Status Checks
- "Pilot" status check on PRs
- Block merge until Pilot completes
- Show progress in GitHub UI

### PR Comments
- Explain what was changed and why
- Highlight key decisions
- Link to execution replay

### Issue Integration
- Close issues when PR merges
- Update issue with progress
- Link PRs to issues

---

## GitHub App Permissions

```yaml
permissions:
  issues: write
  pull_requests: write
  statuses: write
  contents: read
  metadata: read

events:
  - issues
  - pull_request
  - issue_comment
```

---

## Implementation

### Phase 1: Status Checks
- Report Pilot status on PRs
- Pass/fail based on quality gates

### Phase 2: PR Comments
- Post summary on PR creation
- Update on significant events

### Phase 3: Full App
- GitHub Marketplace listing
- One-click installation
- OAuth for user auth

---

## Example PR Comment

```markdown
## 🤖 Pilot Summary

**Task**: Add user authentication
**Duration**: 5m 32s
**Files Changed**: 8

### What Changed
- Added `src/auth/` with JWT implementation
- Updated `src/routes/` with protected routes
- Added tests with 95% coverage

### Decisions Made
- Used `jsonwebtoken` for JWT (widely adopted, secure)
- Stored tokens in httpOnly cookies (XSS protection)

[View execution replay →](...)
```

---

**Monetization**: Free tier with branding, paid removes branding
