package comms

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// CommandHandler processes bot commands with access to messenger and memory store.
// It provides a platform-agnostic implementation of all slash commands.
type CommandHandler struct {
	messenger         Messenger
	store             *memory.Store
	runCommandFunc    func(ctx context.Context, contextID, taskID string)
	statusQueryFunc   func(contextID string) (pending, running interface{})
	activeProjectFunc func(contextID string) (name, path string)
	projectListFunc   func() []interface{}
	setProjectFunc    func(contextID, projectName string) error
	cancelTaskFunc    func(ctx context.Context, contextID string) error
	stopTaskFunc      func(ctx context.Context, contextID string) error
	listTasksFunc     func() string
	briefGeneratorFunc func(ctx context.Context, contextID string) error // Platform-specific brief generation
}

// NewCommandHandler creates a command handler with messenger and optional memory store.
func NewCommandHandler(messenger Messenger, store *memory.Store) *CommandHandler {
	return &CommandHandler{
		messenger: messenger,
		store:     store,
	}
}

// SetRunCommandFunc sets the /run command handler (platform-specific).
func (c *CommandHandler) SetRunCommandFunc(f func(ctx context.Context, contextID, taskID string)) {
	c.runCommandFunc = f
}

// SetStatusQueryFunc sets the status query function.
func (c *CommandHandler) SetStatusQueryFunc(f func(contextID string) (pending, running interface{})) {
	c.statusQueryFunc = f
}

// SetActiveProjectFunc sets the active project query function.
func (c *CommandHandler) SetActiveProjectFunc(f func(contextID string) (name, path string)) {
	c.activeProjectFunc = f
}

// SetProjectListFunc sets the project list function.
func (c *CommandHandler) SetProjectListFunc(f func() []interface{}) {
	c.projectListFunc = f
}

// SetSetProjectFunc sets the project switching function.
func (c *CommandHandler) SetSetProjectFunc(f func(contextID, projectName string) error) {
	c.setProjectFunc = f
}

// SetCancelTaskFunc sets the task cancellation function.
func (c *CommandHandler) SetCancelTaskFunc(f func(ctx context.Context, contextID string) error) {
	c.cancelTaskFunc = f
}

// SetStopTaskFunc sets the task stopping function.
func (c *CommandHandler) SetStopTaskFunc(f func(ctx context.Context, contextID string) error) {
	c.stopTaskFunc = f
}

// SetListTasksFunc sets the task listing function.
func (c *CommandHandler) SetListTasksFunc(f func() string) {
	c.listTasksFunc = f
}

// SetBriefGeneratorFunc sets the brief generator function (platform-specific).
func (c *CommandHandler) SetBriefGeneratorFunc(f func(ctx context.Context, contextID string) error) {
	c.briefGeneratorFunc = f
}

// HandleCommand routes slash commands to their handlers.
func (c *CommandHandler) HandleCommand(ctx context.Context, contextID, text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/start", "/help":
		c.handleHelp(ctx, contextID)
	case "/status":
		c.handleStatus(ctx, contextID)
	case "/cancel":
		c.handleCancel(ctx, contextID)
	case "/queue":
		c.handleQueue(ctx, contextID)
	case "/projects":
		c.handleProjects(ctx, contextID)
	case "/project", "/switch":
		if len(args) > 0 {
			c.handleSwitch(ctx, contextID, args[0])
		} else {
			c.handleCurrentProject(ctx, contextID)
		}
	case "/history":
		c.handleHistory(ctx, contextID)
	case "/budget":
		c.handleBudget(ctx, contextID)
	case "/tasks", "/list":
		c.handleTasks(ctx, contextID)
	case "/run":
		if len(args) > 0 {
			if c.runCommandFunc != nil {
				c.runCommandFunc(ctx, contextID, args[0])
			} else {
				_ = c.messenger.SendText(ctx, contextID, "Usage: /run <task-id>\nExample: /run 07")
			}
		} else {
			_ = c.messenger.SendText(ctx, contextID, "Usage: /run <task-id>\nExample: /run 07")
		}
	case "/stop":
		c.handleStop(ctx, contextID)
	case "/brief":
		c.handleBrief(ctx, contextID)
	case "/nopr":
		if len(args) > 0 {
			c.handleNoPR(ctx, contextID, strings.Join(args, " "))
		} else {
			_ = c.messenger.SendText(ctx, contextID, "Usage: /nopr <task description>\nExecutes task without creating a PR.")
		}
	case "/pr":
		if len(args) > 0 {
			c.handleForcePR(ctx, contextID, strings.Join(args, " "))
		} else {
			_ = c.messenger.SendText(ctx, contextID, "Usage: /pr <task description>\nForces PR creation even for ephemeral-looking tasks.")
		}
	default:
		_ = c.messenger.SendText(ctx, contextID, "Unknown command. Use /help for available commands.")
	}
}

