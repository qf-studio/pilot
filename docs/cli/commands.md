# CLI Reference

Complete command reference for Pilot.

## Core Commands

### `pilot start`

Start the Pilot daemon with configured inputs.

```bash
pilot start                          # Config-driven
pilot start --telegram               # Enable Telegram polling
pilot start --github                 # Enable GitHub issue polling
pilot start --linear                 # Enable Linear webhooks
pilot start --telegram --github      # Enable both
pilot start --dashboard              # With TUI dashboard
pilot start --no-gateway             # Polling only (no HTTP server)
pilot start --sequential             # Sequential execution mode
pilot start --parallel               # Parallel execution mode
pilot start --autopilot=stage        # Autopilot mode (dev/stage/prod)
pilot start --no-pr                  # Skip PR creation (local only)
pilot start --direct-commit          # Push directly to main (danger)
pilot start -p ~/Projects/myapp      # Specify project
pilot start --replace                # Kill existing instance first
```

### `pilot stop`

Stop the running Pilot daemon.

```bash
pilot stop
```

### `pilot status`

Show current running tasks and configuration.

```bash
pilot status
```

### `pilot init`

Initialize Pilot configuration.

```bash
pilot init                    # Interactive setup
pilot init --minimal          # Minimal config only
```

### `pilot version`

Show version information.

```bash
pilot version
```

---

## Task Execution

### `pilot task`

Execute a single task.

```bash
pilot task "Add user authentication"                    # Run in cwd
pilot task "Fix login bug" -p ~/Projects/myapp          # Specify project
pilot task "Add feature" --create-pr                    # Auto-create PR
pilot task "Refactor API" --verbose                     # Stream output
pilot task "Update docs" --dry-run                      # Preview only
pilot task "Quick fix" --no-branch                      # Skip branch
pilot task "Implement feature" --backend opencode       # Use OpenCode
```

| Flag | Description |
|------|-------------|
| `-p, --project` | Project path |
| `--create-pr` | Create PR after completion |
| `--verbose` | Stream raw JSON output |
| `--dry-run` | Show prompt without executing |
| `--no-branch` | Skip branch creation |
| `--backend` | Executor backend (claude-code/opencode) |

---

## GitHub Integration

### `pilot github run`

Execute a specific GitHub issue.

```bash
pilot github run 42                  # Run issue #42
pilot github run 42 --verbose        # With streaming output
```

---

## Upgrade & Maintenance

### `pilot upgrade`

Check for and install updates.

```bash
pilot upgrade                    # Check and upgrade
pilot upgrade check              # Only check for updates
pilot upgrade rollback           # Restore previous version
pilot upgrade --force            # Skip task completion wait
pilot upgrade --no-restart       # Don't restart after upgrade
pilot upgrade --yes              # Skip confirmation
```

### `pilot doctor`

Run system health checks.

```bash
pilot doctor
```

---

## Analytics & Reporting

### `pilot brief`

Generate and send daily/weekly briefs.

```bash
pilot brief                       # Show scheduler status
pilot brief --now                 # Generate and send immediately
pilot brief --weekly              # Generate weekly summary
```

### `pilot metrics`

View execution metrics.

```bash
pilot metrics summary             # Last 7 days overview
pilot metrics summary --days 30   # Last 30 days
pilot metrics daily               # Daily breakdown
pilot metrics projects            # Per-project stats
```

### `pilot usage`

View API usage and costs.

```bash
pilot usage summary               # Billable usage summary
pilot usage daily                 # Daily breakdown
pilot usage export --format json  # Export for billing
```

---

## Memory & Patterns

### `pilot patterns`

Query learned patterns from cross-project memory.

```bash
pilot patterns list               # List all patterns
pilot patterns search "auth"      # Search by keyword
pilot patterns stats              # Usage statistics
```

---

## Replay & Debug

### `pilot replay`

View and analyze execution recordings.

```bash
pilot replay list                 # List recordings
pilot replay list --project myapp # Filter by project
pilot replay list --failed        # Only failed executions
pilot replay show <id>            # Show recording metadata
pilot replay play <id>            # Interactive TUI replay
pilot replay analyze <id>         # Token/phase breakdown
pilot replay export <id>          # Export to HTML/JSON/MD
pilot replay export <id> --format json
```

---

## Budget & Cost Control

### `pilot budget`

View budget status and limits.

```bash
pilot budget                      # Show current budget status
pilot budget status               # Detailed breakdown
```

---

## Team Management

### `pilot team`

Manage team permissions.

```bash
pilot team list                   # List teams
pilot team add <name>             # Add team
pilot team remove <name>          # Remove team
```

---

## Infrastructure

### `pilot tunnel`

Manage Cloudflare tunnel for webhook ingress.

```bash
pilot tunnel start                # Start tunnel
pilot tunnel stop                 # Stop tunnel
pilot tunnel status               # Show tunnel status
```

### `pilot webhooks`

Configure outbound webhooks.

```bash
pilot webhooks list               # List configured webhooks
pilot webhooks add <url>          # Add webhook endpoint
pilot webhooks remove <id>        # Remove webhook
pilot webhooks test <id>          # Test webhook delivery
```

---

## Shell Completion

### `pilot completion`

Generate shell completion scripts.

```bash
pilot completion bash             # Bash completion
pilot completion zsh              # Zsh completion
pilot completion fish             # Fish completion

# Install for bash
pilot completion bash > /etc/bash_completion.d/pilot

# Install for zsh
pilot completion zsh > "${fpath[1]}/_pilot"
```

---

## Global Flags

These flags work with all commands:

| Flag | Description |
|------|-------------|
| `--config` | Config file path (default: ~/.pilot/config.yaml) |
| `--verbose` | Enable verbose output |
| `--quiet` | Suppress non-error output |
| `--help` | Show help for command |
