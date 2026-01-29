package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C3AED")).
			Padding(0, 1)

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#10B981"))

	statusPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))

	statusFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#EF4444"))

	statusCompletedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#3B82F6"))

	progressBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7C3AED"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#374151")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// TaskDisplay represents a task for display
type TaskDisplay struct {
	ID       string
	Title    string
	Status   string
	Phase    string
	Progress int
	Duration string
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// CompletedTask represents a finished task for history
type CompletedTask struct {
	ID          string
	Title       string
	Status      string // "success" or "failed"
	Duration    string
	CompletedAt time.Time
}

// Model is the TUI model
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
	costPerMToken  float64 // Cost per million tokens (default Claude pricing)
}

// tickMsg is sent periodically to refresh the display
type tickMsg time.Time

// updateTasksMsg updates the task list
type updateTasksMsg []TaskDisplay

// addLogMsg adds a log entry
type addLogMsg string

// updateTokensMsg updates token usage
type updateTokensMsg TokenUsage

// addCompletedTaskMsg adds a completed task to history
type addCompletedTaskMsg CompletedTask

// NewModel creates a new dashboard model
func NewModel() Model {
	return Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0, // Default: $3 per million tokens (blended Claude rate)
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		tea.EnterAltScreen,
	)
}

// tickCmd creates a tick command
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "l":
			m.showLogs = !m.showLogs
		case "up", "k":
			if m.selectedTask > 0 {
				m.selectedTask--
			}
		case "down", "j":
			if m.selectedTask < len(m.tasks)-1 {
				m.selectedTask++
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		return m, tickCmd()

	case updateTasksMsg:
		m.tasks = msg

	case addLogMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 100 {
			m.logs = m.logs[1:]
		}

	case updateTokensMsg:
		m.tokenUsage = TokenUsage(msg)

	case addCompletedTaskMsg:
		m.completedTasks = append(m.completedTasks, CompletedTask(msg))
		// Keep only last 5 completed tasks
		if len(m.completedTasks) > 5 {
			m.completedTasks = m.completedTasks[len(m.completedTasks)-5:]
		}
	}

	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	if m.quitting {
		return "👋 Pilot stopped.\n"
	}

	var b strings.Builder

	// Header
	header := titleStyle.Render("🚀 Pilot Dashboard")
	b.WriteString(header)
	b.WriteString("\n\n")

	// Token usage and cost section
	metricsView := m.renderMetrics()
	b.WriteString(metricsView)
	b.WriteString("\n")

	// Tasks section
	tasksView := m.renderTasks()
	b.WriteString(tasksView)
	b.WriteString("\n")

	// Completed tasks history
	historyView := m.renderHistory()
	b.WriteString(historyView)
	b.WriteString("\n")

	// Logs section (if enabled)
	if m.showLogs {
		logsView := m.renderLogs()
		b.WriteString(logsView)
		b.WriteString("\n")
	}

	// Help
	help := helpStyle.Render("q: quit • l: toggle logs • ↑/↓: select task")
	b.WriteString(help)

	return b.String()
}

// renderMetrics renders token usage and cost
func (m Model) renderMetrics() string {
	var content strings.Builder

	content.WriteString("📊 Token Usage & Cost\n")
	content.WriteString(strings.Repeat("─", 60))
	content.WriteString("\n")

	// Token counts
	content.WriteString(fmt.Sprintf("  Input:  %8d tokens\n", m.tokenUsage.InputTokens))
	content.WriteString(fmt.Sprintf("  Output: %8d tokens\n", m.tokenUsage.OutputTokens))
	content.WriteString(fmt.Sprintf("  Total:  %8d tokens\n", m.tokenUsage.TotalTokens))
	content.WriteString("\n")

	// Cost calculation
	cost := float64(m.tokenUsage.TotalTokens) / 1_000_000 * m.costPerMToken
	costStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	content.WriteString(fmt.Sprintf("  Estimated Cost: %s\n", costStyle.Render(fmt.Sprintf("$%.4f", cost))))

	return boxStyle.Render(content.String())
}

