# TASK-27: Pilot CLI

**Status**: Backlog
**Priority**: Medium
**Created**: 2026-01-26

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

- [ ] `pilot run 07` executes task and shows progress
- [ ] `pilot list` shows backlog with status
- [ ] `pilot status` shows running tasks
- [ ] `pilot stop` kills running task
- [ ] `pilot cost` shows token spend
- [ ] Works in CI/CD pipelines
- [ ] Supports piping and scripting

---

## Dependencies

- Existing executor (internal/executor)
- Task resolution (already in telegram handler)
