# Pilot: Onboarding Experience

## Goal

**Time to first PR: < 15 minutes**

User should see a real PR from Pilot within their first session. This is the "aha moment" that converts trial to paid.

---

## Onboarding Flow

### Step 1: Install (2 min)
```bash
# One command install
curl -fsSL https://pilot.dev/install.sh | sh

# Or with homebrew
brew install alekspetrov/tap/pilot
```

**Key:** No Docker, no complex dependencies. Single binary.

### Step 2: Setup Wizard (3 min)
```bash
pilot init
```

Wizard flow:
```
Welcome to Pilot!

? Select your ticket source:
  > Linear
    GitHub Issues
    Jira

? Enter your Linear API key: ****
  (Get one at: linear.app/settings/api)

? Which team should Pilot watch?
  > Engineering
    Product
    Design

? What label triggers Pilot?
  > pilot (recommended)
    ai
    auto

? Select notification channel:
  > Slack
    Telegram
    Email
    None

? Enter Slack webhook URL: ****

✓ Config saved to ~/.pilot/config.yaml
✓ Linear webhook registered
✓ Slack connection verified

Ready! Create a ticket with label "pilot" to start.
```

### Step 3: First Ticket (5 min)
User creates a ticket in Linear:

```
Title: Add health check endpoint
Description: Create GET /health that returns {"status": "ok"}
Labels: pilot
```

### Step 4: Watch Magic (5 min)
```bash
pilot status
```

Shows real-time:
```
⏳ Processing: Add health check endpoint

   Implementing   [████████████░░░░░░░░] 60%  LIN-123  32s

   [14:35:15] Started Navigator session
   [14:35:18] Analyzing codebase patterns...
   [14:35:25] Creating internal/api/health.go
   [14:35:40] Adding route to router.go
   [14:35:55] Writing tests...

✅ PR created: github.com/user/repo/pull/47
```

### Step 5: Review PR
Slack notification:
```
🎉 Pilot completed: Add health check endpoint
PR: github.com/user/repo/pull/47
Duration: 52s
Files changed: 3
```

User reviews PR, sees clean code following their patterns, merges.

**Aha moment achieved.**

---

## First-Time User Prompts

### No Navigator Detected
```
⚠️  No Navigator docs found in this project.

Pilot works best with Navigator for codebase context.
Run: navigator init

Or continue without (Pilot will still work, but won't know your patterns).
```

### No API Key
```
✗ Linear API key not found.

Get one at: linear.app/settings/api
Then run: pilot config set linear.api_key YOUR_KEY
```

### Wrong Permissions
```
✗ Linear API key doesn't have write access.

Pilot needs these scopes:
- issues:read
- webhooks:write

Regenerate at: linear.app/settings/api
```

---

## Suggested First Tickets

After setup, suggest easy wins:
```
Ready to try Pilot? Here are good first tickets:

1. "Add health check endpoint" - Simple API endpoint
2. "Add loading spinner to Button component" - UI enhancement
3. "Write tests for UserService" - Test coverage
4. "Update README with setup instructions" - Documentation

These are scoped well for Pilot. Avoid architecture decisions for first tickets.
```

---

## Failure Recovery

### Ticket Too Complex
```
⚠️ Pilot struggled with this ticket.

Reason: Ticket requires architectural decisions Pilot can't make.

Suggestions:
- Break into smaller tickets
- Add more context to description
- Mark as "needs-human" and handle manually

This ticket won't count against your monthly limit.
```

### PR Quality Issue
```
⚠️ This PR may need extra review.

Pilot detected:
- No existing tests to follow as examples
- Multiple architectural patterns in codebase
- Ambiguous ticket description

Recommendation: Review carefully, leave comments, Pilot learns from feedback.
```

### API Rate Limits
```
⚠️ Paused: Linear API rate limited.

Resuming in 60 seconds...
Your ticket will complete, just taking longer.
```

---

## Onboarding Emails

### Day 0: Welcome
```
Subject: Your first Pilot PR is waiting

You're set up. Now create a ticket with label "pilot" and watch it become a PR.

Good first tickets:
- Health check endpoints
- CRUD operations
- Test coverage
- Documentation updates

Stuck? Reply to this email.
```

### Day 3: Check-in
```
Subject: How's Pilot working?

You've completed {n} tickets with Pilot.

{if n == 0}
Haven't tried a ticket yet? Here's a simple one to start:
"Add GET /health endpoint returning {"status": "ok"}"

{else}
Nice! Your PRs had a {merge_rate}% merge rate.
That's {hours} hours of dev time saved.

{endif}

Questions? Hit reply.
```

### Day 7: Upgrade prompt (if on free)
```
Subject: You're at 4/5 free tickets

You've shipped {n} PRs with Pilot this week.

Upgrade to Solo ($29/mo) for 30 tickets/month.
That's about $1/ticket for work that takes devs 2+ hours.

[Upgrade now] or reply with questions.
```

---

## Metrics to Track

| Metric | Target | Action if missed |
|--------|--------|------------------|
| Install → first ticket | 60% | Simplify setup wizard |
| First ticket → PR created | 90% | Better ticket suggestions |
| First PR → merged | 70% | Improve code quality |
| Time to first PR | <15 min | Remove friction |
| Day 7 retention | 40% | Better onboarding emails |

---

## Common Failure Points

| Point | Why users drop | Fix |
|-------|----------------|-----|
| API key setup | "Too many steps" | OAuth flow instead of manual key |
| First ticket fails | "Doesn't work" | Better ticket suggestions, clearer errors |
| PR quality low | "Code is bad" | Better Navigator init, pattern detection |
| No feedback | "Did it work?" | Real-time status, Slack notifications |
| Forgot about it | "Never came back" | Day 3 email, daily digest |
