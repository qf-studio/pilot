package dashboard

import (
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
)

// Panel width (all panels same width)
const (
	panelTotalWidth = 69 // Total visual width including borders
	panelInnerWidth = 65 // panelTotalWidth - 4 (2 borders + 2 padding spaces)
)

// Metrics card dimensions
const (
	cardWidth      = 23 // 23*3 = 69 = panelTotalWidth (no gaps)
	cardInnerWidth = 17 // cardWidth - 6 (border + 2-char padding each side)
	cardGap        = 0  // no gap — cards fill full panel width
)

// sparkBlocks maps normalized levels (0-8) to Unicode block elements for sparkline rendering.
var sparkBlocks = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// MetricsCardData holds aggregated metrics for the dashboard metrics cards.
type MetricsCardData struct {
	TotalTokens, InputTokens, OutputTokens  int
	TotalCostUSD, CostPerTask               float64
	TotalTasks, Succeeded, Failed, Declined int
	// TASK-358: non-failure terminal outcomes, split out of "failed".
	NoOp, Stalled, RateLimited, Infra, Skipped int
	TokenHistory                               []int64   // 7 days
	CostHistory                                []float64 // 7 days
	TaskHistory                                []int     // 7 days
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

	statusQueuedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8b949e")) // mid gray

	statusDoneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7ec699")) // sage green (same as completed)

	progressBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7eb8da")) // steel blue

	progressEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#3d4450")) // slate

	progressBarDoneStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7ec699")) // sage green for done bars

	progressBarFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#d48a8a")) // dusty rose for failed bars

	shimmerDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3d4450")) // slate

	shimmerMidStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6e7681")) // between slate and mid gray

	shimmerBrightStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8b949e")) // mid gray

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

// autopilotController is the subset of autopilot.Controller used by AutopilotPanel.
// Defined as an interface so tests can inject fakes without a real Controller.
type autopilotController interface {
	GetActivePRs() []*autopilot.PRState
	Config() *autopilot.Config
	GetPRFailures(prNumber int) int
}

// AutopilotPanel displays autopilot status in the dashboard.
type AutopilotPanel struct {
	controller autopilotController
	panelWidth int // dynamic panel width, set before View()
	tick       int // increments per 1s animation tick, drives ◐ rotation
}

// NewAutopilotPanel creates an autopilot panel.
func NewAutopilotPanel(controller *autopilot.Controller) *AutopilotPanel {
	if controller == nil {
		return &AutopilotPanel{controller: nil, panelWidth: panelTotalWidth}
	}
	return &AutopilotPanel{controller: controller, panelWidth: panelTotalWidth}
}

// SetTick updates the animation tick counter (called from the parent model's tickMsg handler).
func (p *AutopilotPanel) SetTick(t int) { p.tick = t }

// View renders the autopilot panel (GH-2620 variant A redesign).
// Uses renderPanel so the card has the same 1-line top/bottom padding as QUEUE/HISTORY/LOGS.
// Idle: 5 lines (border, empty, content, empty, border).
// Active: 6 lines (+ PR identity + pipeline rail).
// Failed: 7 lines (+ error reason on line 3).
func (p *AutopilotPanel) View() string {
	tw := p.panelWidth
	if tw < panelTotalWidth {
		tw = panelTotalWidth
	}
	inner := tw - 4 // content width: tw minus 2 borders and 2 padding spaces

	if p.controller == nil {
		return renderPanel("AUTOPILOT", "  Disabled", tw)
	}

	prs := p.controller.GetActivePRs()
	if len(prs) == 0 {
		return renderPanel("AUTOPILOT", "  "+dimStyle.Render("idle · no active PR"), tw)
	}

	pr := prs[0]

	// Line 1: "  #NNNN  {title}{padding}{age}"
	age := p.formatDuration(time.Since(pr.CreatedAt))
	prefix1 := fmt.Sprintf("  #%d  ", pr.PRNumber)
	prefix1Len := lipgloss.Width(prefix1)
	ageLen := len(age)
	titleMaxLen := inner - prefix1Len - ageLen - 1 // 1 space before age
	if titleMaxLen < 5 {
		titleMaxLen = 5
	}
	title := truncateString(pr.PRTitle, titleMaxLen)
	pad1 := inner - prefix1Len - len(title) - ageLen
	if pad1 < 1 {
		pad1 = 1
	}
	line1 := prefix1 + title + strings.Repeat(" ", pad1) + age

	// Line 2: "  {rail}{padding}{N/M[ ⟲]}"
	cfg := p.controller.Config()
	maxFailures := cfg.MaxFailures
	if maxFailures <= 0 {
		maxFailures = 5
	}
	failures := p.controller.GetPRFailures(pr.PRNumber)

	rail := renderAutopilotRail(pr.Stage, pr.CIStatus, p.tick)

	retryNum := fmt.Sprintf("%d/%d", failures, maxFailures)
	var retryStr string
	if failures > 0 {
		retryStr = dimStyle.Render(retryNum) + " " + warningStyle.Render("⟲")
	} else {
		retryStr = dimStyle.Render(retryNum) + " "
	}

	pad2 := inner - 2 - lipgloss.Width(rail) - lipgloss.Width(retryStr)
	if pad2 < 1 {
		pad2 = 1
	}
	line2 := "  " + rail + strings.Repeat(" ", pad2) + retryStr

	lines := []string{line1, line2}

	// Line 3 (conditional): "  ↳ {truncated error}" — only on failure with message
	if pr.Stage == autopilot.StageFailed && pr.Error != "" {
		const errPrefix = "  ↳ "
		errMax := inner - len(errPrefix)
		if errMax < 5 {
			errMax = 5
		}
		lines = append(lines, errPrefix+truncateString(pr.Error, errMax))
	}

	// Overflow: additional active PRs
	if len(prs) > 1 {
		lines = append(lines, fmt.Sprintf("  + %d more PR(s)", len(prs)-1))
	}

	return renderPanel("AUTOPILOT", strings.Join(lines, "\n"), tw)
}

// pipelineStagePosition maps a PRStage to its 0-based position in the 5-node rail.
func pipelineStagePosition(stage autopilot.PRStage) int {
	switch stage {
	case autopilot.StagePRCreated, autopilot.StageWaitingCI,
		autopilot.StageCIPassed, autopilot.StageCIFailed:
		return 0
	case autopilot.StageAwaitApproval, autopilot.StageReviewRequested:
		return 1
	case autopilot.StageMerging, autopilot.StageMerged:
		return 2
	case autopilot.StagePostMergeCI:
		return 3
	case autopilot.StageReleasing:
		return 4
	case autopilot.StageFailed:
		return 0
	}
	return 0
}

// renderAutopilotRail renders the 5-node pipeline rail with glyph-per-node status.
// Glyphs: ✓ done (sage), ◐◓◑◒ in-progress animated (steel blue), ○ pending (gray), ✗ failed (rose).
// tick drives the spinner rotation; ciStatus allows the ci node to show ✗ independently of stage.
// Format example for stage=releasing:
//
//	✓ ci ── ✓ rebase ── ✓ merge ── ✓ tag ── ◐ release
func renderAutopilotRail(stage autopilot.PRStage, ciStatus autopilot.CIStatus, tick int) string {
	nodes := []string{"ci", "rebase", "merge", "tag", "release"}
	spinner := []rune{'◐', '◓', '◑', '◒'}
	pos := pipelineStagePosition(stage)

	var sb strings.Builder
	for i, name := range nodes {
		var glyph string
		var glyphStyle, nameStyle lipgloss.Style

		switch {
		case i == 0 && ciStatus == autopilot.CIFailure:
			// CI check failed — show ✗ on the ci node regardless of current stage
			glyph = "✗"
			glyphStyle, nameStyle = statusFailedStyle, statusFailedStyle
		case stage == autopilot.StageFailed && i == pos:
			// Pipeline failed at current position — show ✗
			glyph = "✗"
			glyphStyle, nameStyle = statusFailedStyle, statusFailedStyle
		case i < pos:
			glyph = "✓"
			glyphStyle, nameStyle = statusCompletedStyle, statusCompletedStyle
		case i == pos:
			glyph = string(spinner[tick%4])
			glyphStyle, nameStyle = statusRunningStyle, titleStyle
		default:
			glyph = "○"
			glyphStyle, nameStyle = dimStyle, dimStyle
		}

		sb.WriteString(glyphStyle.Render(glyph))
		sb.WriteString(" ")
		sb.WriteString(nameStyle.Render(name))
		if i < len(nodes)-1 {
			sb.WriteString(dimStyle.Render(" ── "))
		}
	}
	return sb.String()
}

// formatDurationShort formats a duration compactly (e.g., "2m", "1h30m").
func formatDurationShort(d time.Duration) string {
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

// formatDuration wraps formatDurationShort for AutopilotPanel methods.
func (p *AutopilotPanel) formatDuration(d time.Duration) string {
	return formatDurationShort(d)
}

// truncateString truncates a string to maxLen, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// TaskDisplay represents a task for display
type TaskDisplay struct {
	ID          string
	Title       string
	Status      string
	Phase       string
	Progress    int
	Duration    string
	IssueURL    string
	PRURL       string
	ProjectPath string // Resolved project directory (GH-2167)
	ProjectName string // Short project name for git graph title (GH-2167)
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Model        string // model that produced these tokens; empty until first stream event
}

// CompletedTask represents a finished task for history
type CompletedTask struct {
	ID          string
	Title       string
	Status      string // "success" or "failed"
	Duration    string
	CompletedAt time.Time
	ParentID    string   // Parent issue ID for sub-issues (e.g. "GH-498")
	SubIssues   []string // Sub-issue IDs for epics (e.g. ["GH-501", "GH-502"])
	TotalSubs   int      // Total number of sub-issues (epic tracking)
	DoneSubs    int      // Number of completed sub-issues (epic tracking)
	IsEpic      bool     // Whether this task was decomposed into sub-issues
	// PeakRSSMB is the peak subprocess RSS in MiB from the RSS sampler. GH-3028.
	// Zero when the sampler had no data (pre-3028 executions, non-Linux/darwin).
	PeakRSSMB int
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
	shimmerTick   int // Counter for queue shimmer animation (increments each tick)

	// Upgrade state
	updateInfo      *UpdateInfo
	upgradeState    UpgradeState
	upgradeProgress int
	upgradeMessage  string
	upgradeError    string
	upgradeCh       chan<- struct{} // Channel to trigger upgrade (write-only)

	// Banner toggle (GH-1520)
	showBanner bool

	// Banner metadata (GH-2455 / GH-2459 rework): env name, model stack, adapter
	// status list.
	startTime      time.Time
	modelStack     string
	envName        string
	bannerAdapters []AdapterStatus
	// activeAdapters retained for backwards compatibility with SetBannerMeta callers.
	activeAdapters []string

	// Splash state — shown for the first ~1.5s of the session inside the same
	// tea.Program (avoids alt-screen flicker that a separate splash program caused).
	splashActive bool
	splashFrame  int       // increments each splashTickMsg
	splashStart  time.Time // first frame timestamp
	configPath   string    // shown in splash boot block ("~/.pilot/config.yaml")

	// Git graph panel (GH-1506)
	gitGraphMode       GitGraphMode
	gitGraphState      *GitGraphState
	gitGraphScroll     int
	gitGraphFocus      bool
	dbSyncTick         int    // Counter for periodic DB re-sync (GH-2248)
	projectPath        string // Working directory for git commands
	defaultProjectPath string // Fallback project path from config (GH-2167)
	metricsScopePath   string // Project path used to filter store/metrics queries (GH-3531)
	gitProjectName     string // Current project name shown in git panel title (GH-2167)
}

// isStackedMode returns true when the git graph is visible and the terminal is
// too narrow for side-by-side layout, so the graph stacks below the dashboard.
func (m Model) isStackedMode() bool {
	if m.gitGraphMode == GitGraphHidden || m.width <= 0 {
		return false
	}
	// Minimum for side-by-side: dashboard + gap + smallest useful graph (20)
	return m.width < panelTotalWidth+1+20
}

// effectivePanelTotalWidth returns the panel width for the current layout.
// In stacked mode with a wider terminal, panels stretch to fill terminal width.
func (m Model) effectivePanelTotalWidth() int {
	if m.isStackedMode() && m.width > panelTotalWidth {
		return m.width
	}
	return panelTotalWidth
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

// storeRefreshMsg carries refreshed state from SQLite (GH-2248).
type storeRefreshMsg struct {
	completedTasks []CompletedTask
	metricsCard    MetricsCardData
}

// NewModel creates a new dashboard model
func NewModel(version string) Model {
	return Model{
		tasks:          []TaskDisplay{},
		logs:           []string{},
		showLogs:       true,
		showBanner:     true,
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
		showBanner:     true,
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
		showBanner:     true,
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
		showBanner:     true,
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
	executions, err := m.store.GetRecentExecutions(20, m.metricsScopePath)
	if err != nil {
		slog.Warn("failed to load recent executions", slog.Any("error", err))
		return
	}

	// Initialize metrics card from lifetime execution data (survives restarts).
	// Session tokens only track the current process; executions table has the real totals.
	lifetime, err := m.store.GetLifetimeTokens(m.metricsScopePath)
	if err != nil {
		slog.Warn("failed to load lifetime tokens", slog.Any("error", err))
	} else {
		m.metricsCard.TotalTokens = int(lifetime.TotalTokens)
		m.metricsCard.InputTokens = int(lifetime.InputTokens)
		m.metricsCard.OutputTokens = int(lifetime.OutputTokens)
		m.metricsCard.TotalCostUSD = lifetime.TotalCostUSD
	}

	// Initialize task counts from lifetime data (survives restarts).
	// Previous code sampled from GetRecentExecutions(20), showing only last 20 results.
	taskCounts, err := m.store.GetLifetimeTaskCounts(m.metricsScopePath)
	if err != nil {
		slog.Warn("failed to load lifetime task counts", slog.Any("error", err))
	} else {
		m.metricsCard.TotalTasks = taskCounts.Total
		m.metricsCard.Succeeded = taskCounts.Succeeded
		m.metricsCard.Failed = taskCounts.Failed
		m.metricsCard.Declined = taskCounts.Declined
		m.metricsCard.NoOp = taskCounts.NoOp
		m.metricsCard.Stalled = taskCounts.Stalled
		m.metricsCard.RateLimited = taskCounts.RateLimited
		m.metricsCard.Infra = taskCounts.Infra
		m.metricsCard.Skipped = taskCounts.Skipped
	}

	// Populate history panel from recent executions (most recent 5)
	for i, exec := range executions {
		if i >= 5 {
			break
		}
		status := "success"
		if exec.Status == "failed" {
			status = "failed"
		}
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
			PeakRSSMB:   exec.PeakRSSMB,
		})
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
	if m.metricsScopePath != "" {
		query.Projects = []string{m.metricsScopePath}
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
		showBanner:     true,
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

// SetProjectPath sets the working directory used for git graph commands.
// The first call also sets the default fallback path (GH-2167) and seeds
// metricsScopePath so callers that only call SetProjectPath retain their
// current behavior (metrics scoped to the same project).
func (m *Model) SetProjectPath(path string) {
	m.projectPath = path
	if m.defaultProjectPath == "" {
		m.defaultProjectPath = path
	}
	if m.metricsScopePath == "" {
		m.metricsScopePath = path
	}
}

// SetMetricsScopePath sets the project path used to filter store queries
// (recent executions, lifetime tokens, task counts, sparklines, eval panel).
// Defaults to the value passed to SetProjectPath when not set explicitly.
func (m *Model) SetMetricsScopePath(path string) {
	m.metricsScopePath = path
}

// RenderBannerForTest exposes renderBanner for cross-package tests
// (cmd/pilot verifies applyDashboardBannerMeta wiring end-to-end).
func (m Model) RenderBannerForTest() string { return m.renderBanner() }

// AdapterStatus describes a configured adapter for the banner status row.
// Active=true when the adapter was started this session (flag passed); false
// when it is configured but not running. Adapters absent from the slice are
// not rendered at all (not configured).
type AdapterStatus struct {
	Name   string
	Active bool
}

// SetBannerMeta configures optional metadata shown in the dashboard banner (GH-2455).
// Adapters provided here are all rendered as Active=true (legacy contract).
// New callers should use SetBannerAdapters for richer state (active vs configured).
func (m *Model) SetBannerMeta(envName, modelStack string, adapters []string, startTime time.Time) {
	m.envName = envName
	m.modelStack = modelStack
	m.activeAdapters = adapters
	// Mirror into bannerAdapters with Active=true so renderBanner has a single source.
	m.bannerAdapters = make([]AdapterStatus, 0, len(adapters))
	for _, a := range adapters {
		m.bannerAdapters = append(m.bannerAdapters, AdapterStatus{Name: a, Active: true})
	}
	if startTime.IsZero() {
		m.startTime = time.Now()
	} else {
		m.startTime = startTime
	}
}

// EnableSplash turns on the in-program splash overlay shown for the first
// ~1.5s after the dashboard starts. configPath is displayed in the boot
// block (e.g. "~/.pilot/config.yaml").
func (m *Model) EnableSplash(configPath string) {
	m.splashActive = true
	m.configPath = configPath
}

// SetBannerAdapters replaces the adapter status list shown in the banner.
// Pass an entry with Active=false for adapters that are configured but not
// running this session; omit entries entirely for adapters with no config.
func (m *Model) SetBannerAdapters(adapters []AdapterStatus) {
	m.bannerAdapters = adapters
	// Mirror Active-true names into legacy field for any consumers still reading it.
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		if a.Active {
			names = append(names, a.Name)
		}
	}
	m.activeAdapters = names
}

// syncGitGraphToSelectedTask updates projectPath to match the selected task's project.
// Returns a tea.Cmd to refresh the git graph if the project changed, nil otherwise.
// Falls back to defaultProjectPath when no task is selected or the task has no project. (GH-2167)
func (m *Model) syncGitGraphToSelectedTask() tea.Cmd {
	if m.gitGraphMode == GitGraphHidden {
		return nil
	}

	newPath := m.defaultProjectPath
	newName := ""

	if m.selectedTask >= 0 && m.selectedTask < len(m.tasks) {
		task := m.tasks[m.selectedTask]
		if task.ProjectPath != "" {
			newPath = task.ProjectPath
			newName = task.ProjectName
			if newName == "" {
				newName = filepath.Base(newPath)
			}
		}
	}

	if newPath == m.projectPath {
		// Project unchanged — just update display name if needed
		m.gitProjectName = newName
		return nil
	}

	m.projectPath = newPath
	m.gitProjectName = newName
	m.gitGraphScroll = 0
	return refreshGitGraphCmd(m.projectPath)
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), tea.EnterAltScreen}
	if m.splashActive {
		cmds = append(cmds, splashTickCmd())
	}
	return tea.Batch(cmds...)
}

// tickCmd creates a tick command
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// splashTickMsg drives the boot-screen lamp animation.
type splashTickMsg time.Time

// splashTickCmd schedules the next splash frame (~150ms cadence).
func splashTickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return splashTickMsg(t)
	})
}

