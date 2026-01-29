package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alekspetrov/pilot/internal/autopilot"
)

// Panel width (all panels same width)
const (
	panelTotalWidth = 69 // Total visual width including borders
	panelInnerWidth = 65 // panelTotalWidth - 4 (2 borders + 2 padding spaces)
)

// Styles (Kali Linux-inspired cyber aesthetic)
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00d4ff"))

	borderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#30363d"))

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00d4ff"))

	statusPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6e7681"))

	statusFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ff0055"))

	statusCompletedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00ff41"))

	progressBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00d4ff"))

	progressEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#30363d"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#c9d1d9"))

	costStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00ff41")).
			Bold(true)

	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffaa00"))
)

// AutopilotPanel displays autopilot status in the dashboard.
type AutopilotPanel struct {
	controller *autopilot.Controller
}

// NewAutopilotPanel creates an autopilot panel.
func NewAutopilotPanel(controller *autopilot.Controller) *AutopilotPanel {
	return &AutopilotPanel{controller: controller}
}

// View renders the autopilot panel content.
func (p *AutopilotPanel) View() string {
	var content strings.Builder
	w := panelInnerWidth

	if p.controller == nil {
		content.WriteString("  Disabled")
		return renderPanel("🤖 AUTOPILOT", content.String())
	}

	cfg := p.controller.Config()

	// Environment/Mode
	content.WriteString(dotLeader("Mode", string(cfg.Environment), w))
	content.WriteString("\n")

	// Active PRs
	prs := p.controller.GetActivePRs()
	if len(prs) == 0 {
		content.WriteString(dotLeader("Active PRs", "0", w))
	} else {
		content.WriteString(dotLeader("Active PRs", fmt.Sprintf("%d", len(prs)), w))
		content.WriteString("\n")

		for _, pr := range prs {
			icon := p.stageIcon(pr.Stage)
			prLine := fmt.Sprintf("  %s #%d: %s", icon, pr.PRNumber, pr.Stage)
			content.WriteString(prLine)
			content.WriteString("\n")
		}
	}

	// Circuit breaker status
	failures := p.controller.ConsecutiveFailures()
	if failures > 0 {
		content.WriteString("\n")
		failStr := fmt.Sprintf("%d/%d", failures, cfg.MaxFailures)
		content.WriteString(dotLeaderStyled("⚠️ Failures", failStr, warningStyle, w))
	}

	return renderPanel("🤖 AUTOPILOT", content.String())
}

// stageIcon returns an emoji icon for the PR stage.
func (p *AutopilotPanel) stageIcon(stage autopilot.PRStage) string {
	switch stage {
	case autopilot.StagePRCreated:
		return "📝"
	case autopilot.StageWaitingCI:
		return "⏳"
	case autopilot.StageCIPassed:
		return "✅"
	case autopilot.StageCIFailed:
		return "❌"
	case autopilot.StageAwaitApproval:
		return "👤"
	case autopilot.StageMerging:
		return "🔀"
	case autopilot.StageMerged:
		return "✅"
	case autopilot.StagePostMergeCI:
		return "🚀"
	case autopilot.StageFailed:
		return "💥"
	default:
		return "❓"
	}
}

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
	costPerMToken  float64
	autopilotPanel *AutopilotPanel
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
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(nil), // Disabled by default
	}
}

// NewModelWithAutopilot creates a dashboard model with autopilot integration.
func NewModelWithAutopilot(controller *autopilot.Controller) Model {
	return Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(controller),
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
		if len(m.completedTasks) > 5 {
			m.completedTasks = m.completedTasks[len(m.completedTasks)-5:]
		}
	}

	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	if m.quitting {
		return "Pilot stopped.\n"
	}

	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render("PILOT"))
	b.WriteString("\n\n")

	// Token usage
	b.WriteString(m.renderMetrics())
	b.WriteString("\n")

	// Tasks
	b.WriteString(m.renderTasks())
	b.WriteString("\n")

	// Autopilot panel
	b.WriteString(m.autopilotPanel.View())
	b.WriteString("\n")

	// History
	b.WriteString(m.renderHistory())
	b.WriteString("\n")

	// Logs (if enabled)
	if m.showLogs {
		b.WriteString(m.renderLogs())
		b.WriteString("\n")
	}

	// Help
	b.WriteString(helpStyle.Render("q: quit  l: logs  j/k: select"))

	return b.String()
}

