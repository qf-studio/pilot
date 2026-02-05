package dashboard

import (
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alekspetrov/pilot/internal/autopilot"
	"github.com/alekspetrov/pilot/internal/banner"
	"github.com/alekspetrov/pilot/internal/memory"
)

// Panel width (all panels same width)
const (
	panelTotalWidth = 69 // Total visual width including borders
	panelInnerWidth = 65 // panelTotalWidth - 4 (2 borders + 2 padding spaces)
)

// Metrics card dimensions
const (
	cardWidth      = 21 // 21*3 + 1*2 = 65 ≤ panelTotalWidth
	cardInnerWidth = 17 // cardWidth - 4 (border + padding)
	cardGap        = 1  // space between cards
)

// sparkBlocks maps normalized levels (0-8) to Unicode block elements for sparkline rendering.
var sparkBlocks = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// MetricsCardData holds aggregated metrics for the dashboard metrics cards.
type MetricsCardData struct {
	TotalTokens, InputTokens, OutputTokens int
	TotalCostUSD, CostPerTask              float64
	TotalTasks, Succeeded, Failed          int
	TokenHistory []int64   // 7 days
	CostHistory  []float64 // 7 days
	TaskHistory  []int     // 7 days
}

// Styles (muted terminal aesthetic)
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7eb8da")) // steel blue

	borderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3d4450")) // slate

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7eb8da")) // steel blue

	statusPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6e7681"))

	statusFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#d48a8a")) // dusty rose

	statusCompletedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7ec699")) // sage green

	progressBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7eb8da")) // steel blue

	progressEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#3d4450")) // slate

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#c9d1d9"))

	costStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7ec699")). // sage green
			Bold(true)

	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d4a054")) // amber

	orangeBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#d4a054")) // amber

	orangeLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#d4a054")) // amber
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
		return renderPanel("AUTOPILOT", content.String())
	}

	cfg := p.controller.Config()

	// Environment/Mode
	content.WriteString(dotLeader("Mode", string(cfg.Environment), w))
	content.WriteString("\n")

	// Release status
	if cfg.Release != nil && cfg.Release.Enabled {
		content.WriteString(dotLeader("Auto-release", "enabled", w))
	} else {
		content.WriteString(dotLeader("Auto-release", "disabled", w))
	}
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
			// Show PR number and stage with time in stage
			timeInStage := p.formatDuration(time.Since(pr.CreatedAt))
			prLine := fmt.Sprintf("  %s #%d: %s (%s)", icon, pr.PRNumber, pr.Stage, timeInStage)
			content.WriteString(prLine)
			content.WriteString("\n")

			// Show CI status if waiting for CI
			if pr.Stage == autopilot.StageWaitingCI {
				ciLine := fmt.Sprintf("     CI: %s", pr.CIStatus)
				content.WriteString(ciLine)
				content.WriteString("\n")
			}

			// Show error if in failed state
			if pr.Stage == autopilot.StageFailed && pr.Error != "" {
				errLine := fmt.Sprintf("     Error: %s", truncateString(pr.Error, 30))
				content.WriteString(errLine)
				content.WriteString("\n")
			}
		}
	}

	// Circuit breaker status
	failures := p.controller.ConsecutiveFailures()
	if failures > 0 {
		content.WriteString("\n")
		failStr := fmt.Sprintf("%d/%d", failures, cfg.MaxFailures)
		content.WriteString(dotLeaderStyled("⚠️ Failures", failStr, warningStyle, w))
	}

	return renderPanel("AUTOPILOT", content.String())
}

// formatDuration formats a duration for display (e.g., "2m", "1h30m").
func (p *AutopilotPanel) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}

// truncateString truncates a string to maxLen, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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
	IssueURL string
	PRURL    string
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

// UpdateInfo contains information about an available update
type UpdateInfo struct {
	CurrentVersion string
	LatestVersion  string
	ReleaseNotes   string
}

// UpgradeState tracks the current upgrade status
type UpgradeState int

