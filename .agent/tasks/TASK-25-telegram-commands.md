# TASK-25: Telegram Bot Commands

**Status**: Backlog
**Priority**: Medium
**Created**: 2026-01-26

---

## Overview

Expand Telegram bot with additional commands for better task management and project control.

---

## Current State

### Existing Commands
- `/start`, `/help` — Show help
- `/status` — Bot status + pending task
- `/cancel` — Cancel pending task

### Existing Intents
- Greeting → friendly response
- Question → Claude answers (read-only)
- Task → confirm → execute

### Fast Paths (no Claude)
- `issues`, `tasks`, `backlog` → list from .agent/tasks/
- `status`, `progress` → read DEVELOPMENT-README.md
- `todos`, `fixmes` → grep codebase

---

## Proposed Commands

### Task Management
| Command | Function | Priority |
|---------|----------|----------|
| `/tasks` or `/list` | Show task backlog | High |
| `/run <id>` | Execute task (skip confirm) | High |
| `/stop` | Kill running task | High |
| `/log` | Show last execution result | Medium |

### Git/PR Operations
| Command | Function | Priority |
|---------|----------|----------|
| `/branch <name>` | Set branch for next task | Medium |
| `/pr` | Create PR from current branch | Medium |
| `/diff` | Show uncommitted changes | Low |

### Project Context
| Command | Function | Priority |
|---------|----------|----------|
| `/project <path>` | Switch project context | Low |
| `/projects` | List known projects | Low |

### Quick Actions (no slash)
| Pattern | Action |
|---------|--------|
| `07` or `task 07` | Run TASK-07 directly |
| `status?` | Project status |
| `todos?` | Grep TODOs |

---

## Implementation Notes

### `/run <id>` - Direct Execution
```go
// Skip confirmation for trusted task IDs
if strings.HasPrefix(text, "/run ") {
    taskID := strings.TrimPrefix(text, "/run ")
    // Load task from .agent/tasks/TASK-{id}-*.md
    // Execute without confirmation
}
```

### `/stop` - Kill Running Task
```go
// Use existing runner.Cancel() method
func (h *Handler) handleStop(ctx context.Context, chatID string) {
    // Find running task for this chat
    // Call runner.Cancel(taskID)
}
```

### `/branch` - Branch Context
```go
// Store branch preference per chat
type Handler struct {
    // ...
    branchOverride map[string]string // chatID -> branch
}
```

---

## Acceptance Criteria

- [ ] `/tasks` shows backlog with status indicators
- [ ] `/run 07` executes TASK-07 without confirmation
- [ ] `/stop` kills running task gracefully
- [ ] `/branch feature-x` sets branch for subsequent tasks
- [ ] `/pr` creates PR with task summary
- [ ] Quick patterns (`07`, `status?`) work as shortcuts

---

## Dependencies

- None (builds on existing handler.go)

---

## Notes

- Keep fast paths for common queries (no Claude spawn)
- Consider rate limiting for `/run` without confirmation
- Branch override should reset after task completion
