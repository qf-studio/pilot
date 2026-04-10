package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/memory"
)

// CommandHandler processes bot commands with access to memory store
type CommandHandler struct {
	handler *Handler
	store   *memory.Store
}

// NewCommandHandler creates a command handler with optional memory store
func NewCommandHandler(h *Handler, store *memory.Store) *CommandHandler {
	return &CommandHandler{
		handler: h,
		store:   store,
	}
}

// HandleCommand routes commands to their handlers
func (c *CommandHandler) HandleCommand(ctx context.Context, chatID, text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {
	case "/start", "/help":
		c.handleHelp(ctx, chatID)
	case "/status":
		c.handleStatus(ctx, chatID)
	case "/cancel":
		c.handleCancel(ctx, chatID)
	case "/queue":
		c.handleQueue(ctx, chatID)
	case "/projects":
		c.handleProjects(ctx, chatID)
	case "/project", "/switch":
		if len(args) > 0 {
			c.handleSwitch(ctx, chatID, args[0])
		} else {
			c.handleCurrentProject(ctx, chatID)
		}
	case "/history":
		c.handleHistory(ctx, chatID)
	case "/budget":
		c.handleBudget(ctx, chatID)
	case "/tasks", "/list":
		c.handleTasks(ctx, chatID)
	case "/run":
		if len(args) > 0 {
			c.handler.handleRunCommand(ctx, chatID, args[0])
		} else {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "Usage: /run <task-id>\nExample: /run 07", "")
		}
	case "/stop":
		c.handleStop(ctx, chatID)
	case "/voice":
		c.handler.sendVoiceSetupPrompt(ctx, chatID)
	case "/brief":
		c.handleBrief(ctx, chatID)
	case "/nopr":
		if len(args) > 0 {
			c.handleNoPR(ctx, chatID, strings.Join(args, " "))
		} else {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "Usage: /nopr <task description>\nExecutes task without creating a PR.", "")
		}
	case "/pr":
		if len(args) > 0 {
			c.handleForcePR(ctx, chatID, strings.Join(args, " "))
		} else {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "Usage: /pr <task description>\nForces PR creation even for ephemeral-looking tasks.", "")
		}
	default:
		_, _ = c.handler.client.SendMessage(ctx, chatID, "Unknown command. Use /help for available commands.", "")
	}
}