// splashFramesTotal: number of frames the splash plays before dismissal.
// 10 frames * 150ms = 1.5s.
const splashFramesTotal = 10

// storeRefreshCmd queries SQLite for current execution state (GH-2248).
// Runs asynchronously so the TUI never blocks on DB I/O.
func storeRefreshCmd(store *memory.Store, projectPath string) tea.Cmd {
	return func() tea.Msg {
		msg := storeRefreshMsg{}

		executions, err := store.GetRecentExecutions(20, projectPath)
		if err != nil {
			slog.Warn("store refresh: failed to load executions", slog.Any("error", err))
			return msg
		}
		for i, exec := range executions {
			if i >= 5 {
				break
			}
			status := "success"
			if exec.Status == "failed" {
				status = "failed"
			}
			completedAt := exec.CreatedAt
			if exec.CompletedAt != nil {
				completedAt = *exec.CompletedAt
			}
			msg.completedTasks = append(msg.completedTasks, CompletedTask{
				ID:          exec.TaskID,
				Title:       exec.TaskTitle,
				Status:      status,
				Duration:    fmt.Sprintf("%dms", exec.DurationMs),
				CompletedAt: completedAt,
				PeakRSSMB:   exec.PeakRSSMB,
			})
		}

		lifetime, err := store.GetLifetimeTokens(projectPath)
		if err != nil {
			slog.Warn("store refresh: failed to load lifetime tokens", slog.Any("error", err))
		} else {
			msg.metricsCard.TotalTokens = int(lifetime.TotalTokens)
			msg.metricsCard.InputTokens = int(lifetime.InputTokens)
			msg.metricsCard.OutputTokens = int(lifetime.OutputTokens)
			msg.metricsCard.TotalCostUSD = lifetime.TotalCostUSD
		}

		taskCounts, err := store.GetLifetimeTaskCounts(projectPath)
		if err != nil {
			slog.Warn("store refresh: failed to load task counts", slog.Any("error", err))
		} else {
			msg.metricsCard.TotalTasks = taskCounts.Total
			msg.metricsCard.Succeeded = taskCounts.Succeeded
			msg.metricsCard.Failed = taskCounts.Failed
			msg.metricsCard.Declined = taskCounts.Declined
			msg.metricsCard.NoOp = taskCounts.NoOp
			msg.metricsCard.Stalled = taskCounts.Stalled
			msg.metricsCard.RateLimited = taskCounts.RateLimited
			msg.metricsCard.Infra = taskCounts.Infra
			msg.metricsCard.Skipped = taskCounts.Skipped
		}

		if msg.metricsCard.TotalTasks > 0 {
			msg.metricsCard.CostPerTask = msg.metricsCard.TotalCostUSD / float64(msg.metricsCard.TotalTasks)
		}

		return msg
	}
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "b":
			m.showBanner = !m.showBanner
			return m, tea.ClearScreen
		case "l":
			m.showLogs = !m.showLogs
			return m, tea.ClearScreen // GH-1249: Logs toggle changes height
		case "g":
			// Toggle git graph: Hidden ↔ Visible (auto-sizes)
			if m.gitGraphMode == GitGraphHidden {
				m.gitGraphMode = GitGraphVisible
			} else {
				m.gitGraphMode = GitGraphHidden
			}
			m.gitGraphFocus = false
			if m.gitGraphMode != GitGraphHidden {
				// Start refresh and 15s tick when becoming visible
				return m, tea.Batch(
					refreshGitGraphCmd(m.projectPath),
					gitRefreshTickCmd(),
					tea.ClearScreen,
				)
			}
			return m, tea.ClearScreen
		case "tab":
			if m.gitGraphMode != GitGraphHidden {
				m.gitGraphFocus = !m.gitGraphFocus
			}
		case "up", "k":
			if m.gitGraphFocus {
				if m.gitGraphScroll > 0 {
					m.gitGraphScroll--
				}
			} else if m.selectedTask > 0 {
				m.selectedTask--
				if cmd := m.syncGitGraphToSelectedTask(); cmd != nil {
					return m, cmd
				}
			}
		case "down", "j":
			if m.gitGraphFocus {
				if m.gitGraphState != nil {
					viewportH := m.gitGraphViewportHeight()
					maxScroll := len(m.gitGraphState.Lines) - viewportH
					if maxScroll < 0 {
						maxScroll = 0
					}
					if m.gitGraphScroll < maxScroll {
						m.gitGraphScroll++
					}
				}
			} else if m.selectedTask < len(m.tasks)-1 {
				m.selectedTask++
				if cmd := m.syncGitGraphToSelectedTask(); cmd != nil {
					return m, cmd
				}
			}
		case "ctrl+d":
			if m.gitGraphFocus && m.gitGraphState != nil {
				viewportH := m.gitGraphViewportHeight()
				m.gitGraphScroll += viewportH / 2
				maxScroll := len(m.gitGraphState.Lines) - viewportH
				if maxScroll < 0 {
					maxScroll = 0
				}
				if m.gitGraphScroll > maxScroll {
					m.gitGraphScroll = maxScroll
				}
			}
		case "ctrl+u":
			if m.gitGraphFocus {
				viewportH := m.gitGraphViewportHeight()
				m.gitGraphScroll -= viewportH / 2
				if m.gitGraphScroll < 0 {
					m.gitGraphScroll = 0
				}
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
		return m, tea.ClearScreen // GH-1249: Terminal resized → full repaint

	case tickMsg:
		m.sparklineTick = !m.sparklineTick
		m.shimmerTick++
		m.dbSyncTick++
		if m.autopilotPanel != nil {
			m.autopilotPanel.SetTick(m.autopilotPanel.tick + 1)
		}
		// GH-2248: Re-sync history and metrics from SQLite every 5 seconds
		// so external DB changes (orphan cleanup, manual edits) are reflected.
		if m.store != nil && m.dbSyncTick%5 == 0 {
			return m, tea.Batch(tickCmd(), storeRefreshCmd(m.store, m.metricsScopePath))
		}
		return m, tickCmd()

	case splashTickMsg:
		if !m.splashActive {
			return m, nil
		}
		if m.splashStart.IsZero() {
			m.splashStart = time.Time(msg)
		}
		m.splashFrame++
		if m.splashFrame >= splashFramesTotal {
			m.splashActive = false
			return m, tea.ClearScreen
		}
		return m, splashTickCmd()

	case updateTasksMsg:
		prevLen := len(m.tasks)
		m.tasks = msg
		// GH-2167: Sync git graph to selected task's project when task list updates
		gitCmd := m.syncGitGraphToSelectedTask()
		if len(m.tasks) != prevLen {
			// GH-1249: Task count changed → content height changed.
			// Force full repaint to prevent ghost lines from Bubbletea's diff renderer.
			if gitCmd != nil {
				return m, tea.Batch(gitCmd, tea.ClearScreen)
			}
			return m, tea.ClearScreen
		}
		if gitCmd != nil {
			return m, gitCmd
		}

	case addLogMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 100 {
			m.logs = m.logs[1:]
		}

	case updateTokensMsg:
		// Calculate delta and persist to session
		inputDelta := msg.InputTokens - m.tokenUsage.InputTokens
		outputDelta := msg.OutputTokens - m.tokenUsage.OutputTokens
		m.tokenUsage = TokenUsage(msg)
		m.persistTokenUsage(inputDelta, outputDelta)

		// Add deltas to lifetime metrics card totals (not replace with session values)
		m.metricsCard.InputTokens += inputDelta
		m.metricsCard.OutputTokens += outputDelta
		m.metricsCard.TotalTokens += inputDelta + outputDelta
		costModel := msg.Model
		if costModel == "" {
			costModel = memory.DefaultModel
		}
		m.metricsCard.TotalCostUSD += memory.EstimateCost(
			int64(inputDelta),
			int64(outputDelta),
			costModel,
		)
		if m.metricsCard.TotalTasks > 0 {
			m.metricsCard.CostPerTask = m.metricsCard.TotalCostUSD / float64(m.metricsCard.TotalTasks)
		}

	case addCompletedTaskMsg:
		prevLen := len(m.completedTasks)
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

		// GH-1249: History count changed → force repaint
		if len(m.completedTasks) != prevLen {
			return m, tea.ClearScreen
		}

	case updateMetricsCardMsg:
		m.metricsCard = MetricsCardData(msg)

	case storeRefreshMsg:
		// GH-2248: Replace in-memory history and metrics with live DB state.
		prevLen := len(m.completedTasks)
		m.completedTasks = msg.completedTasks
		m.metricsCard = msg.metricsCard
		m.loadMetricsHistory()
		if len(m.completedTasks) != prevLen {
			return m, tea.ClearScreen
		}

	case updateAvailableMsg:
		m.updateInfo = &UpdateInfo{
			CurrentVersion: msg.CurrentVersion,
			LatestVersion:  msg.LatestVersion,
			ReleaseNotes:   msg.ReleaseNotes,
		}
		m.upgradeState = UpgradeStateAvailable
		return m, tea.ClearScreen // GH-1249: New panel added

	case upgradeProgressMsg:
		m.upgradeProgress = msg.Progress
		m.upgradeMessage = msg.Message

	case upgradeCompleteMsg:
		if msg.Success {
			m.upgradeState = UpgradeStateComplete
			m.upgradeMessage = "Upgrade complete! Restart Pilot to apply."
		} else {
			m.upgradeState = UpgradeStateFailed
			m.upgradeError = msg.Error
			m.upgradeMessage = "Upgrade failed"
		}

	case gitRefreshMsg:
		m.gitGraphState = msg.state
		// Re-arm the 15-second refresh tick if panel is still visible
		if m.gitGraphMode != GitGraphHidden {
			return m, gitRefreshTickCmd()
		}

	case gitRefreshTickMsg:
		// Only refresh when visible to save resources
		if m.gitGraphMode != GitGraphHidden {
			return m, refreshGitGraphCmd(m.projectPath)
		}
	}

	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	if m.quitting {
		return "Pilot stopped.\n"
	}

	if m.splashActive {
		return m.renderSplash()
	}

	dashboard := m.renderDashboard()

	var result string
	if m.gitGraphMode == GitGraphHidden {
		result = dashboard
	} else if m.width > 0 && m.width < panelTotalWidth+1+20 {
		// Terminal too narrow for side-by-side — stack graph below at full terminal width.
		dashLines := strings.Count(dashboard, "\n") + 1
		graphHeight := m.height - dashLines - 1 // -1 for help footer
		if graphHeight < 8 {
			graphHeight = 8 // minimum useful graph height
		}
		graphPanel := m.renderGitGraph(m.width, graphHeight)
		if graphPanel == "" {
			result = dashboard
		} else {
			result = dashboard + "\n" + graphPanel
		}
	} else {
		graphPanel := m.renderGitGraph()
		if graphPanel == "" {
			result = dashboard
		} else {
			result = lipgloss.JoinHorizontal(lipgloss.Top, dashboard, " ", graphPanel)
		}
	}

	// Help footer — appended after height truncation so it's never cut off.
	helpLine := m.renderHelp()

	// GH-1249: Pad or truncate output to terminal height to prevent ghost lines.
	// Reserve the last line for the help footer so it's always visible.
	if m.height > 1 {
		contentHeight := m.height - 1 // reserve 1 line for help footer
		lines := strings.Split(result, "\n")
		if len(lines) < contentHeight {
			for len(lines) < contentHeight {
				lines = append(lines, "")
			}
		} else if len(lines) > contentHeight {
			lines = lines[:contentHeight]
		}
		lines = append(lines, helpLine)
		result = strings.Join(lines, "\n")
	} else if m.height == 1 {
		result = helpLine
	} else {
		// height unknown — just append help
		result += "\n" + helpLine
	}

	return result
}

