# GitHub Integration

Pilot monitors GitHub issues and automatically implements tasks labeled with `pilot`.

## Setup

1. Create a GitHub Personal Access Token with `repo` scope
2. Configure in `~/.pilot/config.yaml`:

```yaml
adapters:
  github:
    enabled: true
    token: "${GITHUB_TOKEN}"
    repo: "owner/repo"
    pilot_label: "pilot"
    polling:
      enabled: true
      interval: 30s
```

3. Start Pilot:

```bash
pilot start --github
```

## Workflow

```
1. Create issue with 'pilot' label
         │
         ▼
2. Pilot claims issue (adds 'pilot/in-progress')
         │
         ▼
3. Creates branch: pilot/GH-{number}
         │
         ▼
4. Implements solution
         │
         ▼
5. Creates PR linked to issue
         │
         ▼
6. Adds 'pilot/done' label
         │
         ▼
7. You review and merge
```

## Labels

| Label | Description |
|-------|-------------|
| `pilot` | Request Pilot to implement this issue |
| `pilot/in-progress` | Pilot is currently working on it |
| `pilot/done` | Pilot finished, PR created |
| `pilot/failed` | Execution failed (check logs) |

## Writing Good Issues

Pilot works best with clear, scoped issues:

### Good Examples

```markdown
# Add rate limiting to /api/users endpoint

Implement rate limiting for the user API:
- 100 requests per minute per IP
- Return 429 with Retry-After header
- Use in-memory store (Redis later)

Files to modify:
- internal/api/middleware/
- internal/api/users/handler.go
```

```markdown
# Fix: Login redirects to 404

After successful login, users see a 404 instead of dashboard.

Steps to reproduce:
1. Go to /login
2. Enter valid credentials
3. Click submit
4. See 404 instead of /dashboard

Expected: Redirect to /dashboard
```

### Avoid

- Vague descriptions: "Improve performance"
- Multiple unrelated tasks in one issue
- Missing context or acceptance criteria

## Manual Trigger

Run a specific issue without polling:

```bash
pilot github run 42
```

## Sequential Execution

By default, Pilot processes one issue at a time:

```yaml
orchestrator:
  execution:
    mode: sequential       # One at a time
    wait_for_merge: true   # Wait for PR merge before next
```

This prevents merge conflicts and ensures code quality.

## Combining with Telegram

Run both inputs simultaneously:

```bash
pilot start --telegram --github
```

GitHub issues go into the same queue as Telegram tasks.

## PR Format

Pilot creates PRs with:

```markdown
## Summary
Brief description of changes

## Changes
- Added rate limiting middleware
- Updated user handler to use middleware
- Added tests for rate limiting

## Linked Issue
Closes #42

## Test Plan
- [ ] Run `make test`
- [ ] Manual test with curl
```

## Troubleshooting

### Issue not picked up

1. Check the label matches config: `pilot_label: "pilot"`
2. Verify token has `repo` scope
3. Check Pilot logs: `pilot start --github --verbose`

### PR creation fails

1. Ensure branch doesn't already exist
2. Check GitHub API rate limits
3. Verify token permissions

### Authentication errors

```bash
# Test your token
gh auth status

# Or manually
curl -H "Authorization: token $GITHUB_TOKEN" \
  https://api.github.com/user
```