// handleHelp shows comprehensive help with all commands
func (c *CommandHandler) handleHelp(ctx context.Context, chatID string) {
	var helpText string
	if c.handler.plainTextMode {
		helpText = `🤖 Pilot Bot

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
	} else {
		helpText = `🤖 *Pilot Bot*

I execute tasks and answer questions about your codebase.

*Commands*
/status — Current task & queue status
/cancel — Cancel pending/running task
/queue — Show queued tasks
/projects — List configured projects
/switch <name> — Switch active project
/history — Recent task history
/budget — Show usage & costs
/brief — Generate daily summary
/help — This message

*Task Commands*
/tasks — Show task backlog
/run <id> — Execute task (e.g., /run 07)
/stop — Stop running task
/nopr <task> — Execute without creating PR
/pr <task> — Force PR creation

*Quick Patterns*
• 07 or task 07 — Run TASK-07
• status? — Project status
• todos? — List TODOs

*What I Understand*
• Tasks: "Create a file...", "Add feature..."
• Questions: "What handles auth?", "How does X work?"
• Greetings: "Hi", "Hello"

_Note: Ephemeral commands (serve, run, etc.) auto-skip PR creation._`
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, helpText, c.handler.getParseMode())
}

// handleStatus shows current status with running/pending/queue info
func (c *CommandHandler) handleStatus(ctx context.Context, chatID string) {
	var pending *comms.PendingTask
	var running *comms.RunningTask
	if c.handler.commsHandler != nil {
		pending = c.handler.commsHandler.GetPendingTask(chatID)
		running = c.handler.commsHandler.GetRunningTask(chatID)
	}

	activeProjectPath := c.handler.getActiveProjectPath(chatID)
	projName := filepath.Base(activeProjectPath)
	if info := c.handler.getActiveProjectInfo(chatID); info != nil {
		projName = info.Name
	}

	var sb strings.Builder
	plainText := c.handler.plainTextMode

	if plainText {
		sb.WriteString("📊 Status\n\n")
		sb.WriteString(fmt.Sprintf("📁 Project: %s\n", projName))
	} else {
		sb.WriteString("📊 *Status*\n\n")
		sb.WriteString(fmt.Sprintf("📁 Project: %s\n", escapeMarkdown(projName)))
	}

	// Running task
	if running != nil {
		elapsed := time.Since(running.StartedAt).Round(time.Second)
		if plainText {
			sb.WriteString(fmt.Sprintf("\n🔄 Running: %s\n", running.TaskID))
		} else {
			sb.WriteString(fmt.Sprintf("\n🔄 *Running*: `%s`\n", running.TaskID))
		}
		sb.WriteString(fmt.Sprintf("   ⏱ %s\n", elapsed))
	}

	// Pending task
	if pending != nil {
		age := time.Since(pending.CreatedAt).Round(time.Second)
		if plainText {
			sb.WriteString(fmt.Sprintf("\n⏳ Pending: %s\n", pending.TaskID))
		} else {
			sb.WriteString(fmt.Sprintf("\n⏳ *Pending*: `%s`\n", pending.TaskID))
		}
		sb.WriteString(fmt.Sprintf("   Awaiting confirmation (%s)\n", age))
	}

	// Queue info from memory store
	if c.store != nil {
		queued, err := c.store.GetQueuedTasks(10)
		if err == nil && len(queued) > 0 {
			if plainText {
				sb.WriteString(fmt.Sprintf("\n📋 Queue: %d task(s)\n", len(queued)))
			} else {
				sb.WriteString(fmt.Sprintf("\n📋 *Queue*: %d task(s)\n", len(queued)))
			}
		}
	}

	// No activity
	if running == nil && pending == nil {
		sb.WriteString("\n✅ Ready for tasks")
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, sb.String(), c.handler.getParseMode())
}

// handleCancel cancels pending or running task
func (c *CommandHandler) handleCancel(ctx context.Context, chatID string) {
	if c.handler.commsHandler != nil {
		if err := c.handler.commsHandler.CancelTask(ctx, chatID); err != nil {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "No task to cancel.", "")
		}
		return
	}
	_, _ = c.handler.client.SendMessage(ctx, chatID, "No task to cancel.", "")
}

// handleQueue shows queued tasks
func (c *CommandHandler) handleQueue(ctx context.Context, chatID string) {
	if c.store == nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "📋 Queue not available (no memory store)", "")
		return
	}

	queued, err := c.store.GetQueuedTasks(10)
	if err != nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "❌ Failed to fetch queue", "")
		return
	}

	if len(queued) == 0 {
		// Show pending task as fallback
		if c.handler.commsHandler != nil && c.handler.commsHandler.GetPendingTask(chatID) != nil {
			_, _ = c.handler.client.SendMessage(ctx, chatID,
				"📋 No queued tasks\n⏳ 1 pending confirmation", "")
		} else {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "📋 Queue is empty", "")
		}
		return
	}

	var sb strings.Builder
	plainText := c.handler.plainTextMode

	if plainText {
		sb.WriteString("📋 Task Queue\n\n")
	} else {
		sb.WriteString("📋 *Task Queue*\n\n")
	}

	for i, task := range queued {
		age := time.Since(task.CreatedAt).Round(time.Minute)
		if plainText {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, task.TaskID))
		} else {
			sb.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, task.TaskID))
		}
		sb.WriteString(fmt.Sprintf("   📁 %s • ⏱ %s ago\n\n", filepath.Base(task.ProjectPath), age))
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, sb.String(), c.handler.getParseMode())
}

// handleProjects lists configured projects
func (c *CommandHandler) handleProjects(ctx context.Context, chatID string) {
	if c.handler.projects == nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID,
			"📁 No projects configured.\n\nAdd projects to ~/.pilot/config.yaml", "")
		return
	}

	projects := c.handler.projects.ListProjects()
	if len(projects) == 0 {
		_, _ = c.handler.client.SendMessage(ctx, chatID,
			"📁 No projects configured.\n\nAdd projects to ~/.pilot/config.yaml", "")
		return
	}

	activeProjectPath := c.handler.getActiveProjectPath(chatID)
	plainText := c.handler.plainTextMode

	var sb strings.Builder
	if plainText {
		sb.WriteString("📁 Projects\n\n")
	} else {
		sb.WriteString("📁 *Projects*\n\n")
	}

	// Build keyboard for quick switching
	var keyboard [][]InlineKeyboardButton

	for _, p := range projects {
		marker := ""
		if p.Path == activeProjectPath {
			marker = " ✅"
		}
		nav := ""
		if p.Navigator {
			nav = " 🧭"
		}
		if plainText {
			sb.WriteString(fmt.Sprintf("• %s%s%s\n", p.Name, marker, nav))
			sb.WriteString(fmt.Sprintf("  %s\n\n", p.Path))
		} else {
			sb.WriteString(fmt.Sprintf("• *%s*%s%s\n", escapeMarkdown(p.Name), marker, nav))
			sb.WriteString(fmt.Sprintf("  `%s`\n\n", p.Path))
		}

		// Add keyboard button if not active
		if p.Path != activeProjectPath {
			keyboard = append(keyboard, []InlineKeyboardButton{
				{Text: fmt.Sprintf("📂 %s", p.Name), CallbackData: fmt.Sprintf("switch_%s", p.Name)},
			})
		}
	}

	if len(keyboard) > 0 {
		_, _ = c.handler.client.SendMessageWithKeyboard(ctx, chatID, sb.String(), c.handler.getParseMode(), keyboard)
	} else {
		_, _ = c.handler.client.SendMessage(ctx, chatID, sb.String(), c.handler.getParseMode())
	}
}

// handleSwitch switches to a different project
func (c *CommandHandler) handleSwitch(ctx context.Context, chatID, projectName string) {
	proj, err := c.handler.setActiveProject(chatID, projectName)
	if err != nil {
		// Try fuzzy match
		if c.handler.projects != nil {
			for _, p := range c.handler.projects.ListProjects() {
				if strings.Contains(strings.ToLower(p.Name), strings.ToLower(projectName)) {
					proj, err = c.handler.setActiveProject(chatID, p.Name)
					break
				}
			}
		}

		if err != nil {
			_, _ = c.handler.client.SendMessage(ctx, chatID,
				fmt.Sprintf("❌ Project '%s' not found\n\nUse /projects to see available projects", projectName), "")
			return
		}
	}

	nav := ""
	if proj.Navigator {
		nav = " 🧭"
	}
	var text string
	if c.handler.plainTextMode {
		text = fmt.Sprintf("✅ Switched to %s%s\n%s", proj.Name, nav, proj.Path)
	} else {
		text = fmt.Sprintf("✅ Switched to *%s*%s\n`%s`", escapeMarkdown(proj.Name), nav, proj.Path)
	}
	_, _ = c.handler.client.SendMessage(ctx, chatID, text, c.handler.getParseMode())
}

// handleCurrentProject shows current active project
func (c *CommandHandler) handleCurrentProject(ctx context.Context, chatID string) {
	activeProjectPath := c.handler.getActiveProjectPath(chatID)
	projInfo := c.handler.getActiveProjectInfo(chatID)

	var projName string
	nav := ""
	if projInfo != nil {
		projName = projInfo.Name
		if projInfo.Navigator {
			nav = " 🧭"
		}
	} else {
		projName = filepath.Base(activeProjectPath)
	}

	var text string
	if c.handler.plainTextMode {
		text = fmt.Sprintf("📁 Active: %s%s\n%s\n\nUse /projects to see all", projName, nav, activeProjectPath)
	} else {
		text = fmt.Sprintf("📁 Active: *%s*%s\n`%s`\n\nUse /projects to see all", escapeMarkdown(projName), nav, activeProjectPath)
	}
	_, _ = c.handler.client.SendMessage(ctx, chatID, text, c.handler.getParseMode())
}

// handleHistory shows recent task history
func (c *CommandHandler) handleHistory(ctx context.Context, chatID string) {
	if c.store == nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "📜 History not available (no memory store)", "")
		return
	}

	executions, err := c.store.GetRecentExecutions(10)
	if err != nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "❌ Failed to fetch history", "")
		return
	}

	if len(executions) == 0 {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "📜 No task history yet", "")
		return
	}

	var sb strings.Builder
	plainText := c.handler.plainTextMode

	if plainText {
		sb.WriteString("📜 Recent Tasks\n\n")
	} else {
		sb.WriteString("📜 *Recent Tasks*\n\n")
	}

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
		age := formatTimeAgo(exec.CreatedAt)

		if plainText {
			sb.WriteString(fmt.Sprintf("%s %s\n", emoji, exec.TaskID))
		} else {
			sb.WriteString(fmt.Sprintf("%s `%s`\n", emoji, exec.TaskID))
		}
		sb.WriteString(fmt.Sprintf("   %s%s\n", age, duration))

		// Add PR link if present
		if exec.PRUrl != "" {
			if plainText {
				sb.WriteString(fmt.Sprintf("   PR: %s\n", exec.PRUrl))
			} else {
				sb.WriteString(fmt.Sprintf("   [PR](%s)\n", exec.PRUrl))
			}
		}
		sb.WriteString("\n")
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, sb.String(), c.handler.getParseMode())
}

// handleBudget shows usage and costs
func (c *CommandHandler) handleBudget(ctx context.Context, chatID string) {
	if c.store == nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "💰 Budget not available (no memory store)", "")
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
		_, _ = c.handler.client.SendMessage(ctx, chatID, "❌ Failed to fetch usage data", "")
		return
	}

	var sb strings.Builder
	plainText := c.handler.plainTextMode

	if plainText {
		sb.WriteString("💰 Usage This Month\n\n")
	} else {
		sb.WriteString("💰 *Usage This Month*\n\n")
	}

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
	if plainText {
		sb.WriteString("\nCosts\n")
	} else {
		sb.WriteString("\n*Costs*\n")
	}
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
	if plainText {
		sb.WriteString(fmt.Sprintf("\nTotal: $%.2f\n", summary.TotalCost))
	} else {
		sb.WriteString(fmt.Sprintf("\n*Total*: $%.2f\n", summary.TotalCost))
	}

	// Period info
	if plainText {
		sb.WriteString(fmt.Sprintf("\nPeriod: %s - %s", monthStart.Format("Jan 2"), now.Format("Jan 2")))
	} else {
		sb.WriteString(fmt.Sprintf("\n_Period: %s - %s_", monthStart.Format("Jan 2"), now.Format("Jan 2")))
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, sb.String(), c.handler.getParseMode())
}

// handleTasks shows task backlog
func (c *CommandHandler) handleTasks(ctx context.Context, chatID string) {
	taskList := c.handler.fastListTasks()
	if taskList == "" {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "📋 No tasks found in .agent/tasks/", "")
		return
	}
	var text string
	if c.handler.plainTextMode {
		text = "📋 Task Backlog\n\n" + taskList
	} else {
		text = "📋 *Task Backlog*\n\n" + taskList
	}
	_, _ = c.handler.client.SendMessage(ctx, chatID, text, c.handler.getParseMode())
}

// handleStop stops a running task
func (c *CommandHandler) handleStop(ctx context.Context, chatID string) {
	if c.handler.commsHandler != nil {
		running := c.handler.commsHandler.GetRunningTask(chatID)
		if running == nil {
			_, _ = c.handler.client.SendMessage(ctx, chatID, "No task is currently running.", "")
			return
		}
		_ = c.handler.commsHandler.CancelTask(ctx, chatID)
		var text string
		if c.handler.plainTextMode {
			text = fmt.Sprintf("🛑 Stopped task %s", running.TaskID)
		} else {
			text = fmt.Sprintf("🛑 Stopped task `%s`", running.TaskID)
		}
		_, _ = c.handler.client.SendMessage(ctx, chatID, text, c.handler.getParseMode())
		return
	}
	_, _ = c.handler.client.SendMessage(ctx, chatID, "No task is currently running.", "")
}

// handleBrief generates and sends a daily brief on demand
func (c *CommandHandler) handleBrief(ctx context.Context, chatID string) {
	if c.store == nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID, "📋 Brief not available (no memory store)", "")
		return
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, "📊 Generating brief...", "")

	generator := briefs.NewGenerator(c.store, nil)
	brief, err := generator.GenerateDaily()
	if err != nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID,
			fmt.Sprintf("❌ Failed to generate brief: %s", err.Error()), "")
		return
	}

	// Format as plain text for Telegram
	formatter := briefs.NewPlainTextFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		_, _ = c.handler.client.SendMessage(ctx, chatID,
			fmt.Sprintf("❌ Failed to format brief: %s", err.Error()), "")
		return
	}

	_, _ = c.handler.client.SendMessage(ctx, chatID, text, "")
}

// formatTimeAgo formats a time as relative (e.g., "2h ago", "3d ago")
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

// HandleCallbackSwitch handles project switch callbacks
func (c *CommandHandler) HandleCallbackSwitch(ctx context.Context, chatID, projectName string) {
	c.handleSwitch(ctx, chatID, projectName)
}

// handleNoPR executes a task without creating a PR
func (c *CommandHandler) handleNoPR(ctx context.Context, chatID, description string) {
	taskID := fmt.Sprintf("TG-%d", time.Now().Unix())
	_, _ = c.handler.client.SendMessage(ctx, chatID,
		fmt.Sprintf("🚀 Executing without PR: %s", truncateForDisplay(description, 50)), "")
	if c.handler.commsHandler != nil {
		forcePR := false
		c.handler.commsHandler.ExecuteDirectTask(ctx, chatID, "", taskID, description, &comms.DirectTaskOpts{
			ForcePR: &forcePR,
		})
	}
}

// handleForcePR executes a task and forces PR creation
func (c *CommandHandler) handleForcePR(ctx context.Context, chatID, description string) {
	taskID := fmt.Sprintf("TG-%d", time.Now().Unix())
	_, _ = c.handler.client.SendMessage(ctx, chatID,
		fmt.Sprintf("🚀 Executing with PR: %s", truncateForDisplay(description, 50)), "")
	if c.handler.commsHandler != nil {
		forcePR := true
		c.handler.commsHandler.ExecuteDirectTask(ctx, chatID, "", taskID, description, &comms.DirectTaskOpts{
			ForcePR: &forcePR,
		})
	}
}

// truncateForDisplay truncates a string for display purposes
func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