// renderBanner returns the avionics banner: 3 content rows wrapped with inner
// padding rows for breathing space.
//
//	╭─ PILOT ──────────────────────────────────────────────────────────╮
//	│                                                                   │
//	│ 1636/  PILOT v2.103.0      ENV STAGE      MODEL OPUS-4-7 / SONNET │
//	│                                                                   │
//	│ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ │
//	│                                                                   │
//	│ DAEMON ●  GH ●  TG ●  SLACK ○  DISCORD ○      UP 4m 12s  16:36 UTC│
//	│                                                                   │
//	╰───────────────────────────────────────────────────────────────────╯
//
// Adapter dots: ● filled (statusRunningStyle) when active this session,
// ○ empty (dimStyle) when configured but not flagged. Adapters with no
// config are not present in m.bannerAdapters and don't render at all.
// pilotLogo is the ASCII art shown during splash boot.
const pilotLogo = `
   ██████╗ ██╗██╗      ██████╗ ████████╗
   ██╔══██╗██║██║     ██╔═══██╗╚══██╔══╝
   ██████╔╝██║██║     ██║   ██║   ██║
   ██╔═══╝ ██║██║     ██║   ██║   ██║
   ██║     ██║███████╗╚██████╔╝   ██║
   ╚═╝     ╚═╝╚══════╝ ╚═════╝    ╚═╝
`

