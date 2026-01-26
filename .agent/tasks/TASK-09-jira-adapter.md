# TASK-09: Jira Adapter

**Status**: 📋 Planned
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Enterprise teams predominantly use Jira for project management. Without Jira support, Pilot cannot serve this large market segment. Jira's webhook and API patterns differ from Linear, requiring a dedicated adapter.

**Goal**:
Add Jira as an inbound adapter, allowing Jira tickets to trigger Pilot's autonomous development workflow.

**Success Criteria**:
- [ ] Webhook receives Jira issue events
- [ ] Issues with "pilot" label trigger task execution
- [ ] Task status synced back to Jira (transitions, comments)
- [ ] PR link added to issue

---

## Research

### Jira Webhook Events

| Event | Trigger | Use Case |
|-------|---------|----------|
| `jira:issue_created` | New issue | Optional auto-pilot |
| `jira:issue_updated` | Issue changed | Trigger on label add |
| `comment_created` | Comment added | Re-trigger or feedback |

### Jira Flavors

| Platform | API | Auth | Notes |
|----------|-----|------|-------|
| Jira Cloud | REST v3 | OAuth 2.0 / API Token | Modern, recommended |
| Jira Server | REST v2 | Basic Auth / PAT | Self-hosted |
| Jira Data Center | REST v2 | PAT | Enterprise self-hosted |

### API Requirements

- **Jira Cloud**: OAuth 2.0 app or API token + email
- **Jira Server**: Personal Access Token
- **Permissions**: Browse projects, Edit issues, Add comments
- **Webhook**: Configured per project or globally

### Webhook Payload (issue_updated with label)

```json
{
  "webhookEvent": "jira:issue_updated",
  "issue": {
    "key": "PROJ-42",
    "fields": {
      "summary": "Add user authentication",
      "description": "Implement OAuth login...",
      "labels": ["pilot"],
      "issuetype": {"name": "Story"},
      "status": {"name": "To Do"}
    }
  },
  "changelog": {
    "items": [{"field": "labels", "toString": "pilot"}]
  }
}
```

---

## Implementation Plan

### Phase 1: Webhook Handler
**Goal**: Receive and validate Jira webhook events

**Tasks**:
- [ ] Create `internal/adapters/jira/webhook.go`
- [ ] Handle different Jira event types
- [ ] Parse changelog for label additions
- [ ] Filter for "pilot" label
- [ ] Support both Cloud and Server payloads

**Files**:
- `internal/adapters/jira/webhook.go` - Webhook handler
- `internal/gateway/router.go` - Add Jira route

### Phase 2: Jira API Client
**Goal**: Interact with Jira Issues API

**Tasks**:
- [ ] Create Jira API client (support Cloud + Server)
- [ ] Implement GetIssue, TransitionIssue, AddComment
- [ ] Handle authentication (OAuth 2.0 for Cloud, token for Server)
- [ ] Parse issue fields and custom fields

**Files**:
- `internal/adapters/jira/client.go` - API client
- `internal/adapters/jira/client_cloud.go` - Cloud-specific
- `internal/adapters/jira/client_server.go` - Server-specific
- `internal/adapters/jira/types.go` - Data structures

### Phase 3: Task Conversion
**Goal**: Convert Jira Issue to Pilot task

**Tasks**:
- [ ] Parse issue summary/description into task
- [ ] Extract acceptance criteria from description
- [ ] Map Jira priority to task priority
- [ ] Handle custom fields (story points, epic link)

**Files**:
- `internal/adapters/jira/converter.go` - Issue to Task

### Phase 4: Status Sync
**Goal**: Update Jira Issue with progress

**Tasks**:
- [ ] Transition issue to "In Progress" on start
- [ ] Add comment with progress updates
- [ ] Add PR link to issue (web link or custom field)
- [ ] Transition to "Done" on completion

**Files**:
- `internal/adapters/jira/notifier.go` - Status updates

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Platform priority | Cloud only, Both | Both | Enterprise needs Server support |
| Auth method | API Token, OAuth | Both | Cloud prefers OAuth, Server uses PAT |
| Status sync | Comments, Transitions | Both | Transitions for workflow, comments for detail |
| PR linking | Comment, Web Link | Web Link | Better UX in Jira UI |

---

## Configuration

```yaml
adapters:
  jira:
    enabled: true
    platform: "cloud"  # or "server"
    base_url: "https://company.atlassian.net"
    # For Cloud
    client_id: "${JIRA_CLIENT_ID}"
    client_secret: "${JIRA_CLIENT_SECRET}"
    # For Server
    api_token: "${JIRA_API_TOKEN}"
    username: "pilot-bot@company.com"
    trigger_label: "pilot"
    transitions:
      in_progress: "21"  # Jira transition ID
      done: "31"
```

---

## Dependencies

**Requires**:
- [ ] Jira app created (Cloud) or API token (Server)
- [ ] Webhook URL publicly accessible
- [ ] Issue transition IDs configured

**Related Tasks**:
- Builds on patterns from Linear adapter
- Shares status sync approach with GitHub adapter

---

## Verify

```bash
# Run tests
make test

# Manual test
# 1. Create issue in test project
# 2. Add "pilot" label
# 3. Check Pilot logs for webhook receipt
# 4. Verify task created
# 5. Check Jira for status transitions
```

---

## Done

Observable outcomes that prove completion:

- [ ] Webhook receives Jira issue events
- [ ] Issues with "pilot" label create tasks
- [ ] Issue transitions to "In Progress"
- [ ] Progress posted as comments
- [ ] PR link added to issue
- [ ] Issue transitions to "Done"
- [ ] Works with Jira Cloud
- [ ] Works with Jira Server (optional)
- [ ] Tests pass

---

## References

- [Jira Cloud REST API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/)
- [Jira Webhooks](https://developer.atlassian.com/server/jira/platform/webhooks/)
- [Jira OAuth 2.0](https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/)
- [Jira Transitions](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/#api-rest-api-3-issue-issueidorkey-transitions-post)

---

**Last Updated**: 2026-01-26
