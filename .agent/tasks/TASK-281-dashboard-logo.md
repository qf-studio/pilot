# TASK-281: Add ASCII Logo to Dashboard

**Priority:** P3 - Enhancement
**Branch:** `pilot/GH-281`

## Request

Replace plain "PILOT" text in dashboard header with ASCII logo and version.

## Current State

`internal/dashboard/tui.go:307`:
```go
b.WriteString(titleStyle.Render("PILOT"))
b.WriteString("\n\n")
```

## Target State

```
   ██████╗ ██╗██╗      ██████╗ ████████╗
   ██╔══██╗██║██║     ██╔═══██╗╚══██╔══╝
   ██████╔╝██║██║     ██║   ██║   ██║
   ██╔═══╝ ██║██║     ██║   ██║   ██║
   ██║     ██║███████╗╚██████╔╝   ██║
   ╚═╝     ╚═╝╚══════╝ ╚═════╝    ╚═╝
   Pilot v0.5.1
```

## Implementation

### 1. Update Model struct (line ~174)

Add version field:
```go
type Model struct {
    tasks          []TaskDisplay
    logs           []string
    width          int
    height         int
    showLogs       bool
    selectedTask   int
    quitting       bool
    tokenUsage     TokenUsage
    completedTasks []CompletedTask
    costPerMToken  float64
    autopilotPanel *AutopilotPanel
    version        string  // ADD THIS
}
```

### 2. Update NewModel() (line ~204)

```go
func NewModel(version string) Model {
    return Model{
        tasks:          []TaskDisplay{},
        logs:           []string{},
        showLogs:       true,
        completedTasks: []CompletedTask{},
        costPerMToken:  3.0,
        autopilotPanel: NewAutopilotPanel(nil),
        version:        version,  // ADD THIS
    }
}
```

### 3. Update NewModelWithAutopilot() (line ~216)

```go
func NewModelWithAutopilot(version string, controller *autopilot.Controller) Model {
    return Model{
        // ... existing fields ...
        version:        version,
    }
}
```

### 4. Add import for banner package

```go
import (
    // ... existing imports ...
    "github.com/alekspetrov/pilot/internal/banner"
)
```

### 5. Update View() header rendering (line ~307)

Replace:
```go
b.WriteString(titleStyle.Render("PILOT"))
b.WriteString("\n\n")
```

With:
```go
// Render ASCII logo with styling
for _, line := range strings.Split(strings.TrimSpace(banner.Logo), "\n") {
    b.WriteString(titleStyle.Render(line))
    b.WriteString("\n")
}
b.WriteString(dimStyle.Render(fmt.Sprintf("   Pilot v%s", m.version)))
b.WriteString("\n\n")
```

### 6. Update callers in cmd/pilot/

Find where `NewModel()` and `NewModelWithAutopilot()` are called and pass `version` parameter.

Likely in `cmd/pilot/start.go` or similar.

## Files to Modify

- `internal/dashboard/tui.go` - Add version field, update constructors, update View()
- `cmd/pilot/start.go` (or wherever dashboard is initialized) - Pass version to NewModel

## Done

- [ ] Model has version field
- [ ] NewModel() accepts version parameter
- [ ] NewModelWithAutopilot() accepts version parameter
- [ ] View() renders ASCII logo with titleStyle
- [ ] Version displayed under logo
- [ ] All callers updated
- [ ] Tests pass
