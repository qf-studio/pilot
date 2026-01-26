# TASK-08: GitHub Issues Adapter

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Pilot currently only supports Linear as a ticket source. Many teams use GitHub Issues for task management, especially open-source projects. Without GitHub Issues support, these teams cannot use Pilot.

**Goal**:
Add GitHub Issues as an inbound adapter, allowing tickets created as GitHub Issues to trigger Pilot's autonomous development workflow.

**Success Criteria**:
- [ ] Webhook receives GitHub Issue events (created, labeled)
- [ ] Issues with "pilot" label trigger task execution
- [ ] Task status synced back to GitHub (comments, labels)
- [ ] PR linked to originating issue

---

## Research

### GitHub Webhook Events

| Event | Trigger | Use Case |
|-------|---------|----------|
| `issues.opened` | New issue created | Optional auto-pilot |
| `issues.labeled` | Label added | Trigger on "pilot" label |
| `issues.assigned` | Assignee added | Trigger on "pilot" assignee |
| `issue_comment.created` | Comment added | Re-trigger or feedback |

### GitHub API Requirements

- **Authentication**: GitHub App or Personal Access Token
- **Permissions**: Issues (read/write), Pull Requests (write)
- **Webhook**: Configured at repo or org level
- **Rate Limits**: 5000 req/hour with token

### Webhook Payload (issues.labeled)

```json
{
  "action": "labeled",
  "issue": {
    "number": 42,
    "title": "Add user authentication",
    "body": "Implement OAuth login...",
    "labels": [{"name": "pilot"}]
  },
  "repository": {
    "full_name": "org/repo"
  }
}
```

---

## Implementation Plan

### Phase 1: Webhook Handler
**Goal**: Receive and validate GitHub webhook events

**Tasks**:
- [ ] Create `internal/adapters/github/webhook.go`
- [ ] Implement webhook signature verification (HMAC-SHA256)
- [ ] Parse issue events (opened, labeled)
- [ ] Filter for "pilot" label
- [ ] Route to gateway

**Files**:
- `internal/adapters/github/webhook.go` - Webhook handler
- `internal/gateway/router.go` - Add GitHub route

### Phase 2: GitHub API Client
**Goal**: Interact with GitHub Issues API

**Tasks**:
- [ ] Create GitHub API client with authentication
- [ ] Implement GetIssue, UpdateIssue, AddComment
- [ ] Implement label management (add "in-progress", "done")
- [ ] Link PRs to issues

**Files**:
- `internal/adapters/github/client.go` - API client
- `internal/adapters/github/types.go` - Data structures

### Phase 3: Task Conversion
**Goal**: Convert GitHub Issue to Pilot task

**Tasks**:
- [ ] Parse issue title/body into task description
- [ ] Extract acceptance criteria from body
- [ ] Map GitHub labels to task priority
- [ ] Determine target repository/project

**Files**:
- `internal/adapters/github/converter.go` - Issue to Task

### Phase 4: Status Sync
**Goal**: Update GitHub Issue with progress

**Tasks**:
- [ ] Add "pilot-in-progress" label on start
- [ ] Post comment with progress updates
- [ ] Link PR number when created
- [ ] Update labels on completion (remove pilot, add done)

**Files**:
- `internal/adapters/github/notifier.go` - Status updates

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Auth method | PAT, GitHub App | GitHub App | More secure, per-repo permissions |
| Trigger event | opened, labeled | labeled | Explicit opt-in, matches Linear pattern |
| Progress feedback | Labels only, Comments, Both | Both | Labels for status, comments for detail |
| PR linking | In body, Reference | Reference | Auto-closes issue on merge |

---

## Configuration

```yaml
adapters:
  github:
    enabled: true
    app_id: "${GITHUB_APP_ID}"
    private_key_path: "/path/to/private-key.pem"
    webhook_secret: "${GITHUB_WEBHOOK_SECRET}"
    trigger_label: "pilot"  # customizable
```

---

## Dependencies

**Requires**:
- [ ] GitHub App created and installed on repos
- [ ] Webhook URL publicly accessible
- [ ] Private key stored securely

**Related Tasks**:
- Builds on patterns from Linear adapter
- Shares webhook verification approach

---

## Verify

```bash
# Run tests
make test

# Manual test
# 1. Create issue in test repo
# 2. Add "pilot" label
# 3. Check Pilot logs for webhook receipt
# 4. Verify task created
```

---

## Done

Observable outcomes that prove completion:

- [ ] Webhook receives GitHub issue events
- [ ] Issues with "pilot" label create tasks
- [ ] Task progress posted as comments
- [ ] PR linked to issue (closes #N)
- [ ] Labels updated on completion
- [ ] Tests pass (webhook, client, converter)

---

## References

- [GitHub Webhooks](https://docs.github.com/en/webhooks)
- [GitHub Issues API](https://docs.github.com/en/rest/issues)
- [GitHub Apps](https://docs.github.com/en/apps)
- [Webhook Signature Verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)

---

**Last Updated**: 2026-01-26