const (
	UpgradeStateNone UpgradeState = iota
	UpgradeStateAvailable
	UpgradeStateInProgress
	UpgradeStateComplete
	UpgradeStateFailed
)

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
	version        string
	store          *memory.Store // SQLite persistence (GH-367)
	sessionID      string        // Current session ID for persistence

	// Metrics cards
	metricsCard   MetricsCardData
	sparklineTick bool

	// Upgrade state
	updateInfo      *UpdateInfo
	upgradeState    UpgradeState
	upgradeProgress int
	upgradeMessage  string
	upgradeError    string
	upgradeCh       chan<- struct{} // Channel to trigger upgrade (write-only)
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

// updateAvailableMsg signals that an update is available
type updateAvailableMsg UpdateInfo

// upgradeProgressMsg updates the upgrade progress
type upgradeProgressMsg struct {
	Progress int
	Message  string
}

// upgradeCompleteMsg signals upgrade completion
type upgradeCompleteMsg struct {
	Success bool
	Error   string
}

// NewModel creates a new dashboard model
func NewModel(version string) Model {
	return Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(nil), // Disabled by default
		version:        version,
	}
}

// NewModelWithStore creates a dashboard model with SQLite persistence.
// Hydrates token usage and task history from the store on startup.
func NewModelWithStore(version string, store *memory.Store) Model {
	m := Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(nil),
		version:        version,
		store:          store,
	}
	m.hydrateFromStore()
	return m
}

// NewModelWithAutopilot creates a dashboard model with autopilot integration.
func NewModelWithAutopilot(version string, controller *autopilot.Controller) Model {
	return Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(controller),
		version:        version,
	}
}

// NewModelWithStoreAndAutopilot creates a fully-featured dashboard model.
func NewModelWithStoreAndAutopilot(version string, store *memory.Store, controller *autopilot.Controller) Model {
	m := Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(controller),
		version:        version,
		store:          store,
	}
	m.hydrateFromStore()
	return m
}

// hydrateFromStore loads persisted state from SQLite.
func (m *Model) hydrateFromStore() {
	if m.store == nil {
		return
	}

	// Get or create today's session
	session, err := m.store.GetOrCreateDailySession()
	if err != nil {
		slog.Warn("failed to get/create session", slog.Any("error", err))
	} else {
		m.sessionID = session.ID
		m.tokenUsage = TokenUsage{
			InputTokens:  session.TotalInputTokens,
			OutputTokens: session.TotalOutputTokens,
			TotalTokens:  session.TotalInputTokens + session.TotalOutputTokens,
		}
	}

	// Load recent executions as completed tasks
	executions, err := m.store.GetRecentExecutions(20)
	if err != nil {
		slog.Warn("failed to load recent executions", slog.Any("error", err))
		return
	}

	// Initialize metrics card from session token data
	m.metricsCard.TotalTokens = m.tokenUsage.TotalTokens
	m.metricsCard.InputTokens = m.tokenUsage.InputTokens
	m.metricsCard.OutputTokens = m.tokenUsage.OutputTokens
	m.metricsCard.TotalCostUSD = memory.EstimateCost(
		int64(m.tokenUsage.InputTokens),
		int64(m.tokenUsage.OutputTokens),
		memory.DefaultModel,
	)

	// Count completed/failed tasks from executions and populate history panel
	for i, exec := range executions {
		status := "success"
		if exec.Status == "failed" {
			status = "failed"
			m.metricsCard.Failed++
		} else {
			m.metricsCard.Succeeded++
		}
		m.metricsCard.TotalTasks++

		// Most recent 5 for history panel
		if i < 5 {
			completedAt := exec.CreatedAt
			if exec.CompletedAt != nil {
				completedAt = *exec.CompletedAt
			}
			m.completedTasks = append(m.completedTasks, CompletedTask{
				ID:          exec.TaskID,
				Title:       exec.TaskTitle,
				Status:      status,
				Duration:    fmt.Sprintf("%dms", exec.DurationMs),
				CompletedAt: completedAt,
			})
		}
	}

	// Compute cost per task
	if m.metricsCard.TotalTasks > 0 {
		m.metricsCard.CostPerTask = m.metricsCard.TotalCostUSD / float64(m.metricsCard.TotalTasks)
	}

	// Load sparkline history
	m.loadMetricsHistory()
}