// handleHelp shows comprehensive help with all commands.
func (c *CommandHandler) handleHelp(ctx context.Context, contextID string) {
	helpText := `🤖 Pilot Bot

I execute tasks and answer questions about your codebase.

Commands
/status — Current task & queue status
/cancel — Cancel pending/running task
/queue — Show queued tasks
/projects — List configured projects
/switch <name> — Switch active project
/history — Recent task history
/budget — Show usage & costs
/brief — Generate daily summary
/help — This message

Task Commands
/tasks — Show task backlog
/run <id> — Execute task (e.g., /run 07)
/stop — Stop running task
/nopr <task> — Execute without creating PR
/pr <task> — Force PR creation

Quick Patterns
• 07 or task 07 — Run TASK-07
• status? — Project status
• todos? — List TODOs

What I Understand
• Tasks: "Create a file...", "Add feature..."
• Questions: "What handles auth?", "How does X work?"
• Greetings: "Hi", "Hello"

Note: Ephemeral commands (serve, run, etc.) auto-skip PR creation.`

	_ = c.messenger.SendText(ctx, contextID, helpText)
}

// handleStatus shows current status with running/pending/queue info.
func (c *CommandHandler) handleStatus(ctx context.Context, contextID string) {
	var sb strings.Builder
	sb.WriteString("📊 Status\n\n")

	// Get project name
	projName := "unknown"
	if c.activeProjectFunc != nil {
		name, _ := c.activeProjectFunc(contextID)
		if name != "" {
			projName = name
		}
	}
	sb.WriteString(fmt.Sprintf("📁 Project: %s\n", projName))

	// Get task status
	if c.statusQueryFunc != nil {
		pending, running := c.statusQueryFunc(contextID)

		// Running task
		if running != nil {
			// Type assertion to get task info (duck typing for compatibility)
			if taskIDer, ok := running.(interface{ GetTaskID() string }); ok {
				sb.WriteString(fmt.Sprintf("\n🔄 Running: %s\n", taskIDer.GetTaskID()))
				if startedAter, ok := running.(interface{ GetStartedAt() time.Time }); ok {
					elapsed := time.Since(startedAter.GetStartedAt()).Round(time.Second)
					sb.WriteString(fmt.Sprintf("   ⏱ %s\n", elapsed))
				}
			}
		}

		// Pending task
		if pending != nil {
			if taskIDer, ok := pending.(interface{ GetTaskID() string }); ok {
				sb.WriteString(fmt.Sprintf("\n⏳ Pending: %s\n", taskIDer.GetTaskID()))
				if createdAter, ok := pending.(interface{ GetCreatedAt() time.Time }); ok {
					age := time.Since(createdAter.GetCreatedAt()).Round(time.Second)
					sb.WriteString(fmt.Sprintf("   Awaiting confirmation (%s)\n", age))
				}
			}
		}
	}

	// Queue info from memory store
	if c.store != nil {
		queued, err := c.store.GetQueuedTasks(10)
		if err == nil && len(queued) > 0 {
			sb.WriteString(fmt.Sprintf("\n📋 Queue: %d task(s)\n", len(queued)))
		}
	}

	// No activity
	if c.statusQueryFunc == nil {
		sb.WriteString("\n✅ Ready for tasks")
	}

	_ = c.messenger.SendText(ctx, contextID, sb.String())
}