// renderHistory renders completed tasks history
func (m Model) renderHistory() string {
	var content strings.Builder

	content.WriteString("📜 Recent History (Last 5)\n")
	content.WriteString(strings.Repeat("─", 60))
	content.WriteString("\n")

	if len(m.completedTasks) == 0 {
		content.WriteString("  No completed tasks yet\n")
	} else {
		for _, task := range m.completedTasks {
			var statusIcon string
			var statusStyle lipgloss.Style
			if task.Status == "success" {
				statusIcon = "✓"
				statusStyle = statusCompletedStyle
			} else {
				statusIcon = "✗"
				statusStyle = statusFailedStyle
			}
			status := statusStyle.Render(statusIcon)

			// Format completion time
			timeAgo := formatTimeAgo(task.CompletedAt)

			content.WriteString(fmt.Sprintf("  %s %-6s %-30s %s (%s)\n",
				status,
				task.ID,
				truncate(task.Title, 30),
				task.Duration,
				timeAgo,
			))
		}
	}

	return boxStyle.Render(content.String())
}

// formatTimeAgo formats a time as relative duration
func formatTimeAgo(t time.Time) string {
	duration := time.Since(t)
	if duration < time.Minute {
		return "just now"
	} else if duration < time.Hour {
		mins := int(duration.Minutes())
		return fmt.Sprintf("%dm ago", mins)
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		return fmt.Sprintf("%dh ago", hours)
	}
	return t.Format("Jan 2")
}

// renderTasks renders the tasks list
func (m Model) renderTasks() string {
	var content strings.Builder

	content.WriteString("📋 Tasks\n")
	content.WriteString(strings.Repeat("─", 60))
	content.WriteString("\n")

	if len(m.tasks) == 0 {
		content.WriteString("  No tasks running\n")
	} else {
		for i, task := range m.tasks {
			line := m.renderTask(task, i == m.selectedTask)
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	return boxStyle.Render(content.String())
}

// renderTask renders a single task
func (m Model) renderTask(task TaskDisplay, selected bool) string {
	// Status indicator
	var statusStyle lipgloss.Style
	var statusIcon string
	switch task.Status {
	case "running":
		statusStyle = statusRunningStyle
		statusIcon = "●"
	case "completed":
		statusStyle = statusCompletedStyle
		statusIcon = "✓"
	case "failed":
		statusStyle = statusFailedStyle
		statusIcon = "✗"
	default:
		statusStyle = statusPendingStyle
		statusIcon = "○"
	}

	status := statusStyle.Render(statusIcon)

	// Progress bar
	progressBar := m.renderProgressBar(task.Progress)

	// Selection indicator
	selector := "  "
	if selected {
		selector = "▶ "
	}

	// Task line
	return fmt.Sprintf("%s%s %s %-30s %s %3d%% %s",
		selector,
		status,
		task.ID,
		truncate(task.Title, 30),
		progressBar,
		task.Progress,
		task.Duration,
	)
}

// renderProgressBar renders a progress bar
func (m Model) renderProgressBar(progress int) string {
	width := 20
	filled := progress * width / 100
	empty := width - filled

	bar := progressBarStyle.Render(strings.Repeat("█", filled)) +
		strings.Repeat("░", empty)

	return "[" + bar + "]"
}

// renderLogs renders the logs section
func (m Model) renderLogs() string {
	var content strings.Builder

	content.WriteString("📝 Logs\n")
	content.WriteString(strings.Repeat("─", 60))
	content.WriteString("\n")

	// Show last 10 logs
	start := len(m.logs) - 10
	if start < 0 {
		start = 0
	}

	for _, log := range m.logs[start:] {
		content.WriteString("  ")
		content.WriteString(truncate(log, 56))
		content.WriteString("\n")
	}

	if len(m.logs) == 0 {
		content.WriteString("  No logs yet\n")
	}

	return boxStyle.Render(content.String())
}

// truncate truncates a string to max length
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// UpdateTasks sends updated tasks to the TUI
func UpdateTasks(tasks []TaskDisplay) tea.Cmd {
	return func() tea.Msg {
		return updateTasksMsg(tasks)
	}
}

// AddLog sends a log entry to the TUI
func AddLog(log string) tea.Cmd {
	return func() tea.Msg {
		return addLogMsg(log)
	}
}

// UpdateTokens sends updated token usage to the TUI
func UpdateTokens(input, output int) tea.Cmd {
	return func() tea.Msg {
		return updateTokensMsg(TokenUsage{
			InputTokens:  input,
			OutputTokens: output,
			TotalTokens:  input + output,
		})
	}
}

// AddCompletedTask sends a completed task to the TUI history
func AddCompletedTask(id, title, status, duration string) tea.Cmd {
	return func() tea.Msg {
		return addCompletedTaskMsg(CompletedTask{
			ID:          id,
			Title:       title,
			Status:      status,
			Duration:    duration,
			CompletedAt: time.Now(),
		})
	}
}

// Run starts the TUI
func Run() error {
	p := tea.NewProgram(
		NewModel(),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}