// renderSplash returns the boot screen shown for the first ~1.5s of the
// dashboard session. Lamps progressively light up as splashFrame advances;
// the final frames flash READY before the splash dismisses.
func (m Model) renderSplash() string {
	var sb strings.Builder

	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render(strings.TrimPrefix(pilotLogo, "\n")))
	sb.WriteString("\n")

	ver := m.version
	if idx := strings.Index(ver, "-"); idx > 0 {
		ver = ver[:idx]
	}
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	tagline := dimStyle.Render("AI THAT SHIPS YOUR TICKETS")
	verStyled := labelStyle.Render(ver)
	// 47-char wide row to roughly match the logo block.
	gap := 47 - lipgloss.Width(tagline) - lipgloss.Width(verStyled)
	if gap < 1 {
		gap = 1
	}
	sb.WriteString("   " + tagline + strings.Repeat(" ", gap) + verStyled + "\n\n")

	// BOOT block: 4 lamp lines, lit one at a time as splashFrame progresses.
	const ruleWidth = 49
	rule := dimStyle.Render(strings.Repeat("─", ruleWidth))
	sb.WriteString("   " + dimStyle.Render("BOOT ") + dimStyle.Render(strings.Repeat("─", ruleWidth-5)) + "\n")

	cfgPath := m.configPath
	if cfgPath == "" {
		cfgPath = "~/.pilot/config.yaml"
	}
	adapterList := splashAdapterList(m.bannerAdapters)
	if adapterList == "" {
		adapterList = dimStyle.Render("(none configured)")
	}
	model := m.modelStack
	if model == "" {
		model = dimStyle.Render("(unset)")
	}
	envName := strings.ToUpper(m.envName)
	if envName == "" {
		envName = dimStyle.Render("(default)")
	}

	lamps := []struct {
		label, value string
	}{
		{"config loaded", cfgPath},
		{"adapters online", adapterList},
		{"model stack", model},
		{"env", envName},
	}
	// Threshold = ceil(splashFramesTotal/2) so all 4 lamps light by mid-splash.
	litCount := m.splashFrame * len(lamps) / (splashFramesTotal / 2)
	if litCount > len(lamps) {
		litCount = len(lamps)
	}
	for i, l := range lamps {
		var dot string
		if i < litCount {
			dot = statusRunningStyle.Render("●")
		} else {
			dot = dimStyle.Render("○")
		}
		sb.WriteString(fmt.Sprintf("   %s %s    %s\n",
			dot, dimStyle.Render(padTo(l.label, 16)), l.value))
	}
	sb.WriteString("   " + rule + "\n")

	// READY footer: appears in the last 3 frames.
	if m.splashFrame >= splashFramesTotal-3 {
		sb.WriteString(strings.Repeat(" ", 45) + statusCompletedStyle.Render("READY") + "\n")
	} else {
		sb.WriteString("\n")
	}

	return sb.String()
}

