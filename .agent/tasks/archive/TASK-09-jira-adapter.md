# TASK-09: Jira Adapter

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Assignee**: Pilot

---

## Context

**Problem**:
Enterprise teams predominantly use Jira for project management. Without Jira support, Pilot cannot serve this large market segment. Jira's webhook and API patterns differ from Linear, requiring a dedicated adapter.

**Goal**:
Add Jira as an inbound adapter, allowing Jira tickets to trigger Pilot's autonomous development workflow.

**Success Criteria**:
- [x] Webhook receives Jira issue events
- [x] Issues with "pilot" label trigger task execution
- [x] Task status synced back to Jira (transitions, comments)
- [x] PR link added to issue

---

## Implementation Summary

### Files Created

| File | Purpose |
|------|---------|
| `internal/adapters/jira/types.go` | Data structures, config, priority mapping |
| `internal/adapters/jira/client.go` | REST API client (Cloud + Server) |
| `internal/adapters/jira/webhook.go` | Webhook handler with label detection |
| `internal/adapters/jira/converter.go` | Issue to TaskInfo conversion |
| `internal/adapters/jira/notifier.go` | Status updates, comments, PR linking |
| `internal/adapters/jira/client_test.go` | Client tests |
| `internal/adapters/jira/converter_test.go` | Converter tests |
| `internal/adapters/jira/webhook_test.go` | Webhook handler tests |

### Files Modified

| File | Change |
|------|--------|
| `internal/config/config.go` | Added Jira config import and field |
| `internal/gateway/server.go` | Added `/webhooks/jira` endpoint |

---

## Features

### Webhook Handler
- Handles `jira:issue_created` and `jira:issue_updated` events
- Detects pilot label addition via changelog parsing
- Case-insensitive label matching
- HMAC signature verification (when secret configured)

### API Client
- Supports both Jira Cloud (REST v3) and Server (REST v2)
- Basic Auth with username:api_token
- Operations: GetIssue, AddComment, TransitionIssue, AddRemoteLink
- ADF (Atlassian Document Format) for Cloud comments, plain text for Server

### Task Converter
- Converts Jira issue to TaskInfo with ID, title, description
- Priority mapping (Highest/Blocker/Critical → PriorityHighest, etc.)
- Extracts acceptance criteria from description
- Filters pilot/priority labels from task labels

### Notifier
- Posts start/progress/completion/failure comments
- Transitions issues (by ID or status name)
- Adds PR as remote link (web link with GitHub icon)

---

## Configuration

```yaml
adapters:
  jira:
    enabled: true
    platform: "cloud"  # or "server"
    base_url: "https://company.atlassian.net"
    username: "pilot-bot@company.com"
    api_token: "${JIRA_API_TOKEN}"
    webhook_secret: "${JIRA_WEBHOOK_SECRET}"  # optional
    pilot_label: "pilot"
    transitions:
      in_progress: "21"  # Jira transition ID
      done: "31"
```

---

## Verification

```bash
# Tests pass
make test
# 42 tests for Jira adapter

# Lint clean
golangci-lint run ./internal/adapters/jira/...
# 0 issues

# Build succeeds
make build
```

---

## Done

Observable outcomes:

- [x] Webhook receives Jira issue events
- [x] Issues with "pilot" label create tasks
- [x] Issue transitions to "In Progress"
- [x] Progress posted as comments
- [x] PR link added to issue (remote link)
- [x] Issue transitions to "Done"
- [x] Works with Jira Cloud
- [x] Works with Jira Server
- [x] Tests pass (42 tests)

---

## References

- [Jira Cloud REST API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/)
- [Jira Webhooks](https://developer.atlassian.com/server/jira/platform/webhooks/)
- [Jira OAuth 2.0](https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/)
- [Jira Transitions](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/#api-rest-api-3-issue-issueidorkey-transitions-post)

---

**Last Updated**: 2026-01-26