// renderPanel builds a panel manually with guaranteed width
// Total width: panelTotalWidth (69 chars)
// Structure: ╭─ TITLE ─...─╮ / │ (space) content (space) │ / ╰─...─╯
func renderPanel(title string, content string) string {
	var lines []string

	// Top border: ╭─ TITLE ─────────────────────────────────────────────────────╮
	lines = append(lines, buildTopBorder(title))

	// Empty line padding
	lines = append(lines, buildEmptyLine())

	// Content lines
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, buildContentLine(line))
	}

	// Empty line padding
	lines = append(lines, buildEmptyLine())

	// Bottom border
	lines = append(lines, buildBottomBorder())

	return strings.Join(lines, "\n")
}

// buildTopBorder creates: ╭─ TITLE ─────...─────╮ with exact panelTotalWidth
func buildTopBorder(title string) string {
	// Characters: ╭ (1) + ─ (1) + space (1) + TITLE + space (1) + dashes + ╮ (1)
	// Available for dashes = panelTotalWidth - 5 - len(title)
	titleUpper := strings.ToUpper(title)
	prefix := "╭─ "
	prefixWidth := lipgloss.Width(prefix + titleUpper + " ")

	// Calculate dashes needed (each ─ is 1 visual char)
	dashCount := panelTotalWidth - prefixWidth - 1 // -1 for ╮
	if dashCount < 0 {
		dashCount = 0
	}

	// Style border chars dim, title bright
	return borderStyle.Render(prefix) + labelStyle.Render(titleUpper) + borderStyle.Render(" "+strings.Repeat("─", dashCount)+"╮")
}

// buildBottomBorder creates: ╰─────────────────────────────────────────────────╯
func buildBottomBorder() string {
	// ╰ + dashes + ╯
	dashCount := panelTotalWidth - 2
	line := "╰" + strings.Repeat("─", dashCount) + "╯"
	return borderStyle.Render(line)
}

// buildEmptyLine creates: │                                                                 │
func buildEmptyLine() string {
	// │ + spaces + │
	spaceCount := panelTotalWidth - 2
	border := borderStyle.Render("│")
	return border + strings.Repeat(" ", spaceCount) + border
}

// buildContentLine creates: │ (space) content padded/truncated (space) │
func buildContentLine(content string) string {
	// Available width for content = panelTotalWidth - 4 (│ + space + space + │)
	contentWidth := panelTotalWidth - 4

	// Pad or truncate content to exact width
	adjusted := padOrTruncate(content, contentWidth)

	// Only style borders, not content
	border := borderStyle.Render("│")
	return border + " " + adjusted + " " + border
}

// padOrTruncate ensures content is exactly targetWidth visual chars
func padOrTruncate(s string, targetWidth int) string {
	visualWidth := lipgloss.Width(s)

	if visualWidth == targetWidth {
		return s
	}

	if visualWidth > targetWidth {
		return truncateVisual(s, targetWidth)
	}

	// Pad with spaces
	return s + strings.Repeat(" ", targetWidth-visualWidth)
}

// truncateVisual truncates string to targetWidth visual chars, adding "..." if needed
func truncateVisual(s string, targetWidth int) string {
	if targetWidth <= 3 {
		return strings.Repeat(".", targetWidth)
	}

	// We need to truncate to targetWidth-3 and add "..."
	result := ""
	width := 0
	for _, r := range s {
		runeWidth := lipgloss.Width(string(r))
		if width+runeWidth > targetWidth-3 {
			break
		}
		result += string(r)
		width += runeWidth
	}

	// Pad to exactly targetWidth-3 if needed (in case of wide chars)
	for width < targetWidth-3 {
		result += " "
		width++
	}

	return result + "..."
}

// dotLeader creates a dot-leader line: "  Label .............. Value"
// Uses lipgloss.Width() for accurate visual width calculation
func dotLeader(label string, value string, totalWidth int) string {
	prefix := "  " + label + " "
	suffix := " " + value
	prefixWidth := lipgloss.Width(prefix)
	suffixWidth := lipgloss.Width(suffix)
	dotsNeeded := totalWidth - prefixWidth - suffixWidth
	if dotsNeeded < 3 {
		dotsNeeded = 3
	}
	return prefix + strings.Repeat(".", dotsNeeded) + suffix
}

// dotLeaderStyled creates a dot-leader with styled value
// Calculates width using raw value, then applies style
func dotLeaderStyled(label string, value string, style lipgloss.Style, totalWidth int) string {
	prefix := "  " + label + " "
	suffix := " " + value
	prefixWidth := lipgloss.Width(prefix)
	suffixWidth := lipgloss.Width(suffix)
	dotsNeeded := totalWidth - prefixWidth - suffixWidth
	if dotsNeeded < 3 {
		dotsNeeded = 3
	}
	// Apply style to value only (dots and spaces remain unstyled)
	return prefix + strings.Repeat(".", dotsNeeded) + " " + style.Render(value)
}