// splashAdapterList renders "github · telegram · slack" from the banner's
// adapter list (configured adapters only, lowercased).
func splashAdapterList(adapters []AdapterStatus) string {
	if len(adapters) == 0 {
		return ""
	}
	parts := make([]string, 0, len(adapters))
	for _, a := range adapters {
		parts = append(parts, strings.ToLower(a.Name))
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

func (m Model) renderBanner() string {
	tw := m.effectivePanelTotalWidth()
	// Match the 3-space inner padding other panels use (e.g. AUTOPILOT, QUEUE).
	// buildContentLine reserves "│ " on each side (2 chars), so the content
	// receives tw - 4 cells. Reserving 2 more inside gives a 3-space gutter.
	const innerGutter = 2
	w := tw - 4 - innerGutter*2 // usable content width inside the gutter

	// --- Line 1: code/  PILOT vX.Y.Z   ENV xxx   MODEL plan / exec
	// Strip dev suffix (e.g. "-4-g14764db1-dirty") so the banner shows the
	// clean release version. The full SHA still appears in the session code
	// prefix, so no information is lost.
	ver := m.version
	if idx := strings.Index(ver, "-"); idx > 0 {
		ver = ver[:idx]
	}
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}

	leftPart := labelStyle.Render(ver)

	envSeg := ""
	if m.envName != "" {
		envSeg = dimStyle.Render("ENV") + " " + statusRunningStyle.Render(strings.ToUpper(m.envName))
	}

	modelSeg := ""
	if m.modelStack != "" {
		modelSeg = dimStyle.Render("MODEL") + " " + labelStyle.Render(m.modelStack)
	}

	line1 := joinSegmentsSpaced(w, leftPart, envSeg, modelSeg)

	// --- Line 2: tick separator
	line2 := buildTickSeparator(w)

	// --- Line 3: adapter chips (left), uptime + clock (right)
	upStr := ""
	if !m.startTime.IsZero() {
		upStr = dimStyle.Render("UP") + " " + labelStyle.Render(formatDurationShort(time.Since(m.startTime)))
	}
	clockStr := dimStyle.Render(time.Now().UTC().Format("15:04") + " UTC")
	rightPart := clockStr
	if upStr != "" {
		rightPart = upStr + "  " + clockStr
	}

	// Available room for chips = inner width minus right side and a small gap.
	chipsBudget := w - lipgloss.Width(rightPart) - 2

	chipsStr := buildAdapterChipsRow(m.bannerAdapters, chipsBudget)

	line3 := padLeftRightLine(w, chipsStr, rightPart)

	// Wrap each line with the inner gutter so the banner aligns with sibling
	// panels (which start their content 3 chars in from the border).
	gutter := strings.Repeat(" ", innerGutter)
	wrap := func(s string) string { return gutter + s + gutter }

	pad := buildEmptyLine(tw)
	var lines []string
	lines = append(lines, buildTopBorder("PILOT", tw))
	lines = append(lines, pad)
	lines = append(lines, buildContentLine(wrap(line1), tw))
	lines = append(lines, pad)
	lines = append(lines, buildContentLine(wrap(line2), tw))
	lines = append(lines, pad)
	lines = append(lines, buildContentLine(wrap(line3), tw))
	lines = append(lines, pad)
	lines = append(lines, buildBottomBorder(tw))
	return strings.Join(lines, "\n")
}

// buildAdapterChipsRow packs DAEMON + per-adapter chips into a row that fits
// within `budget` visual chars. Strategy:
//  1. Always include "DAEMON ●".
//  2. Add active adapters first (●, full name).
//  3. Add inactive adapters next (○, full name) until budget is reached.
//  4. If inactive adapters remain that didn't fit, append "+N idle" summary.
func buildAdapterChipsRow(adapters []AdapterStatus, budget int) string {
	const sep = "  "

	type chipEntry struct {
		render   string
		inactive bool
	}

	daemon := chipEntry{render: dimStyle.Render("DAEMON") + " " + statusRunningStyle.Render("●")}
	chips := []chipEntry{daemon}

	for _, a := range adapters {
		if !a.Active {
			continue
		}
		chips = append(chips, chipEntry{
			render: dimStyle.Render(strings.ToUpper(a.Name)) + " " + statusRunningStyle.Render("●"),
		})
	}
	for _, a := range adapters {
		if a.Active {
			continue
		}
		chips = append(chips, chipEntry{
			render:   dimStyle.Render(strings.ToUpper(a.Name)) + " " + dimStyle.Render("○"),
			inactive: true,
		})
	}

	// First pass: include chips greedily until budget runs out.
	included := []chipEntry{}
	used := 0
	for i, c := range chips {
		extra := lipgloss.Width(c.render)
		if i > 0 {
			extra += lipgloss.Width(sep)
		}
		if used+extra > budget {
			break
		}
		included = append(included, c)
		used += extra
	}

	skippedInactive := 0
	for i := len(included); i < len(chips); i++ {
		if chips[i].inactive {
			skippedInactive++
		}
	}

	// If we dropped any inactive chips, append a "+N idle" summary, dropping
	// trailing inactive chips as needed to make room (and updating the count).
	if skippedInactive > 0 {
		for {
			summary := dimStyle.Render(fmt.Sprintf("+%d idle", skippedInactive))
			cost := lipgloss.Width(sep) + lipgloss.Width(summary)
			if used+cost <= budget {
				included = append(included, chipEntry{render: summary})
				break
			}
			// No room — drop trailing chip. If it was inactive, bump the count.
			if len(included) <= 1 {
				break // can't drop DAEMON
			}
			drop := included[len(included)-1]
			included = included[:len(included)-1]
			used -= lipgloss.Width(drop.render) + lipgloss.Width(sep)
			if drop.inactive {
				skippedInactive++
			}
		}
	}

	parts := make([]string, 0, len(included))
	for _, c := range included {
		parts = append(parts, c.render)
	}
	return strings.Join(parts, sep)
}

// joinSegmentsSpaced packs leading + middle + trailing segments into a row of
// inner width w with the leading segment left-aligned, trailing right-aligned,
// and middle segments distributed in between with even spacing. Empty segments
// are skipped.
func joinSegmentsSpaced(w int, segs ...string) string {
	var nonEmpty []string
	for _, s := range segs {
		if s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	if len(nonEmpty) == 0 {
		return strings.Repeat(" ", w)
	}
	if len(nonEmpty) == 1 {
		return padTo(nonEmpty[0], w)
	}

	// Total visual width of segments
	used := 0
	for _, s := range nonEmpty {
		used += lipgloss.Width(s)
	}
	gaps := len(nonEmpty) - 1
	free := w - used
	if free < gaps {
		// Not enough room — fall back to single-space joins.
		return padTo(strings.Join(nonEmpty, " "), w)
	}
	per := free / gaps
	rem := free % gaps

	var sb strings.Builder
	for i, s := range nonEmpty {
		sb.WriteString(s)
		if i < gaps {
			extra := 0
			if i < rem {
				extra = 1
			}
			sb.WriteString(strings.Repeat(" ", per+extra))
		}
	}
	return sb.String()
}

// padLeftRightLine packs left content left-aligned and right content
// right-aligned within width w. Truncates left if total exceeds w.
func padLeftRightLine(w int, left, right string) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw >= w {
		// Right wins on overflow; left is dropped.
		if rw >= w {
			return right
		}
		return strings.Repeat(" ", w-rw) + right
	}
	gap := w - lw - rw
	return left + strings.Repeat(" ", gap) + right
}

// padTo right-pads s with spaces to reach visual width w.
func padTo(s string, w int) string {
	visual := lipgloss.Width(s)
	if visual >= w {
		return s
	}
	return s + strings.Repeat(" ", w-visual)
}

// buildTickSeparator builds a solid horizontal rule of width w using "─".
func buildTickSeparator(w int) string {
	if w <= 0 {
		return ""
	}
	return dimStyle.Render(strings.Repeat("─", w))
}

// renderDashboard builds the left-side dashboard column (all existing panels).
func (m Model) renderDashboard() string {
	var b strings.Builder

	// Set effective panel width on autopilot panel for stacked mode
	if m.autopilotPanel != nil {
		m.autopilotPanel.panelWidth = m.effectivePanelTotalWidth()
	}

	// Header: bordered banner frame (GH-2455 / GH-2459).
	// The ASCII logo is shown only during the splash; steady-state dashboard
	// uses the compact banner frame to keep header real-estate small.
	if m.showBanner {
		b.WriteString(m.renderBanner())
		b.WriteString("\n")
	}

	// Update notification (if available) — always visible regardless of banner
	if m.updateInfo != nil {
		b.WriteString(m.renderUpdateNotification())
		b.WriteString("\n")
	}

	// Metrics cards (tokens, cost, tasks)
	b.WriteString(m.renderMetricsCards())
	b.WriteString("\n")

	// Tasks
	b.WriteString(m.renderTasks())
	b.WriteString("\n")

	// Autopilot panel
	b.WriteString(m.autopilotPanel.View())
	b.WriteString("\n")

	// Eval stats
	if evalPanel := m.renderEvalStats(); evalPanel != "" {
		b.WriteString(evalPanel)
		b.WriteString("\n")
	}

	// History
	b.WriteString(m.renderHistory())
	b.WriteString("\n")

	// Logs (if enabled)
	if m.showLogs {
		b.WriteString(m.renderLogs())
		b.WriteString("\n")
	}

	// Help footer rendered separately in View() to survive height truncation

	return b.String()
}

// renderHelp returns a context-aware help footer that fits within panelTotalWidth (69 chars).
// Keys shown depend on gitGraphMode and gitGraphFocus state.
func (m Model) renderHelp() string {
	var parts []string
	switch {
	case m.gitGraphMode == GitGraphHidden:
		// Graph hidden: show navigation and graph-open key
		parts = []string{"q: quit", "l: logs", "b: banner", "g: graph", "j/k: select"}
	case m.gitGraphFocus:
		// Graph visible, graph panel focused
		parts = []string{"q: quit", "b: banner", "g: close", "tab: dashboard"}
	default:
		// Graph visible, dashboard focused
		parts = []string{"q: quit", "b: banner", "g: close", "tab: graph"}
	}
	help := strings.Join(parts, "  ")
	tw := m.effectivePanelTotalWidth()
	if len(help) > tw {
		help = help[:tw-3] + "..."
	}
	return helpStyle.Render(help)
}

// renderPanel builds a panel manually with guaranteed width.
// tw specifies the total visual width including borders.
// Structure: ╭─ TITLE ─...─╮ / │ (space) content (space) │ / ╰─...─╯
func renderPanel(title string, content string, tw int) string {
	var lines []string

	// Top border: ╭─ TITLE ─────────────────────────────────────────────────────╮
	lines = append(lines, buildTopBorder(title, tw))

	// Empty line padding
	lines = append(lines, buildEmptyLine(tw))

	// Content lines
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, buildContentLine(line, tw))
	}

	// Empty line padding
	lines = append(lines, buildEmptyLine(tw))

	// Bottom border
	lines = append(lines, buildBottomBorder(tw))

	return strings.Join(lines, "\n")
}

// buildTopBorder creates: ╭─ TITLE ─────...─────╮ with exact tw width
func buildTopBorder(title string, tw int) string {
	// Characters: ╭ (1) + ─ (1) + space (1) + TITLE + space (1) + dashes + ╮ (1)
	titleUpper := strings.ToUpper(title)
	prefix := "╭─ "
	prefixWidth := lipgloss.Width(prefix + titleUpper + " ")

	// Calculate dashes needed (each ─ is 1 visual char)
	dashCount := tw - prefixWidth - 1 // -1 for ╮
	if dashCount < 0 {
		dashCount = 0
	}

	// Style border chars dim, title bright
	return borderStyle.Render(prefix) + labelStyle.Render(titleUpper) + borderStyle.Render(" "+strings.Repeat("─", dashCount)+"╮")
}

// buildBottomBorder creates: ╰─────────────────────────────────────────────────╯
func buildBottomBorder(tw int) string {
	// ╰ + dashes + ╯
	dashCount := tw - 2
	line := "╰" + strings.Repeat("─", dashCount) + "╯"
	return borderStyle.Render(line)
}

// buildEmptyLine creates: │                                                                 │
func buildEmptyLine(tw int) string {
	// │ + spaces + │
	spaceCount := tw - 2
	border := borderStyle.Render("│")
	return border + strings.Repeat(" ", spaceCount) + border
}

// buildContentLine creates: │ (space) content padded/truncated (space) │
func buildContentLine(content string, tw int) string {
	// Available width for content = tw - 4 (│ + space + space + │)
	contentWidth := tw - 4

	// Pad or truncate content to exact width
	adjusted := padOrTruncate(content, contentWidth)

	// Only style borders, not content
	border := borderStyle.Render("│")
	return border + " " + adjusted + " " + border
}

// renderOrangePanel renders a panel with orange borders and title (for update notifications)
func renderOrangePanel(title string, content string, tw int) string {
	var lines []string

	// Top border
	lines = append(lines, buildOrangeTopBorder(title, tw))

	// Empty line padding
	lines = append(lines, buildOrangeEmptyLine(tw))

	// Content lines
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, buildOrangeContentLine(line, tw))
	}

	// Empty line padding
	lines = append(lines, buildOrangeEmptyLine(tw))

	// Bottom border
	lines = append(lines, buildOrangeBottomBorder(tw))

	return strings.Join(lines, "\n")
}

// buildOrangeTopBorder creates orange top border: ╭─ TITLE ─────...─────╮
func buildOrangeTopBorder(title string, tw int) string {
	titleUpper := strings.ToUpper(title)
	prefix := "╭─ "
	prefixWidth := lipgloss.Width(prefix + titleUpper + " ")

	dashCount := tw - prefixWidth - 1
	if dashCount < 0 {
		dashCount = 0
	}

	return orangeBorderStyle.Render(prefix) + orangeLabelStyle.Render(titleUpper) + orangeBorderStyle.Render(" "+strings.Repeat("─", dashCount)+"╮")
}

