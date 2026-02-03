# Quick Start

Get Pilot running in under 2 minutes.

## 1. Initialize Configuration

```bash
pilot init
```

This creates `~/.pilot/config.yaml` with sensible defaults.

## 2. Start Pilot

Choose your input method:

=== "GitHub Issues"

    ```bash
    pilot start --github
    ```

    Polls for issues labeled `pilot` every 30 seconds.

=== "Telegram Bot"

    ```bash
    pilot start --telegram
    ```

    Responds to messages in your Telegram bot.

=== "Both"

    ```bash
    pilot start --telegram --github
    ```

    Handles both inputs simultaneously.

## 3. Create a Task

=== "GitHub"

    1. Create a GitHub issue
    2. Add the `pilot` label
    3. Watch Pilot claim and execute it

=== "Telegram"

    Message your bot:

    ```
    Add rate limiting to the /api/users endpoint
    ```

=== "CLI"

    ```bash
    pilot task "Add rate limiting to /api/users"
    ```

## 4. Review and Merge

Pilot creates a PR with:

- Branch: `pilot/GH-{issue-number}`
- Linked to the original issue
- Test and lint results

Review the changes and merge when ready.

## Example Session

```bash
$ pilot start --github --dashboard

┌─ Pilot Dashboard ─────────────────────────────────────────┐
│                                                           │
│  Status: ● Running    Autopilot: off     Queue: 1        │
│                                                           │
│  Current Task                                             │
│  ├─ GH-42: Add user authentication                        │
│  ├─ Phase: Implementing (65%)                             │
│  └─ Duration: 2m 34s                                      │
│                                                           │
└───────────────────────────────────────────────────────────┘
```

## What's Next?

- [Configuration](config.md) - Customize Pilot's behavior
- [Telegram Bot](../features/telegram.md) - Chat-based interaction
- [Autopilot Mode](../features/autopilot.md) - Full automation
