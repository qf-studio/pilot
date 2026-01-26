# TASK-08: GitHub Issues Adapter

**Status**: ✅ Complete
**Created**: 2026-01-26
**Completed**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Pilot currently only supports Linear as a ticket source. Many teams use GitHub Issues for task management, especially open-source projects. Without GitHub Issues support, these teams cannot use Pilot.

**Goal**:
Add GitHub Issues as an inbound adapter, allowing tickets created as GitHub Issues to trigger Pilot's autonomous development workflow.

**Success Criteria**:
- [x] Webhook receives GitHub Issue events (created, labeled)
- [x] Issues with "pilot" label trigger task execution
- [x] Task status synced back to GitHub (comments, labels)
- [x] PR linked to originating issue

---

## Implementation Summary

### Files Created

| File | Purpose |
|------|---------|
| `internal/adapters/github/types.go` | Config struct, Priority constants, DefaultConfig() |
| `internal/adapters/github/client.go` | GitHub REST API client with full issue operations |
| `internal/adapters/github/webhook.go` | Webhook handler with HMAC-SHA256 signature verification |
| `internal/adapters/github/converter.go` | Issue → Task conversion with criteria extraction |
| `internal/adapters/github/notifier.go` | Status updates (comments, labels) back to GitHub |
| `internal/adapters/github/webhook_test.go` | Tests for webhook handling |
| `internal/adapters/github/converter_test.go` | Tests for task conversion |
| `internal/adapters/github/client_test.go` | Tests for API client |

### Files Modified

| File | Changes |
|------|---------|
| `internal/config/config.go` | Added `Github *github.Config` to AdaptersConfig |
| `internal/gateway/server.go` | Added `/webhooks/github` endpoint |
| `internal/pilot/pilot.go` | Added GitHub client, webhook handler, notifier integration |
| `internal/orchestrator/orchestrator.go` | Added `ProcessGithubTicket()` method |

---

## API Client Methods

```go
// GetIssue fetches an issue by owner, repo, and number
GetIssue(ctx, owner, repo string, number int) (*Issue, error)

// AddComment adds a comment to an issue
AddComment(ctx, owner, repo string, number int, body string) (*Comment, error)

// AddLabels adds labels to an issue
AddLabels(ctx, owner, repo string, number int, labels []string) error

// RemoveLabel removes a label from an issue
RemoveLabel(ctx, owner, repo string, number int, label string) error

// UpdateIssueState updates an issue's state (open/closed)
UpdateIssueState(ctx, owner, repo string, number int, state string) error

// GetRepository fetches repository info
GetRepository(ctx, owner, repo string) (*Repository, error)
```

---

## Configuration

```yaml
adapters:
  github:
    enabled: true
    token: "${GITHUB_TOKEN}"           # Personal Access Token or GitHub App token
    webhook_secret: "${GITHUB_WEBHOOK_SECRET}"  # For HMAC signature verification
    pilot_label: "pilot"               # Customizable trigger label
```

Environment variables:
- `GITHUB_TOKEN` - Required for API calls
- `GITHUB_WEBHOOK_SECRET` - Optional (skip verification if not set)

---

## Workflow

1. **Webhook Received** → `/webhooks/github`
   - Validates `X-Hub-Signature-256` header (HMAC-SHA256)
   - Parses `X-GitHub-Event` header for event type

2. **Issue Filtered** → `WebhookHandler.Handle()`
   - Only processes `issues` events with `opened` or `labeled` actions
   - Checks if "pilot" label is present

3. **Task Created** → `converter.ConvertIssueToTask()`
   - Extracts title, description, priority from labels
   - Parses acceptance criteria from issue body
   - Maps to internal TaskInfo struct

4. **Status Updated** → `Notifier`
   - Adds `pilot-in-progress` label on start
   - Posts progress comments during execution
   - Adds `pilot-done` or `pilot-failed` on completion
   - Links PR when created

---

## Tests

```bash
# Run GitHub adapter tests
go test ./internal/adapters/github/... -v

# All 27 tests pass:
# - TestNewClient
# - TestGetIssue
# - TestAddComment
# - TestAddLabels
# - TestRemoveLabel
# - TestDoRequest_ErrorHandling
# - TestDefaultConfig
# - TestPriorityFromLabel
# - TestClientMethodSignatures
# - TestConvertIssueToTask
# - TestExtractPriority
# - TestExtractAcceptanceCriteria
# - TestExtractLabelNames
# - TestBuildTaskPrompt
# - TestPriorityName
# - TestExtractDescription
# - TestVerifySignature
# - TestHasPilotLabel
# - TestExtractIssueAndRepo
# - TestHandleIssueLabeled
# - TestExtractIssueAndRepo_MissingData
```

---

## Done

Observable outcomes that prove completion:

- [x] Webhook receives GitHub issue events
- [x] Issues with "pilot" label create tasks
- [x] Task progress posted as comments
- [x] PR linked to issue (closes #N)
- [x] Labels updated on completion
- [x] Tests pass (webhook, client, converter)

---

## References

- [GitHub Webhooks](https://docs.github.com/en/webhooks)
- [GitHub Issues API](https://docs.github.com/en/rest/issues)
- [GitHub Apps](https://docs.github.com/en/apps)
- [Webhook Signature Verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)

---

**Last Updated**: 2026-01-26