// buildOrangeBottomBorder creates orange bottom border: ╰─────────────────────────────────────────────────╯
func buildOrangeBottomBorder(tw int) string {
	dashCount := tw - 2
	line := "╰" + strings.Repeat("─", dashCount) + "╯"
	return orangeBorderStyle.Render(line)
}

// buildOrangeEmptyLine creates orange bordered empty line: │                                                                 │
func buildOrangeEmptyLine(tw int) string {
	spaceCount := tw - 2
	border := orangeBorderStyle.Render("│")
	return border + strings.Repeat(" ", spaceCount) + border
}

// buildOrangeContentLine creates orange bordered content line: │ (space) content padded/truncated (space) │
func buildOrangeContentLine(content string, tw int) string {
	contentWidth := tw - 4
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

	// We need to truncate to targetWidth-3 and add "...".
	// ANSI escape sequences (e.g. lipgloss color codes) are copied through with
	// zero visible width — counting their bytes as visible (the old behavior) made
	// styled strings break mid-escape and render blank. A CSI sequence starts at
	// ESC (0x1b) and ends at a byte in 0x40–0x7e.
	result := ""
	width := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			result += string(r)
			if r >= 0x40 && r <= 0x7e {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			result += string(r)
			continue
		}
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
// Total visual width equals ciw chars.
func renderSparkline(levels []int, pulsing bool, ciw int) string {
	var b strings.Builder
	// sparkline data chars = ciw - 1 (for pulsing indicator)
	dataWidth := ciw - 1

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

// miniCardEmptyLine returns a bordered empty line at exact cw width.
func miniCardEmptyLine(cw int) string {
	border := borderStyle.Render("│")
	return border + strings.Repeat(" ", cw-2) + border
}

// miniCardContentLine returns a bordered content line with 2-char padding each side.
func miniCardContentLine(content string, cw int) string {
	ciw := cw - 6 // inner width = card width minus borders and padding
	adjusted := padOrTruncate(content, ciw)
	border := borderStyle.Render("│")
	return border + "  " + adjusted + "  " + border
}

// miniCardHeaderLine returns a header with TITLE left-aligned and VALUE right-aligned.
func miniCardHeaderLine(title, value string, ciw int) string {
	styledTitle := titleStyle.Render(strings.ToUpper(title))
	titleWidth := lipgloss.Width(styledTitle)
	valueWidth := lipgloss.Width(value)
	gap := ciw - titleWidth - valueWidth
	if gap < 1 {
		gap = 1
	}
	return styledTitle + strings.Repeat(" ", gap) + value
}

// buildMiniCard assembles a full bordered mini-card.
func buildMiniCard(title, value, detail1, detail2, sparkline string, cw int) string {
	dashCount := cw - 2
	top := borderStyle.Render("╭" + strings.Repeat("─", dashCount) + "╮")
	bottom := borderStyle.Render("╰" + strings.Repeat("─", dashCount) + "╯")

	lines := []string{
		top,
		miniCardEmptyLine(cw),
		miniCardContentLine(miniCardHeaderLine(title, value, cw-6), cw),
		miniCardEmptyLine(cw),
		miniCardContentLine(detail1, cw),
		miniCardContentLine(detail2, cw),
		miniCardEmptyLine(cw),
		miniCardContentLine(sparkline, cw),
		miniCardEmptyLine(cw),
		bottom,
	}
	return strings.Join(lines, "\n")
}

// --- Card renderers ---

// renderTokenCard renders the TOKENS mini-card with the given card width.
func (m Model) renderTokenCard(cw int) string {
	ciw := cw - 6
	value := titleStyle.Render(formatCompact(m.metricsCard.TotalTokens))
	detail1 := dimStyle.Render(fmt.Sprintf("↑ %s input", formatCompact(m.metricsCard.InputTokens)))
	detail2 := dimStyle.Render(fmt.Sprintf("↓ %s output", formatCompact(m.metricsCard.OutputTokens)))

	// Convert int64 history to float64
	floats := make([]float64, len(m.metricsCard.TokenHistory))
	for i, v := range m.metricsCard.TokenHistory {
		floats[i] = float64(v)
	}
	levels := normalizeToSparkline(floats, ciw-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick, ciw))

	return buildMiniCard("tokens", value, detail1, detail2, spark, cw)
}

// renderCostCard renders the COST mini-card with the given card width.
func (m Model) renderCostCard(cw int) string {
	ciw := cw - 6
	value := costStyle.Render(fmt.Sprintf("$%.2f", m.metricsCard.TotalCostUSD))
	costPerTask := m.metricsCard.CostPerTask
	detail1 := dimStyle.Render(fmt.Sprintf("~$%.2f/task", costPerTask))
	detail2 := ""

	levels := normalizeToSparkline(m.metricsCard.CostHistory, ciw-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick, ciw))

	return buildMiniCard("cost", value, detail1, detail2, spark, cw)
}

// nonFailureSuffix builds the muted " (N no-op · M infra · …)" breakdown suffix
// for the QUEUE card, omitting any zero buckets. Returns "" when all are zero.
// On narrow cards the line truncates after the "✗ N failed" headline, which is
// always preserved. TASK-358: these are non-failure terminal outcomes split out
// of "failed".
func nonFailureSuffix(c MetricsCardData) string {
	buckets := []struct {
		n     int
		label string
	}{
		{c.NoOp, "no-op"},
		{c.Infra, "infra"},
		{c.Skipped, "skipped"},
		{c.RateLimited, "rate-limited"},
		{c.Stalled, "stalled"},
		{c.Declined, "declined"},
	}
	var parts []string
	for _, b := range buckets {
		if b.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", b.n, b.label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " · ") + ")"
}

// renderTaskCard renders the QUEUE mini-card with the given card width.
// Value shows current queue depth (pending + running), not lifetime totals.
func (m Model) renderTaskCard(cw int) string {
	ciw := cw - 6
	value := fmt.Sprintf("%d", len(m.tasks))
	detail1 := statusCompletedStyle.Render(fmt.Sprintf("✓ %d succeeded", m.metricsCard.Succeeded))
	// TASK-358: "failed" counts genuine failures only. Non-failure terminal
	// outcomes (no-op / infra / …) are shown as a muted suffix so the numbers
	// reconcile and a no-op is no longer miscounted as a failure. Only append the
	// suffix when the whole line fits the card — otherwise show just the headline.
	// The breakdown is wider than a mini-card on narrow terminals, and truncating
	// a styled multi-segment string blanks the line (the empty "failed" row seen
	// on v2.166.10).
	detail2 := statusFailedStyle.Render(fmt.Sprintf("✗ %d failed", m.metricsCard.Failed))
	if suffix := nonFailureSuffix(m.metricsCard); suffix != "" {
		withSuffix := detail2 + statusPendingStyle.Render(suffix)
		if lipgloss.Width(withSuffix) <= ciw {
			detail2 = withSuffix
		}
	}

	// Convert int history to float64
	floats := make([]float64, len(m.metricsCard.TaskHistory))
	for i, v := range m.metricsCard.TaskHistory {
		floats[i] = float64(v)
	}
	levels := normalizeToSparkline(floats, ciw-1)
	spark := statusRunningStyle.Render(renderSparkline(levels, m.sparklineTick, ciw))

	return buildMiniCard("queue", value, detail1, detail2, spark, cw)
}

// renderMetricsCards renders all three mini-cards side by side.
func (m Model) renderMetricsCards() string {
	epw := m.effectivePanelTotalWidth()
	cw := epw / 3
	remainder := epw - 3*cw
	// Distribute remainder as gaps between the 3 cards (2 gaps)
	gap1 := remainder / 2
	gap2 := remainder - gap1
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderTokenCard(cw), strings.Repeat(" ", gap1),
		m.renderCostCard(cw), strings.Repeat(" ", gap2),
		m.renderTaskCard(cw))
}

// taskStatePriority returns sort priority for task states (lower = higher in list).
func taskStatePriority(status string) int {
	switch status {
	case "done":
		return 0
	case "running":
		return 1
	case "queued":
		return 2
	case "pending":
		return 3
	case "failed":
		return 4
	default:
		return 5
	}
}

// renderTasks renders the tasks list with state-aware sorting and rendering.
func (m Model) renderTasks() string {
	var content strings.Builder

	if len(m.tasks) == 0 {
		content.WriteString("  No tasks in queue")
	} else {
		// Sort by state priority, then by ID within same state
		sorted := make([]TaskDisplay, len(m.tasks))
		copy(sorted, m.tasks)
		sort.SliceStable(sorted, func(i, j int) bool {
			pi, pj := taskStatePriority(sorted[i].Status), taskStatePriority(sorted[j].Status)
			if pi != pj {
				return pi < pj
			}
			return sorted[i].ID < sorted[j].ID
		})

		queueIdx := 0 // shimmer offset counter for queued items
		for i, task := range sorted {
			if i > 0 {
				content.WriteString("\n")
			}
			offset := 0
			if task.Status == "queued" {
				offset = queueIdx
				queueIdx++
			}
			content.WriteString(m.renderTask(task, i == m.selectedTask, offset))
		}
	}

	return renderPanel("QUEUE", content.String(), m.effectivePanelTotalWidth())
}