// handleCancel cancels pending or running task.
func (c *CommandHandler) handleCancel(ctx context.Context, contextID string) {
	if c.cancelTaskFunc != nil {
		if err := c.cancelTaskFunc(ctx, contextID); err == nil {
			return
		}
	}
	_ = c.messenger.SendText(ctx, contextID, "No task to cancel.")
}

// handleQueue shows queued tasks.
func (c *CommandHandler) handleQueue(ctx context.Context, contextID string) {
	if c.store == nil {
		_ = c.messenger.SendText(ctx, contextID, "📋 Queue not available (no memory store)")
		return
	}

	queued, err := c.store.GetQueuedTasks(10)
	if err != nil {
		_ = c.messenger.SendText(ctx, contextID, "❌ Failed to fetch queue")
		return
	}

	if len(queued) == 0 {
		_ = c.messenger.SendText(ctx, contextID, "📋 Queue is empty")
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 Task Queue\n\n")

	for i, task := range queued {
		age := time.Since(task.CreatedAt).Round(time.Minute)
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, task.TaskID))
		sb.WriteString(fmt.Sprintf("   📁 %s • ⏱ %s ago\n\n", filepath.Base(task.ProjectPath), age))
	}

	_ = c.messenger.SendText(ctx, contextID, sb.String())
}

// handleProjects lists configured projects.
func (c *CommandHandler) handleProjects(ctx context.Context, contextID string) {
	if c.projectListFunc == nil {
		_ = c.messenger.SendText(ctx, contextID,
			"📁 No projects configured.\n\nAdd projects to ~/.pilot/config.yaml")
		return
	}

	projects := c.projectListFunc()
	if len(projects) == 0 {
		_ = c.messenger.SendText(ctx, contextID,
			"📁 No projects configured.\n\nAdd projects to ~/.pilot/config.yaml")
		return
	}

	var sb strings.Builder
	sb.WriteString("📁 Projects\n\n")

	activeName := ""
	if c.activeProjectFunc != nil {
		activeName, _ = c.activeProjectFunc(contextID)
	}

	for _, p := range projects {
		// Duck typing for project info
		marker := ""
		if namer, ok := p.(interface{ GetName() string }); ok {
			if namer.GetName() == activeName {
				marker = " ✅"
			}
		}

		nav := ""
		if navigatorer, ok := p.(interface{ IsNavigator() bool }); ok {
			if navigatorer.IsNavigator() {
				nav = " 🧭"
			}
		}

		if namer, ok := p.(interface{ GetName() string }); ok {
			sb.WriteString(fmt.Sprintf("• %s%s%s\n", namer.GetName(), marker, nav))
		}

		if pather, ok := p.(interface{ GetPath() string }); ok {
			sb.WriteString(fmt.Sprintf("  %s\n\n", pather.GetPath()))
		}
	}

	_ = c.messenger.SendText(ctx, contextID, sb.String())
}

// handleSwitch switches to a different project.
func (c *CommandHandler) handleSwitch(ctx context.Context, contextID, projectName string) {
	if c.setProjectFunc == nil {
		_ = c.messenger.SendText(ctx, contextID, "Project switching not configured")
		return
	}

	if err := c.setProjectFunc(contextID, projectName); err != nil {
		_ = c.messenger.SendText(ctx, contextID,
			fmt.Sprintf("❌ Project '%s' not found\n\nUse /projects to see available projects", projectName))
		return
	}

	name := projectName
	if c.activeProjectFunc != nil {
		actualName, _ := c.activeProjectFunc(contextID)
		if actualName != "" {
			name = actualName
		}
	}

	_ = c.messenger.SendText(ctx, contextID, fmt.Sprintf("✅ Switched to %s", name))
}

