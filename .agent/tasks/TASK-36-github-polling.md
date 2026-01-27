# TASK-36: GitHub Polling Mode

**Status**: 📋 Planned
**Priority**: High (P2)
**Created**: 2026-01-27

---

## Context

**Problem**:
GitHub webhooks require a public URL (ngrok/Cloudflare), which is friction for local setups. Mac Mini users running Pilot 24/7 need a zero-config solution.

**Goal**:
Poll GitHub for new issues with `pilot` label, eliminating webhook dependency.

---

## Design

### Configuration

```yaml
adapters:
  github:
    enabled: true
    token: "${GITHUB_TOKEN}"
    repo: alekspetrov/pilot
    polling:
      enabled: true
      interval: 30s
      label: pilot  # Watch for this label
```

### Polling Loop

```go
type GitHubPoller struct {
    client      *github.Client
    repo        string
    label       string
    interval    time.Duration
    lastChecked time.Time
    processed   map[int]bool  // Track processed issue numbers
}

func (p *GitHubPoller) Start(ctx context.Context) {
    ticker := time.NewTicker(p.interval)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            p.checkForNewIssues(ctx)
        }
    }
}

func (p *GitHubPoller) checkForNewIssues(ctx context.Context) {
    issues := p.client.ListIssues(ctx, p.repo, ListOptions{
        Labels: []string{p.label},
        State:  "open",
        Since:  p.lastChecked,
    })

    for _, issue := range issues {
        if !p.processed[issue.Number] && !hasLabel(issue, "pilot-in-progress") {
            p.queueTask(issue)
            p.processed[issue.Number] = true
        }
    }
    p.lastChecked = time.Now()
}
```

### Integration Points

1. **Telegram Handler** - Add poller as optional component
2. **Gateway Server** - Run poller alongside HTTP server
3. **Task Queue** - Share queue with webhook handler

### Deduplication

Prevent re-processing:
- Track processed issue numbers in memory
- Check for `pilot-in-progress` label before processing
- Persist processed IDs to SQLite for restart resilience

---

## Implementation

### Files to Create/Modify

| File | Change |
|------|--------|
| `internal/adapters/github/poller.go` | New - polling loop |
| `internal/adapters/github/types.go` | Add PollingConfig |
| `internal/adapters/github/client.go` | Add ListIssues method |
| `cmd/pilot/main.go` | Integrate poller into telegram/start commands |

### API Methods Needed

```go
// ListIssues returns issues matching criteria
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts *ListOptions) ([]*Issue, error)

type ListOptions struct {
    Labels []string
    State  string  // open, closed, all
    Since  time.Time
    Sort   string  // created, updated, comments
}
```

---

## Acceptance Criteria

- [ ] `polling.enabled: true` activates GitHub polling
- [ ] New issues with `pilot` label auto-queued
- [ ] Processed issues tracked (no duplicates)
- [ ] Works alongside Telegram bot
- [ ] `pilot status` shows polling state
- [ ] Graceful shutdown stops poller

---

## Testing

1. Create issue with `pilot` label
2. Verify Pilot picks it up within poll interval
3. Verify no re-processing after completion
4. Test restart resilience (processed IDs persist)

---

## Notes

- Start with 30s default interval (balance responsiveness vs API rate limits)
- GitHub API rate limit: 5000/hour authenticated
- Consider exponential backoff on errors