// renderTask renders a single task row with state-aware icons, bars, and meta.
//
// Layout (65 inner chars):
//
//	sel(2) + icon+state(8) + space(1) + id(7) + space(1) + title(20) + gap(2) + bar(16) + gap(1) + meta(5)
func (m Model) renderTask(task TaskDisplay, selected bool, queueOffset int) string {
	var icon, stateLabel, meta string
	var iconStyle, barStyle lipgloss.Style

	switch task.Status {
	case "done":
		icon = "✓"
		stateLabel = "done"
		meta = extractPRNumber(task.PRURL)
		iconStyle = statusDoneStyle
		barStyle = progressBarDoneStyle
	case "running":
		icon = "●"
		stateLabel = "running"
		meta = fmt.Sprintf("%4d%%", task.Progress)
		iconStyle = statusRunningStyle
		barStyle = progressBarStyle
	case "queued":
		icon = "◌"
		stateLabel = "queued"
		meta = fmt.Sprintf("  #%d", queueOffset+1)
		iconStyle = statusQueuedStyle
	case "failed":
		icon = "✗"
		stateLabel = "failed"
		meta = truncateVisual(task.Phase, 5)
		iconStyle = statusFailedStyle
		barStyle = progressBarFailedStyle
	default: // pending
		icon = "·"
		stateLabel = "pending"
		meta = ""
		iconStyle = statusPendingStyle
	}

	// Pulse the running icon on animation tick
	renderedIcon := iconStyle.Render(icon)
	if task.Status == "running" && !m.sparklineTick {
		renderedIcon = dimStyle.Render(icon)
	}

	// Build icon+state column (8 chars visual: "● running" or "✓ done   ")
	iconState := renderedIcon + " " + iconStyle.Render(fmt.Sprintf("%-7s", stateLabel))

	// Selector
	selector := "  "
	if selected {
		selector = dimStyle.Render("▸") + " "
	}

	// Progress bar (14 chars inside brackets, 16 total with [])
	var progressBar string
	switch task.Status {
	case "done":
		bar := barStyle.Render(strings.Repeat("█", 14))
		progressBar = "[" + bar + "]"
	case "running":
		progressBar = m.renderProgressBar(task.Progress, 14)
	case "queued":
		progressBar = m.renderShimmerBar(14, queueOffset)
	case "failed":
		progressBar = m.renderFailedBar(task.Progress, 14)
	default: // pending
		bar := progressEmptyStyle.Render(strings.Repeat(" ", 14))
		progressBar = "[" + bar + "]"
	}

	// Render meta with state-appropriate color
	renderedMeta := iconStyle.Render(fmt.Sprintf("%5s", meta))

	// Left side: selector + icon+state + id + title
	// Right side: bar + meta (right-aligned)
	return fmt.Sprintf("%s%s %-7s %-20s  %s %s",
		selector,
		iconState,
		task.ID,
		truncateVisual(task.Title, 20),
		progressBar,
		renderedMeta,
	)
}

// renderProgressBar renders a standard progress bar for running tasks.
func (m Model) renderProgressBar(progress int, width int) string {
	filled := progress * width / 100
	empty := width - filled

	bar := progressBarStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", empty))

	return "[" + bar + "]"
}

// renderFailedBar renders a progress bar frozen at the failure point in dusty rose.
func (m Model) renderFailedBar(progress int, width int) string {
	filled := progress * width / 100
	empty := width - filled

	bar := progressBarFailedStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", empty))

	return "[" + bar + "]"
}

// renderShimmerBar renders an animated shimmer bar for queued tasks.
// A 3-char bright spot (░▒▓▒░) slides across the bar, staggered by offset.
func (m Model) renderShimmerBar(width, offset int) string {
	bar := make([]rune, width)
	for i := range bar {
		bar[i] = '░'
	}

	// Center of bright spot, staggered per queue position
	center := (m.shimmerTick + offset*3) % width

	// Apply shimmer pattern: ░▒▓▒░
	type shimmerChar struct {
		offset int
		char   rune
		style  lipgloss.Style
	}
	pattern := []shimmerChar{
		{-2, '░', shimmerDimStyle},
		{-1, '▒', shimmerMidStyle},
		{0, '▓', shimmerBrightStyle},
		{1, '▒', shimmerMidStyle},
		{2, '░', shimmerDimStyle},
	}

	// Build styled string character by character
	var result strings.Builder
	result.WriteString("[")
	for i := 0; i < width; i++ {
		styled := false
		for _, p := range pattern {
			pos := (center + p.offset + width) % width
			if pos == i {
				result.WriteString(p.style.Render(string(p.char)))
				styled = true
				break
			}
		}
		if !styled {
			result.WriteString(shimmerDimStyle.Render("░"))
		}
	}
	result.WriteString("]")
	return result.String()
}

// extractPRNumber extracts "#1234" from a GitHub PR URL like "https://github.com/owner/repo/pull/1234".
// Returns empty string if URL is empty or doesn't match.
func extractPRNumber(prURL string) string {
	if prURL == "" {
		return ""
	}
	// Find last "/" and extract number after it
	idx := strings.LastIndex(prURL, "/")
	if idx >= 0 && idx < len(prURL)-1 {
		num := prURL[idx+1:]
		return fmt.Sprintf("#%s", num)
	}
	return ""
}

// historyGroup represents a top-level entry in the HISTORY panel.
// It is either a standalone task, an active epic (expanded with sub-issues),
// or a completed epic (collapsed to one line).
type historyGroup struct {
	Task      CompletedTask   // The top-level task (standalone or epic parent)
	SubIssues []CompletedTask // Sub-issues (only populated for epics)
}

// groupedHistory transforms the flat completedTasks slice into groups.
// Sub-issues (ParentID != "") are absorbed under their parent epic.
// Standalone tasks and epics without children in the list pass through as-is.
func (m Model) groupedHistory() []historyGroup {
	// Build lookup: ParentID → children
	childrenOf := make(map[string][]CompletedTask)
	parentIDs := make(map[string]bool)
	for _, t := range m.completedTasks {
		if t.ParentID != "" {
			childrenOf[t.ParentID] = append(childrenOf[t.ParentID], t)
		}
		if t.IsEpic {
			parentIDs[t.ID] = true
		}
	}

	var groups []historyGroup
	seen := make(map[string]bool)

	for _, t := range m.completedTasks {
		if seen[t.ID] {
			continue
		}
		// Skip sub-issues whose parent is present in the list
		if t.ParentID != "" && parentIDs[t.ParentID] {
			continue
		}
		seen[t.ID] = true

		g := historyGroup{Task: t}
		if t.IsEpic {
			g.SubIssues = childrenOf[t.ID]
		}
		groups = append(groups, g)
	}
	return groups
}

// renderEpicProgressBar renders a compact progress bar: [##--]
// innerWidth chars inside brackets, '#' for done, '-' for remaining.
func renderEpicProgressBar(done, total, innerWidth int) string {
	if total <= 0 {
		return "[" + strings.Repeat("-", innerWidth) + "]"
	}
	filled := done * innerWidth / total
	if filled > innerWidth {
		filled = innerWidth
	}
	empty := innerWidth - filled
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", empty) + "]"
}

// renderEvalStats renders a compact eval stats panel showing latest pass@1 rate
// with a trend indicator and optional regression warning.
func (m Model) renderEvalStats() string {
	if m.store == nil {
		return ""
	}

	tw := m.effectivePanelTotalWidth()

	tasks, err := m.store.ListEvalTasks(memory.EvalTaskFilter{Limit: 200})
	if err != nil || len(tasks) == 0 {
		return ""
	}

	// Compute current pass@1 rate from all tasks.
	var passed int
	for _, t := range tasks {
		if t.Success {
			passed++
		}
	}
	rate := float64(passed) / float64(len(tasks)) * 100

	// Determine trend: compare latest half vs oldest half as a simple baseline.
	mid := len(tasks) / 2
	if mid == 0 {
		mid = 1
	}
	// tasks are ordered DESC (newest first)
	recent := tasks[:mid]
	older := tasks[mid:]

	var recentPassed, olderPassed int
	for _, t := range recent {
		if t.Success {
			recentPassed++
		}
	}
	for _, t := range older {
		if t.Success {
			olderPassed++
		}
	}

	recentRate := float64(recentPassed) / float64(len(recent)) * 100
	olderRate := float64(olderPassed) / float64(len(older)) * 100
	delta := recentRate - olderRate

	// Trend indicator
	var trend string
	switch {
	case delta > 2:
		trend = statusDoneStyle.Render("↑")
	case delta < -2:
		trend = statusFailedStyle.Render("↓")
	default:
		trend = dimStyle.Render("→")
	}

	// Format: "  pass@1  72.5%  ↑  (42 tasks)"
	line := fmt.Sprintf("  pass@1  %.1f%%  %s  %s",
		rate,
		trend,
		dimStyle.Render(fmt.Sprintf("(%d tasks)", len(tasks))),
	)

	// Regression warning
	report := memory.CheckRegression(older, recent, memory.DefaultRegressionThreshold)
	if report.Regressed {
		line += "\n" + "  " + statusFailedStyle.Render(
			fmt.Sprintf("! regression: %.1fpp drop (%d task(s))", -report.Delta, len(report.RegressedTaskIDs)),
		)
	}

	evalTitle := "EVAL"
	if m.metricsScopePath != "" {
		evalTitle = "EVAL [global]"
	}
	return renderPanel(evalTitle, line, tw)
}

// renderHistory renders completed tasks history with epic-aware grouping.
// Active epics show expanded with sub-issue tree; completed epics collapse to one line.
func (m Model) renderHistory() string {
	var content strings.Builder
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4

	if len(m.completedTasks) == 0 {
		content.WriteString("  No completed tasks yet")
		return renderPanel("HISTORY", content.String(), tw)
	}

	groups := m.groupedHistory()
	first := true

	for _, g := range groups {
		if g.Task.IsEpic {
			isActive := g.Task.DoneSubs < g.Task.TotalSubs
			if isActive {
				// Active epic: expanded with progress bar and sub-issues
				if !first {
					content.WriteString("\n")
				}
				first = false
				content.WriteString(renderActiveEpicLine(g.Task, iw))
				for _, sub := range g.SubIssues {
					content.WriteString("\n")
					content.WriteString(renderSubIssueLine(sub, iw))
				}
			} else {
				// Completed epic: collapsed single line with [N/N]
				if !first {
					content.WriteString("\n")
				}
				first = false
				content.WriteString(renderCompletedEpicLine(g.Task, iw))
			}
		} else {
			// Standalone task: same as before
			if !first {
				content.WriteString("\n")
			}
			first = false
			content.WriteString(renderStandaloneLine(g.Task, iw))
		}
	}

	return renderPanel("HISTORY", content.String(), tw)
}