// renderMetrics renders token usage and cost
func (m Model) renderMetrics() string {
	var content strings.Builder
	w := panelInnerWidth // Content width between borders

	content.WriteString(dotLeader("Input", formatNumber(m.tokenUsage.InputTokens), w))
	content.WriteString("\n")
	content.WriteString(dotLeader("Output", formatNumber(m.tokenUsage.OutputTokens), w))
	content.WriteString("\n")

	// Cost with styled value
	cost := float64(m.tokenUsage.TotalTokens) / 1_000_000 * m.costPerMToken
	costValue := fmt.Sprintf("$%.4f", cost)
	content.WriteString(dotLeaderStyled("Est. Cost", costValue, costStyle, w))

	return renderPanel("TOKEN USAGE", content.String())
}

// formatNumber formats an integer with comma separators
func formatNumber(n int) string {
	if n == 0 {
		return "0"
	}

	str := fmt.Sprintf("%d", n)
	var result strings.Builder

	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result.WriteRune(',')
		}
		result.WriteRune(c)
	}

	return result.String()
}

// renderTasks renders the tasks list
func (m Model) renderTasks() string {
	var content strings.Builder

	if len(m.tasks) == 0 {
		content.WriteString("  No tasks running")
	} else {
		for i, task := range m.tasks {
			if i > 0 {
				content.WriteString("\n")
			}
			content.WriteString(m.renderTask(task, i == m.selectedTask))
		}
	}

	return renderPanel("TASKS", content.String())
}

// renderTask renders a single task
func (m Model) renderTask(task TaskDisplay, selected bool) string {
	// Status indicator
	var style lipgloss.Style
	var statusIcon string
	switch task.Status {
	case "running":
		style = statusRunningStyle
		statusIcon = "*"
	case "completed":
		style = statusCompletedStyle
		statusIcon = "+"
	case "failed":
		style = statusFailedStyle
		statusIcon = "x"
	default:
		style = statusPendingStyle
		statusIcon = "o"
	}

	status := style.Render(statusIcon)

	// Selection indicator
	selector := "  "
	if selected {
		selector = "> "
	}

	// Progress bar (14 chars)
	progressBar := m.renderProgressBar(task.Progress, 14)

	// Format: "> + GH-156  Title truncated here...  [██████░░░░░░░░] (100%)"
	// Columns: selector(2) + status(2) + id(8) + title(20) + bar(16) + pct(7) = 55
	return fmt.Sprintf("%s%s %-7s  %-20s  %s (%3d%%)",
		selector,
		status,
		task.ID,
		truncateVisual(task.Title, 20),
		progressBar,
		task.Progress,
	)
}

// renderProgressBar renders a progress bar
func (m Model) renderProgressBar(progress int, width int) string {
	filled := progress * width / 100
	empty := width - filled

	bar := progressBarStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", empty))

	return "[" + bar + "]"
}

// renderHistory renders completed tasks history
func (m Model) renderHistory() string {
	var content strings.Builder

	if len(m.completedTasks) == 0 {
		content.WriteString("  No completed tasks yet")
	} else {
		for i, task := range m.completedTasks {
			if i > 0 {
				content.WriteString("\n")
			}

			var statusIcon string
			var style lipgloss.Style
			if task.Status == "success" {
				statusIcon = "+"
				style = statusCompletedStyle
			} else {
				statusIcon = "x"
				style = statusFailedStyle
			}
			status := style.Render(statusIcon)
			timeAgo := dimStyle.Render(formatTimeAgo(task.CompletedAt))

			// Format: "  + GH-156  Title here...              5m12s   4m ago"
			content.WriteString(fmt.Sprintf("  %s %-7s  %-28s  %6s  %s",
				status,
				task.ID,
				truncateVisual(task.Title, 28),
				task.Duration,
				timeAgo,
			))
		}
	}

	return renderPanel("HISTORY", content.String())
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

// renderLogs renders the logs section
func (m Model) renderLogs() string {
	var content strings.Builder
	w := panelInnerWidth - 4 // Account for indent (2 spaces each side)

	if len(m.logs) == 0 {
		content.WriteString("  No logs yet")
	} else {
		start := len(m.logs) - 10
		if start < 0 {
			start = 0
		}

		for i, log := range m.logs[start:] {
			if i > 0 {
				content.WriteString("\n")
			}
			content.WriteString("  " + truncateVisual(log, w))
		}
	}

	return renderPanel("LOGS", content.String())
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