// handleCurrentProject shows current active project.
func (c *CommandHandler) handleCurrentProject(ctx context.Context, contextID string) {
	if c.activeProjectFunc == nil {
		_ = c.messenger.SendText(ctx, contextID, "Active project: unknown\n\nUse /projects to see all")
		return
	}

	projName, projPath := c.activeProjectFunc(contextID)
	if projName == "" {
		projName = filepath.Base(projPath)
	}

	text := fmt.Sprintf("📁 Active: %s\n%s\n\nUse /projects to see all", projName, projPath)
	_ = c.messenger.SendText(ctx, contextID, text)
}

// handleHistory shows recent task history.
func (c *CommandHandler) handleHistory(ctx context.Context, contextID string) {
	if c.store == nil {
		_ = c.messenger.SendText(ctx, contextID, "📜 History not available (no memory store)")
		return
	}

	executions, err := c.store.GetRecentExecutions(10)
	if err != nil {
		_ = c.messenger.SendText(ctx, contextID, "❌ Failed to fetch history")
		return
	}

	if len(executions) == 0 {
		_ = c.messenger.SendText(ctx, contextID, "📜 No task history yet")
		return
	}

	var sb strings.Builder
	sb.WriteString("📜 Recent Tasks\n\n")

	for _, exec := range executions {
		// Status emoji
		emoji := "⏳"
		switch exec.Status {
		case "completed":
			emoji = "✅"
		case "failed":
			emoji = "❌"
		case "running":
			emoji = "🔄"
		}

		// Format duration
		duration := ""
		if exec.DurationMs > 0 {
			d := time.Duration(exec.DurationMs) * time.Millisecond
			duration = fmt.Sprintf(" • %s", d.Round(time.Second))
		}

		// Format time
		age := FormatTimeAgo(exec.CreatedAt)

		sb.WriteString(fmt.Sprintf("%s %s\n", emoji, exec.TaskID))
		sb.WriteString(fmt.Sprintf("   %s%s\n", age, duration))

		// Add PR link if present
		if exec.PRUrl != "" {
			sb.WriteString(fmt.Sprintf("   PR: %s\n", exec.PRUrl))
		}
		sb.WriteString("\n")
	}

	_ = c.messenger.SendText(ctx, contextID, sb.String())
}

// handleBudget shows usage and costs.
func (c *CommandHandler) handleBudget(ctx context.Context, contextID string) {
	if c.store == nil {
		_ = c.messenger.SendText(ctx, contextID, "💰 Budget not available (no memory store)")
		return
	}

	// Get current month's usage
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	summary, err := c.store.GetUsageSummary(memory.UsageQuery{
		Start: monthStart,
		End:   now,
	})
	if err != nil {
		_ = c.messenger.SendText(ctx, contextID, "❌ Failed to fetch usage data")
		return
	}

	var sb strings.Builder
	sb.WriteString("💰 Usage This Month\n\n")

	// Task count
	sb.WriteString(fmt.Sprintf("🎯 Tasks: %d\n", summary.TaskCount))

	// Token usage
	if summary.TokensTotal > 0 {
		tokensK := float64(summary.TokensTotal) / 1000
		sb.WriteString(fmt.Sprintf("🔤 Tokens: %.1fK\n", tokensK))
	}

	// Compute time
	if summary.ComputeMinutes > 0 {
		sb.WriteString(fmt.Sprintf("⏱ Compute: %d min\n", summary.ComputeMinutes))
	}

	// Costs breakdown
	sb.WriteString("\nCosts\n")
	if summary.TaskCost > 0 {
		sb.WriteString(fmt.Sprintf("• Tasks: $%.2f\n", summary.TaskCost))
	}
	if summary.TokenCost > 0 {
		sb.WriteString(fmt.Sprintf("• Tokens: $%.2f\n", summary.TokenCost))
	}
	if summary.ComputeCost > 0 {
		sb.WriteString(fmt.Sprintf("• Compute: $%.2f\n", summary.ComputeCost))
	}

	// Total
	sb.WriteString(fmt.Sprintf("\nTotal: $%.2f\n", summary.TotalCost))

	// Period info
	sb.WriteString(fmt.Sprintf("Period: %s - %s", monthStart.Format("Jan 2"), now.Format("Jan 2")))

	_ = c.messenger.SendText(ctx, contextID, sb.String())
}