// renderStandaloneLine renders a standalone (non-epic) task line.
// Layout: "  + GH-156  Title...                                    2m ago"
// indent(2) + icon(1) + space(1) + id(7) + space(2) + title + space(2) + timeAgo(8) = iw
// When PeakRSSMB > 0 (GH-3028), an RSS indicator ("4.2G") is appended after the time.
func renderStandaloneLine(task CompletedTask, iw int) string {
	icon, style := statusIconStyle(task.Status)
	timeAgoStr := formatTimeAgo(task.CompletedAt)

	var rssStr string
	if task.PeakRSSMB > 0 {
		rssStr = fmt.Sprintf(" %s", formatRSSMB(task.PeakRSSMB))
	}

	// Reserve space: indent(2)+icon(1)+sp(1)+id(7)+sp(2)+sp(2)+time(8)+rss = 23+len(rssStr)
	titleWidth := iw - 23 - len(rssStr)
	if titleWidth < 10 {
		titleWidth = 10
	}
	titleStr := padOrTruncate(task.Title, titleWidth)

	return fmt.Sprintf("  %s %-7s  %s  %8s%s",
		style.Render(icon),
		task.ID,
		titleStr,
		dimStyle.Render(timeAgoStr),
		dimStyle.Render(rssStr),
	)
}

// formatRSSMB formats a RSS value in MiB as a compact human-readable string.
// 512 → "512M", 2048 → "2.0G", 10240 → "10G".
func formatRSSMB(mb int) string {
	if mb < 1024 {
		return fmt.Sprintf("%dM", mb)
	}
	gb := float64(mb) / 1024.0
	if gb < 10 {
		return fmt.Sprintf("%.1fG", gb)
	}
	return fmt.Sprintf("%.0fG", gb)
}

// renderActiveEpicLine renders the parent line for an active epic.
func renderActiveEpicLine(task CompletedTask, iw int) string {
	const progressInnerWidth = 4
	// Recalculate: total = indent(2)+icon(1)+sp(1)+id(7)+sp(2)+title+sp(2)+right(rightWidth) = 65
	// title = 65 - 2 - 1 - 1 - 7 - 2 - 2 - rightWidth = 65 - 15 - rightWidth
	// Let's be precise:
	// indent(2) + icon(1) + sp(1) + id(7) + sp(2) + title + sp(1) + progress(6) + sp(1) + counts + sp(1) + time
	// We need the right side to fit. Let's use fixed columns:

	bar := renderEpicProgressBar(task.DoneSubs, task.TotalSubs, progressInnerWidth)
	counts := fmt.Sprintf("%d/%d", task.DoneSubs, task.TotalSubs)
	timeStr := task.Duration
	if timeStr == "" {
		timeStr = formatTimeAgo(task.CompletedAt)
	}

	// Right part: " [##--] 2/3   3m" — build with fixed width
	// bar(6) + sp(1) + counts(padded to 5) + sp(1) + time(padded to 5)
	rightPart := fmt.Sprintf(" %s %-5s %5s", bar, counts, timeStr)
	rightLen := len(rightPart) // plain ASCII, no ANSI

	// Title gets whatever remains
	tWidth := iw - 2 - 1 - 1 - 7 - 2 - rightLen
	if tWidth < 10 {
		tWidth = 10
	}

	titleStr := padOrTruncate(task.Title, tWidth)

	return fmt.Sprintf("  %s %-7s  %s%s",
		warningStyle.Render("*"),
		task.ID,
		titleStr,
		rightPart,
	)
}

// renderCompletedEpicLine renders a collapsed completed epic.
func renderCompletedEpicLine(task CompletedTask, iw int) string {
	counts := fmt.Sprintf("[%d/%d]", task.DoneSubs, task.TotalSubs)
	timeAgoStr := formatTimeAgo(task.CompletedAt)

	// Right part: " [N/N]    Xm ago"
	rightPart := fmt.Sprintf(" %s  %8s", counts, timeAgoStr)
	rightLen := len(rightPart)

	// Title = iw - indent(2) - icon(1) - sp(1) - id(7) - sp(2) - rightLen
	tWidth := iw - 2 - 1 - 1 - 7 - 2 - rightLen
	if tWidth < 10 {
		tWidth = 10
	}

	icon, style := statusIconStyle(task.Status)
	titleStr := padOrTruncate(task.Title, tWidth)

	return fmt.Sprintf("  %s %-7s  %s%s",
		style.Render(icon),
		task.ID,
		titleStr,
		dimStyle.Render(rightPart),
	)
}

// renderSubIssueLine renders an indented sub-issue line under an active epic.
func renderSubIssueLine(task CompletedTask, iw int) string {
	titleWidth := iw - 25 // extra 2 indent vs standalone
	icon, style := subIssueIconStyle(task.Status)

	var timeStr string
	switch task.Status {
	case "pending":
		timeStr = "--"
	case "running":
		timeStr = "now"
	default:
		timeStr = formatTimeAgo(task.CompletedAt)
	}

	titleStr := padOrTruncate(task.Title, titleWidth)

	return fmt.Sprintf("    %s %-7s  %s  %8s",
		style.Render(icon),
		task.ID,
		titleStr,
		dimStyle.Render(timeStr),
	)
}

// statusIconStyle returns the icon and style for a task status (top-level tasks).
func statusIconStyle(status string) (string, lipgloss.Style) {
	switch status {
	case "success":
		return "+", statusCompletedStyle
	case "failed":
		return "x", statusFailedStyle
	case "stalled":
		return "~", statusFailedStyle
	case "no_op":
		return "=", statusPendingStyle // TASK-358: no-change run, not a failure
	case "declined":
		return "-", statusPendingStyle // TASK-358: agent declined as unactionable
	case "rate_limited":
		return "%", statusPendingStyle // TASK-358: provider quota hit, transient
	case "infra":
		return "!", statusPendingStyle // TASK-358: plumbing/resource failure, not the work
	case "skipped":
		return ".", statusPendingStyle // TASK-358: never ran / cancelled
	case "running":
		return "~", statusRunningStyle
	default:
		return ".", statusPendingStyle
	}
}

// subIssueIconStyle returns the icon and style for a sub-issue status.
// Uses the same mapping but included for clarity/future divergence.
func subIssueIconStyle(status string) (string, lipgloss.Style) {
	return statusIconStyle(status)
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
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4
	w := iw - 4 // Account for indent (2 spaces each side)

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

	return renderPanel("LOGS", content.String(), tw)
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

// UpdateTokens sends updated token usage to the TUI.
// model is the model name that produced the tokens; may be empty before the first
// stream event, in which case the handler falls back to memory.DefaultModel.
func UpdateTokens(input, output int, model string) tea.Cmd {
	return func() tea.Msg {
		return updateTokensMsg(TokenUsage{
			InputTokens:  input,
			OutputTokens: output,
			TotalTokens:  input + output,
			Model:        model,
		})
	}
}

// AddCompletedTask sends a completed task to the TUI history.
// parentID is the parent issue ID for sub-issues (empty string if none).
// isEpic indicates whether the task was decomposed into sub-issues.
func AddCompletedTask(id, title, status, duration string, parentID string, isEpic bool) tea.Cmd {
	return func() tea.Msg {
		return addCompletedTaskMsg(CompletedTask{
			ID:          id,
			Title:       title,
			Status:      status,
			Duration:    duration,
			CompletedAt: time.Now(),
			ParentID:    parentID,
			IsEpic:      isEpic,
		})
	}
}

// renderUpdateNotification renders the update notification panel
func (m Model) renderUpdateNotification() string {
	var content strings.Builder
	var title string
	var hint string
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4

	switch m.upgradeState {
	case UpgradeStateAvailable:
		title = "^ UPDATE"
		// Left: version info, Right: will be hint below panel
		leftText := fmt.Sprintf("%s -> %s available", m.updateInfo.CurrentVersion, m.updateInfo.LatestVersion)
		rightText := ""
		content.WriteString(formatPanelRow(leftText, rightText, iw))
		hint = "u: upgrade"

	case UpgradeStateInProgress:
		title = "* UPGRADING"
		bar := m.renderProgressBar(m.upgradeProgress, 30)
		content.WriteString(fmt.Sprintf("  Installing %s... %s %d%%", m.updateInfo.LatestVersion, bar, m.upgradeProgress))
		if m.upgradeMessage != "" {
			content.WriteString("\n  " + m.upgradeMessage)
		}

	case UpgradeStateComplete:
		title = "+ UPGRADED"
		content.WriteString(fmt.Sprintf("  Upgrade to %s installed — restart Pilot manually to apply.", m.updateInfo.LatestVersion))

	case UpgradeStateFailed:
		title = "! UPGRADE FAILED"
		if m.upgradeError != "" {
			content.WriteString("  " + m.upgradeError)
		} else {
			content.WriteString("  " + m.upgradeMessage)
		}

	default:
		return ""
	}

	result := renderOrangePanel(title, content.String(), tw)
	if hint != "" {
		// Right-align hint under panel
		hintLine := fmt.Sprintf("%*s", tw, hint)
		result += "\n" + dimStyle.Render(hintLine)
	}
	return result
}

// formatPanelRow creates a full-width row with left and right aligned text
func formatPanelRow(left, right string, iw int) string {
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	padding := iw - leftWidth - rightWidth - 4 // 4 for indent
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
