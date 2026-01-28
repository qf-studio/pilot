# TASK-29: Multi-Project Support

**Status**: Backlog
**Priority**: High
**Created**: 2026-01-26

---

## Overview

Enable Pilot to manage multiple projects from a single config file. Remove dependency on `-p` flag for telegram bot. Support project switching via commands.

---

## Current State

- `pilot telegram -p ~/path` - single project via flag
- Config has `projects: []` but not wired to bot
- Daily briefs have `filters.projects` but need integration

---

## Requirements

### 1. Config-Based Projects

```yaml
# ~/.pilot/config.yaml
projects:
  - name: pilot
    path: ~/Projects/startups/pilot
    navigator: true
    default_branch: main

  - name: navigator
    path: ~/.claude/plugins/navigator
    navigator: true

  - name: website
    path: ~/Projects/startups/aleks-petrov-next
    navigator: true

default_project: pilot
```

### 2. Telegram Commands

| Command | Action |
|---------|--------|
| `/projects` | List configured projects |
| `/project pilot` | Switch active project |
| `/project` | Show current project |

### 3. Project Context in Tasks

When user says "Start task 07":
1. Check current active project
2. Look for TASK-07 in that project's `.agent/tasks/`
3. Execute in that project's directory

### 4. Daily Briefs Per Project

```yaml
daily_brief:
  enabled: true
  schedule: "0 8 * * *"
  channels:
    - type: telegram
      channel: "283716179"
  # Aggregate all projects or filter
  filters:
    projects: []  # Empty = all configured projects
```

---

## Implementation

### Handler Changes

```go
type Handler struct {
    // ...existing fields...
    config        *config.Config
    activeProject map[string]string  // chatID -> projectPath
}

func (h *Handler) getActiveProject(chatID string) *config.ProjectConfig {
    if path, ok := h.activeProject[chatID]; ok {
        return h.config.GetProject(path)
    }
    return h.config.GetDefaultProject()
}
```

### New Commands

```go
func (h *Handler) handleProjectsCommand(ctx context.Context, chatID string) {
    var sb strings.Builder
    sb.WriteString("📁 *Projects*\n\n")

    active := h.getActiveProject(chatID)
    for _, p := range h.config.Projects {
        marker := ""
        if p.Path == active.Path {
            marker = " ✅"
        }
        sb.WriteString(fmt.Sprintf("• `%s`%s\n  %s\n", p.Name, marker, p.Path))
    }

    _, _ = h.client.SendMessage(ctx, chatID, sb.String(), "Markdown")
}

func (h *Handler) handleProjectCommand(ctx context.Context, chatID, projectName string) {
    // Switch active project for this chat
}
```

### Remove -p Flag Dependency

```go
// cmd/pilot/main.go
var telegramCmd = &cobra.Command{
    Use:   "telegram",
    Short: "Start Telegram bot",
    Run: func(cmd *cobra.Command, args []string) {
        cfg := config.Load()

        // No longer require -p flag
        // Use default project from config
        handler := telegram.NewHandler(cfg, runner)
        handler.StartPolling(ctx)
    },
}
```

---

## Acceptance Criteria

- [ ] `pilot telegram` works without `-p` flag (uses config)
- [ ] `/projects` lists all configured projects
- [ ] `/project <name>` switches active project
- [ ] Tasks execute in active project context
- [ ] Daily briefs aggregate across projects (configurable)
- [ ] Project context persists per chat

---

## Dependencies

- Existing config.Projects structure
- TASK-25: Telegram Commands (partial)