// persistTokenUsage saves token usage to the current session.
func (m *Model) persistTokenUsage(inputDelta, outputDelta int) {
	if m.store == nil || m.sessionID == "" {
		return
	}
	if err := m.store.UpdateSessionTokens(m.sessionID, inputDelta, outputDelta); err != nil {
		slog.Warn("failed to persist token usage", slog.Any("error", err))
	}
}

// loadMetricsHistory queries daily metrics for the past 7 days and populates sparkline history arrays.
func (m *Model) loadMetricsHistory() {
	if m.store == nil {
		return
	}
	now := time.Now()
	query := memory.MetricsQuery{
		Start: now.AddDate(0, 0, -7),
		End:   now,
	}
	dailyMetrics, err := m.store.GetDailyMetrics(query)
	if err != nil {
		slog.Warn("failed to load metrics history", slog.Any("error", err))
		return
	}

	// Build date→metrics map (GetDailyMetrics returns DESC order)
	byDate := make(map[string]*memory.DailyMetrics, len(dailyMetrics))
	for _, dm := range dailyMetrics {
		byDate[dm.Date.Format("2006-01-02")] = dm
	}

	// Fill 7-day arrays oldest→newest (left→right in sparkline)
	m.metricsCard.TokenHistory = make([]int64, 7)
	m.metricsCard.CostHistory = make([]float64, 7)
	m.metricsCard.TaskHistory = make([]int, 7)
	for i := 0; i < 7; i++ {
		day := now.AddDate(0, 0, -6+i).Format("2006-01-02")
		if dm, ok := byDate[day]; ok {
			m.metricsCard.TokenHistory[i] = dm.TotalTokens
			m.metricsCard.CostHistory[i] = dm.TotalCostUSD
			m.metricsCard.TaskHistory[i] = dm.ExecutionCount
		}
	}
}