// handleTasks shows task backlog.
func (c *CommandHandler) handleTasks(ctx context.Context, contextID string) {
	if c.listTasksFunc == nil {
		_ = c.messenger.SendText(ctx, contextID, "📋 No tasks found in .agent/tasks/")
		return
	}

	taskList := c.listTasksFunc()
	if taskList == "" {
		_ = c.messenger.SendText(ctx, contextID, "📋 No tasks found in .agent/tasks/")
		return
	}

	text := "📋 Task Backlog\n\n" + taskList
	_ = c.messenger.SendText(ctx, contextID, text)
}

// handleStop stops a running task.
func (c *CommandHandler) handleStop(ctx context.Context, contextID string) {
	if c.stopTaskFunc != nil {
		if err := c.stopTaskFunc(ctx, contextID); err == nil {
			return
		}
	}
	_ = c.messenger.SendText(ctx, contextID, "No task is currently running.")
}

// handleBrief generates and sends a daily brief on demand.
func (c *CommandHandler) handleBrief(ctx context.Context, contextID string) {
	if c.briefGeneratorFunc != nil {
		// Use the platform-specific brief generator (e.g., from Telegram adapter)
		_ = c.briefGeneratorFunc(ctx, contextID)
		return
	}

	if c.store == nil {
		_ = c.messenger.SendText(ctx, contextID, "📋 Brief not available (no memory store)")
		return
	}

	_ = c.messenger.SendText(ctx, contextID, "📊 Brief generation not configured")
}

// handleNoPR executes a task without creating a PR.
func (c *CommandHandler) handleNoPR(ctx context.Context, contextID, description string) {
	if c.runCommandFunc != nil {
		taskID := fmt.Sprintf("CMD-%d", time.Now().Unix())
		_ = c.messenger.SendText(ctx, contextID,
			fmt.Sprintf("🚀 Executing without PR: %s", TruncateText(description, 50)))
		// Note: In Telegram, this calls executeTaskWithOptions with forcePR=false
		// For shared handler, we just invoke the run command and let the adapter handle noPR semantics
		c.runCommandFunc(ctx, contextID, taskID)
	} else {
		_ = c.messenger.SendText(ctx, contextID,
			fmt.Sprintf("🚀 Executing without PR: %s", TruncateText(description, 50)))
	}
}

// handleForcePR executes a task and forces PR creation.
func (c *CommandHandler) handleForcePR(ctx context.Context, contextID, description string) {
	if c.runCommandFunc != nil {
		taskID := fmt.Sprintf("CMD-%d", time.Now().Unix())
		_ = c.messenger.SendText(ctx, contextID,
			fmt.Sprintf("🚀 Executing with PR: %s", TruncateText(description, 50)))
		// Note: In Telegram, this calls executeTaskWithOptions with forcePR=true
		// For shared handler, we just invoke the run command and let the adapter handle forcePR semantics
		c.runCommandFunc(ctx, contextID, taskID)
	} else {
		_ = c.messenger.SendText(ctx, contextID,
			fmt.Sprintf("🚀 Executing with PR: %s", TruncateText(description, 50)))
	}
}
