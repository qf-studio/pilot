# Configuration

Pilot uses a YAML configuration file located at `~/.pilot/config.yaml`.

## Basic Configuration

```yaml
version: "1.0"

gateway:
  host: "127.0.0.1"
  port: 9090

projects:
  - name: "my-project"
    path: "~/Projects/my-project"
    navigator: true
    default_branch: main
```

## Adapter Configuration

### Telegram

```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"
    transcription:
      provider: openai
      openai_key: "${OPENAI_API_KEY}"
```

### GitHub

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

### Slack

```yaml
adapters:
  slack:
    enabled: true
    bot_token: "${SLACK_BOT_TOKEN}"
    channel: "#pilot-updates"
```

## Execution Settings

```yaml
orchestrator:
  execution:
    mode: sequential           # "sequential" or "parallel"
    wait_for_merge: true       # Wait for PR merge before next task
    poll_interval: 30s
    pr_timeout: 1h

executor:
  backend: claude-code         # "claude-code" or "opencode"
  direct_commit: false         # Commit directly to main (dangerous)
```

## Quality Gates

```yaml
quality:
  enabled: true
  max_retries: 3
  gates:
    - name: tests
      type: test
      command: "make test"
    - name: lint
      type: lint
      command: "make lint"
    - name: build
      type: build
      command: "make build"
```

## Alerts

```yaml
alerts:
  enabled: true
  defaults:
    cooldown: 5m
  channels:
    - name: telegram-alerts
      type: telegram
      severities: [critical, error, warning]
    - name: slack-ops
      type: slack
      slack:
        channel: "#pilot-alerts"
  rules:
    - name: task-failed
      type: task_failed
      channels: [telegram-alerts, slack-ops]
```

## Daily Briefs

```yaml
daily_brief:
  enabled: true
  schedule: "0 8 * * *"        # 8 AM daily
  timezone: "Europe/Berlin"
  channels:
    - type: slack
      channel: "#pilot-summary"
```

## Environment Variables

Pilot supports environment variable substitution in config:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Custom Anthropic API key |
| `ANTHROPIC_BASE_URL` | Custom API endpoint |
| `CLAUDE_CODE_USE_BEDROCK` | Set to `1` for AWS Bedrock |
| `CLAUDE_CODE_USE_VERTEX` | Set to `1` for Google Vertex AI |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `GITHUB_TOKEN` | GitHub personal access token |
| `SLACK_BOT_TOKEN` | Slack bot token |
| `OPENAI_API_KEY` | OpenAI key for voice transcription |

## Full Example

```yaml
version: "1.0"

gateway:
  host: "127.0.0.1"
  port: 9090

adapters:
  telegram:
    enabled: true
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"

  github:
    enabled: true
    token: "${GITHUB_TOKEN}"
    repo: "owner/repo"
    pilot_label: "pilot"
    polling:
      enabled: true
      interval: 30s

  slack:
    enabled: true
    bot_token: "${SLACK_BOT_TOKEN}"

orchestrator:
  execution:
    mode: sequential
    wait_for_merge: true
    poll_interval: 30s
    pr_timeout: 1h

projects:
  - name: "my-project"
    path: "~/Projects/my-project"
    navigator: true
    default_branch: main

quality:
  enabled: true
  gates:
    - name: tests
      type: test
      command: "make test"

alerts:
  enabled: true
  channels:
    - name: telegram-alerts
      type: telegram
      severities: [critical, error, warning]

daily_brief:
  enabled: true
  schedule: "0 8 * * *"
  timezone: "Europe/Berlin"

executor:
  backend: claude-code
```