// NewModelWithOptions creates a dashboard model with all options including upgrade support.
func NewModelWithOptions(version string, store *memory.Store, controller *autopilot.Controller, upgradeCh chan<- struct{}) Model {
	m := Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		completedTasks: []CompletedTask{},
		costPerMToken:  3.0,
		autopilotPanel: NewAutopilotPanel(controller),
		version:        version,
		store:          store,
		upgradeCh:      upgradeCh,
	}
	m.hydrateFromStore()
	return m
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
		case "enter":
			if m.selectedTask >= 0 && m.selectedTask < len(m.tasks) {
				task := m.tasks[m.selectedTask]
				if task.IssueURL != "" {
					_ = openBrowser(task.IssueURL)
				}
			}
		case "u":
			// Trigger upgrade if update is available and not already upgrading
			if m.updateInfo != nil && m.upgradeState == UpgradeStateAvailable && m.upgradeCh != nil {
				m.upgradeState = UpgradeStateInProgress
				m.upgradeProgress = 0
				m.upgradeMessage = "Starting upgrade..."
				// Non-blocking send to upgrade channel
				select {
				case m.upgradeCh <- struct{}{}:
				default:
				}
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		m.sparklineTick = !m.sparklineTick
		return m, tickCmd()

	case updateTasksMsg:
		m.tasks = msg

	case addLogMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 100 {
			m.logs = m.logs[1:]
		}

	case updateTokensMsg:
		// Calculate delta and persist
		inputDelta := msg.InputTokens - m.tokenUsage.InputTokens
		outputDelta := msg.OutputTokens - m.tokenUsage.OutputTokens
		m.tokenUsage = TokenUsage(msg)
		m.persistTokenUsage(inputDelta, outputDelta)

		// Sync metrics card with latest token data
		m.metricsCard.TotalTokens = m.tokenUsage.TotalTokens
		m.metricsCard.InputTokens = m.tokenUsage.InputTokens
		m.metricsCard.OutputTokens = m.tokenUsage.OutputTokens
		m.metricsCard.TotalCostUSD = memory.EstimateCost(
			int64(m.tokenUsage.InputTokens),
			int64(m.tokenUsage.OutputTokens),
			memory.DefaultModel,
		)
		if m.metricsCard.TotalTasks > 0 {
			m.metricsCard.CostPerTask = m.metricsCard.TotalCostUSD / float64(m.metricsCard.TotalTasks)
		}

	case addCompletedTaskMsg:
		m.completedTasks = append(m.completedTasks, CompletedTask(msg))
		if len(m.completedTasks) > 5 {
			m.completedTasks = m.completedTasks[len(m.completedTasks)-5:]
		}

		// Update metrics card task counters
		m.metricsCard.TotalTasks++
		if CompletedTask(msg).Status == "success" {
			m.metricsCard.Succeeded++
		} else {
			m.metricsCard.Failed++
		}
		if m.metricsCard.TotalTasks > 0 {
			m.metricsCard.CostPerTask = m.metricsCard.TotalCostUSD / float64(m.metricsCard.TotalTasks)
		}

	case updateMetricsCardMsg:
		m.metricsCard = MetricsCardData(msg)

	case updateAvailableMsg:
		m.updateInfo = &UpdateInfo{
			CurrentVersion: msg.CurrentVersion,
			LatestVersion:  msg.LatestVersion,
			ReleaseNotes:   msg.ReleaseNotes,
		}
		m.upgradeState = UpgradeStateAvailable

	case upgradeProgressMsg:
		m.upgradeProgress = msg.Progress
		m.upgradeMessage = msg.Message

	case upgradeCompleteMsg:
		if msg.Success {
			m.upgradeState = UpgradeStateComplete
			m.upgradeMessage = "Upgrade complete! Restarting..."
		} else {
			m.upgradeState = UpgradeStateFailed
			m.upgradeError = msg.Error
			m.upgradeMessage = "Upgrade failed"
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

	// Header with ASCII logo
	b.WriteString("\n")                           // Top padding to match bottom spacing
	logo := strings.TrimPrefix(banner.Logo, "\n") // Remove leading newline
	b.WriteString(titleStyle.Render(logo))
	b.WriteString(titleStyle.Render(fmt.Sprintf("   Pilot %s", m.version)))
	b.WriteString("\n")

	// Update notification (if available)
	if m.updateInfo != nil {
		b.WriteString(m.renderUpdateNotification())
	}
	b.WriteString("\n")

	// Metrics cards (tokens, cost, tasks)
	b.WriteString(m.renderMetricsCards())
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
	b.WriteString(helpStyle.Render("q: quit  l: logs  j/k: select  enter: open"))

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

// renderOrangePanel renders a panel with orange borders and title (for update notifications)
func renderOrangePanel(title string, content string) string {
	var lines []string

	// Top border
	lines = append(lines, buildOrangeTopBorder(title))

	// Empty line padding
	lines = append(lines, buildOrangeEmptyLine())

	// Content lines
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, buildOrangeContentLine(line))
	}

	// Empty line padding
	lines = append(lines, buildOrangeEmptyLine())

	// Bottom border
	lines = append(lines, buildOrangeBottomBorder())

	return strings.Join(lines, "\n")
}

// buildOrangeTopBorder creates orange top border: ╭─ TITLE ─────...─────╮
func buildOrangeTopBorder(title string) string {
	titleUpper := strings.ToUpper(title)
	prefix := "╭─ "
	prefixWidth := lipgloss.Width(prefix + titleUpper + " ")

	dashCount := panelTotalWidth - prefixWidth - 1
	if dashCount < 0 {
		dashCount = 0
	}

	return orangeBorderStyle.Render(prefix) + orangeLabelStyle.Render(titleUpper) + orangeBorderStyle.Render(" "+strings.Repeat("─", dashCount)+"╮")
}

// buildOrangeBottomBorder creates orange bottom border: ╰─────────────────────────────────────────────────╯
func buildOrangeBottomBorder() string {
	dashCount := panelTotalWidth - 2
	line := "╰" + strings.Repeat("─", dashCount) + "╯"
	return orangeBorderStyle.Render(line)
}

