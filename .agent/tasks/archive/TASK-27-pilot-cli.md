# TASK-27: Pilot CLI

**Status**: Complete
**Priority**: Medium
**Created**: 2026-01-26
**Completed**: 2026-01-28

---

## Overview

Expand Pilot CLI with direct task execution, queue management, and power user features. Enable CI/CD integration and scripting without Telegram dependency.

---

## Proposed Commands

### Task Execution
| Command | Description |
|---------|-------------|
| `pilot run 07` | Execute TASK-07 directly |
| `pilot run "fix auth bug"` | Ad-hoc task from description |
| `pilot run 07 --branch feature-x` | Execute on specific branch |
| `pilot run 07 --pr` | Execute and create PR |
| `pilot run 07 --dry-run` | Show what would execute |

### Queue Management
| Command | Description |
|---------|-------------|
| `pilot queue` | Show task queue |
| `pilot queue add "..."` | Add task without executing |
| `pilot queue clear` | Clear pending tasks |
| `pilot watch` | Auto-execute new tickets |

### Status & Monitoring
| Command | Description |
|---------|-------------|
| `pilot status` | Running tasks + progress |
| `pilot list` | Show backlog with status |
| `pilot logs [task-id]` | View execution history |
| `pilot stop [task-id]` | Kill running task |

### Analytics
| Command | Description |
|---------|-------------|
| `pilot cost` | Token spend today/week/month |
| `pilot stats` | Execution metrics |
| `pilot history` | Recent completions |

---

## Use Cases

### CI/CD Integration
```yaml
# .github/workflows/pilot.yml
- name: Run Pilot task
  run: pilot run ${{ github.event.issue.number }} --pr
```

### Scripting
```bash
# Execute multiple tasks
pilot run 07 && pilot run 08 && pilot run 09

# Batch execution
for task in 10 11 12; do
  pilot run $task --branch "batch-$(date +%Y%m%d)"
done
```

### Power User Workflow
```bash
# Morning routine
pilot list                    # Check backlog
pilot run 07                  # Start first task
pilot status                  # Monitor progress
pilot cost                    # Check spend
```

---

## Implementation Notes

### Command Structure
```go
// cmd/pilot/main.go
var rootCmd = &cobra.Command{Use: "pilot"}

var runCmd = &cobra.Command{
    Use:   "run [task-id or description]",
    Short: "Execute a task",
    Run:   runTask,
}

var listCmd = &cobra.Command{
    Use:   "list",
    Short: "Show task backlog",
    Run:   listTasks,
}
```

### Shared Executor
- Reuse `internal/executor.Runner` from Telegram handler
- Same progress reporting
- Same result formatting

---

## Acceptance Criteria

- [x] Interactive mode (`pilot` without args)
- [x] JSON output (`--json` flag on status, logs, config show)
- [x] `pilot logs [task-id]` - view task execution logs
- [x] `pilot config edit` - open config in $EDITOR
- [x] `pilot config validate` - validate config syntax
- [x] `pilot config show` - display current config
- [x] `pilot config path` - show config file path
- [x] Shell completions (bash/zsh/fish/powershell)
- [x] `--quiet` mode for scripts
- [x] Works in CI/CD pipelines

---

## Dependencies

- Existing executor (internal/executor)
- Task resolution (already in telegram handler)