// buildOrangeEmptyLine creates orange bordered empty line: │                                                                 │
func buildOrangeEmptyLine() string {
	spaceCount := panelTotalWidth - 2
	border := orangeBorderStyle.Render("│")
	return border + strings.Repeat(" ", spaceCount) + border
}

// buildOrangeContentLine creates orange bordered content line: │ (space) content padded/truncated (space) │
func buildOrangeContentLine(content string) string {
	contentWidth := panelTotalWidth - 4
	adjusted := padOrTruncate(content, contentWidth)
	border := orangeBorderStyle.Render("│")
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

// truncateVisual truncates string to targetWidth visual chars, adding "..." only if needed
func truncateVisual(s string, targetWidth int) string {
	visualWidth := lipgloss.Width(s)

	// If string already fits, return as-is (no truncation needed)
	if visualWidth <= targetWidth {
		return s
	}

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

// formatCompact formats a number in compact form: 0, 999, 1.0K, 57.3K, 1.2M.
func formatCompact(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// normalizeToSparkline scales float64 values to 0-8 range for sparkline rendering.
// Left-pads with zeros if fewer values than width. Each returned int maps to a sparkBlocks index.
func normalizeToSparkline(values []float64, width int) []int {
	result := make([]int, width)
	if len(values) == 0 {
		return result
	}

	// Left-pad: place values at the right end
	offset := width - len(values)
	if offset < 0 {
		// More values than width — take the last `width` values
		values = values[len(values)-width:]
		offset = 0
	}

	// Find min/max for scaling
	minVal := values[0]
	maxVal := values[0]
	for _, v := range values[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	span := maxVal - minVal
	if span == 0 {
		// All values identical
		level := 1 // baseline for all-zero
		if values[0] > 0 {
			level = 4 // midpoint for uniform non-zero
		}
		for i := range values {
			result[offset+i] = level
		}
		return result
	}

	for i, v := range values {
		// Scale to 1-8 (reserve 0 for padding, 1 = visible baseline)
		normalized := (v - minVal) / span * 7
		level := int(math.Round(normalized)) + 1
		if v == 0 {
			level = 1 // visible baseline for zero values
		}
		if level < 1 {
			level = 1
		}
		if level > 8 {
			level = 8
		}
		result[offset+i] = level
	}

	return result
}

// renderSparkline maps int levels to sparkBlocks rune chars.
// Appends pulsing indicator (•) when pulsing=true, space otherwise.
// Total visual width equals cardInnerWidth (17 chars).
func renderSparkline(levels []int, pulsing bool) string {
	var b strings.Builder
	// sparkline data chars = cardInnerWidth - 1 (for pulsing indicator)
	dataWidth := cardInnerWidth - 1

	// Render levels (take last dataWidth values, or pad left)
	start := 0
	if len(levels) > dataWidth {
		start = len(levels) - dataWidth
	}

	// Left-pad if needed
	for i := 0; i < dataWidth-len(levels)+start; i++ {
		b.WriteRune(sparkBlocks[0])
	}

	for i := start; i < len(levels); i++ {
		idx := levels[i]
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBlocks) {
			idx = len(sparkBlocks) - 1
		}
		b.WriteRune(sparkBlocks[idx])
	}

	if pulsing {
		b.WriteRune('•')
	} else {
		b.WriteRune(' ')
	}

	return b.String()
}

// --- Mini-card builder helpers ---

// miniCardEmptyLine returns a bordered empty line at exact cardWidth (21 chars).
func miniCardEmptyLine() string {
	border := borderStyle.Render("│")
	return border + strings.Repeat(" ", cardWidth-2) + border
}

// miniCardContentLine returns a bordered content line with 1-char padding each side.
func miniCardContentLine(content string) string {
	adjusted := padOrTruncate(content, cardInnerWidth)
	border := borderStyle.Render("│")
	return border + " " + adjusted + " " + border
}

// miniCardHeaderLine returns a header with TITLE left-aligned and VALUE right-aligned.
func miniCardHeaderLine(title, value string) string {
	styledTitle := titleStyle.Render(strings.ToUpper(title))
	titleWidth := lipgloss.Width(styledTitle)
	valueWidth := lipgloss.Width(value)
	gap := cardInnerWidth - titleWidth - valueWidth
	if gap < 1 {
		gap = 1
	}
	return styledTitle + strings.Repeat(" ", gap) + value
}

// buildMiniCard assembles a full bordered mini-card.
func buildMiniCard(title, value, detail1, detail2, sparkline string) string {
	dashCount := cardWidth - 2
	top := borderStyle.Render("╭" + strings.Repeat("─", dashCount) + "╮")
	bottom := borderStyle.Render("╰" + strings.Repeat("─", dashCount) + "╯")

	lines := []string{
		top,
		miniCardEmptyLine(),
		miniCardContentLine(miniCardHeaderLine(title, value)),
		miniCardEmptyLine(),
		miniCardContentLine(detail1),
		miniCardContentLine(detail2),
		miniCardEmptyLine(),
		miniCardContentLine(sparkline),
		miniCardEmptyLine(),
		bottom,
	}
	return strings.Join(lines, "\n")
}

// --- Card renderers ---

// renderTokenCard renders the TOKENS mini-card.
func (m Model) renderTokenCard() string {
	value := titleStyle.Render(formatCompact(m.metricsCard.TotalTokens))
	detail1 := dimStyle.Render(fmt.Sprintf("↑ %s input", formatCompact(m.metricsCard.InputTokens)))
	detail2 := dimStyle.Render(fmt.Sprintf("↓ %s output", formatCompact(m.metricsCard.OutputTokens)))

	// Convert int64 history to float64
	floats := make([]float64, len(m.metricsCard.TokenHistory))
	for i, v := range m.metricsCard.TokenHistory {
		floats[i] = float64(v)
	}
	levels := normalizeToSparkline(floats, cardInnerWidth-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick))

	return buildMiniCard("tokens", value, detail1, detail2, spark)
}

// renderCostCard renders the COST mini-card.
func (m Model) renderCostCard() string {
	value := costStyle.Render(fmt.Sprintf("$%.2f", m.metricsCard.TotalCostUSD))
	costPerTask := m.metricsCard.CostPerTask
	detail1 := dimStyle.Render(fmt.Sprintf("~$%.2f/task", costPerTask))
	detail2 := ""

	levels := normalizeToSparkline(m.metricsCard.CostHistory, cardInnerWidth-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick))

	return buildMiniCard("cost", value, detail1, detail2, spark)
}

// renderTaskCard renders the TASKS mini-card.
func (m Model) renderTaskCard() string {
	value := fmt.Sprintf("%d", m.metricsCard.TotalTasks)
	detail1 := statusCompletedStyle.Render(fmt.Sprintf("✓ %d succeeded", m.metricsCard.Succeeded))
	detail2 := statusFailedStyle.Render(fmt.Sprintf("✗ %d failed", m.metricsCard.Failed))

	// Convert int history to float64
	floats := make([]float64, len(m.metricsCard.TaskHistory))
	for i, v := range m.metricsCard.TaskHistory {
		floats[i] = float64(v)
	}
	levels := normalizeToSparkline(floats, cardInnerWidth-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick))

	return buildMiniCard("tasks", value, detail1, detail2, spark)
}

// renderMetricsCards renders all three mini-cards side by side.
func (m Model) renderMetricsCards() string {
	gap := strings.Repeat(" ", cardGap)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderTokenCard(), gap, m.renderCostCard(), gap, m.renderTaskCard())
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
		// Layout: "  + GH-156  Title...                                    2m ago"
		// Fixed parts: indent(2) + status(1) + space(1) + id(7) + space(2) + title + space(2) + timeAgo(8)
		// Title gets remaining width: panelInnerWidth - 23
		titleWidth := panelInnerWidth - 23
		if titleWidth < 10 {
			titleWidth = 10
		}

		for i, task := range m.completedTasks {
			if i > 0 {
				content.WriteString("\n")
			}

			// Determine status style
			var statusIcon string
			var style lipgloss.Style
			if task.Status == "success" {
				statusIcon = "+"
				style = statusCompletedStyle
			} else {
				statusIcon = "x"
				style = statusFailedStyle
			}

			// Get plain text values for width calculation
			timeAgoStr := formatTimeAgo(task.CompletedAt)
			titleStr := padOrTruncate(task.Title, titleWidth)

			// Build line with styles applied to specific parts only
			// Don't use %-Ns with styled text - ANSI codes break padding
			content.WriteString(fmt.Sprintf("  %s %-7s  %s  %8s",
				style.Render(statusIcon),
				task.ID,
				titleStr,
				dimStyle.Render(timeAgoStr),
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

// updateMetricsCardMsg updates the metrics card data
type updateMetricsCardMsg MetricsCardData

// UpdateMetricsCard sends updated metrics card data to the TUI
func UpdateMetricsCard(data MetricsCardData) tea.Cmd {
	return func() tea.Msg {
		return updateMetricsCardMsg(data)
	}
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

// renderUpdateNotification renders the update notification panel
func (m Model) renderUpdateNotification() string {
	var content strings.Builder
	var title string
	var hint string

	switch m.upgradeState {
	case UpgradeStateAvailable:
		title = "^ UPDATE"
		// Left: version info, Right: will be hint below panel
		leftText := fmt.Sprintf("%s -> %s available", m.updateInfo.CurrentVersion, m.updateInfo.LatestVersion)
		rightText := ""
		content.WriteString(formatPanelRow(leftText, rightText))
		hint = "u: upgrade"

	case UpgradeStateInProgress:
		title = "* UPGRADING"
		bar := m.renderProgressBar(m.upgradeProgress, 30)
		content.WriteString(fmt.Sprintf("  Installing %s... %s %d%%", m.updateInfo.LatestVersion, bar, m.upgradeProgress))

	case UpgradeStateComplete:
		title = "+ UPGRADED"
		content.WriteString(fmt.Sprintf("  Now running %s - Restarting...", m.updateInfo.LatestVersion))

	case UpgradeStateFailed:
		title = "! UPGRADE FAILED"
		content.WriteString("  " + m.upgradeError)

	default:
		return ""
	}

	result := renderOrangePanel(title, content.String())
	if hint != "" {
		// Right-align hint under panel
		hintLine := fmt.Sprintf("%*s", panelTotalWidth, hint)
		result += "\n" + dimStyle.Render(hintLine)
	}
	return result
}

// formatPanelRow creates a full-width row with left and right aligned text
func formatPanelRow(left, right string) string {
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	padding := panelInnerWidth - leftWidth - rightWidth - 4 // 4 for indent
	if padding < 1 {
		padding = 1
	}
	return fmt.Sprintf("  %s%s%s", left, strings.Repeat(" ", padding), right)
}

// SetUpgradeChannel sets the channel used to trigger upgrades
func (m *Model) SetUpgradeChannel(ch chan<- struct{}) {
	m.upgradeCh = ch
}

// NotifyUpdateAvailable sends an update available message to the TUI
func NotifyUpdateAvailable(current, latest, releaseNotes string) tea.Cmd {
	return func() tea.Msg {
		return updateAvailableMsg{
			CurrentVersion: current,
			LatestVersion:  latest,
			ReleaseNotes:   releaseNotes,
		}
	}
}

// NotifyUpgradeProgress sends an upgrade progress update to the TUI
func NotifyUpgradeProgress(progress int, message string) tea.Cmd {
	return func() tea.Msg {
		return upgradeProgressMsg{
			Progress: progress,
			Message:  message,
		}
	}
}

// NotifyUpgradeComplete sends an upgrade completion message to the TUI
func NotifyUpgradeComplete(success bool, err string) tea.Cmd {
	return func() tea.Msg {
		return upgradeCompleteMsg{
			Success: success,
			Error:   err,
		}
	}
}

// Run starts the TUI with the given version
func Run(version string) error {
	p := tea.NewProgram(
		NewModel(version),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
