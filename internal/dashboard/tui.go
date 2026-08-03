package dashboard

import (
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/qf-studio/grom/pkg/tui/render"

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
	cardWidth = 23 // 23*3 = 69 = panelTotalWidth (no gaps)
)

// MetricsCardData holds aggregated metrics for the dashboard metrics cards.
type MetricsCardData struct {
	TotalTokens, InputTokens, OutputTokens  int
	CacheReadTokens, CacheWriteTokens       int
	TotalCostUSD, CostPerTask               float64
	TotalTasks, Succeeded, Failed, Declined int
	// TASK-358: non-failure terminal outcomes, split out of "failed".
	NoOp, Stalled, RateLimited, Infra, Skipped int
	TokenHistory                               []int64   // 7 days, fresh (input+output)
	CachedTokenHistory                         []int64   // 7 days, cache read+write
	CostHistory                                []float64 // 7 days
	TaskHistory                                []int     // 7 days
	SuccessHistory                             []int     // 7 days
	FailedHistory                              []int     // 7 days
}

// Styles (muted terminal aesthetic)
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7eb8da")) // steel blue

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
	panelWidth int  // dynamic panel width, set before View()
	focused    bool // TASK-399: true when this panel holds spatial focus, set before View()
}

// NewAutopilotPanel creates an autopilot panel.
func NewAutopilotPanel(controller *autopilot.Controller) *AutopilotPanel {
	if controller == nil {
		return &AutopilotPanel{controller: nil, panelWidth: panelTotalWidth}
	}
	return &AutopilotPanel{controller: controller, panelWidth: panelTotalWidth}
}

// maxAutopilotRows caps how many PRs render as rows before collapsing to a
// "+ N more" summary line.
const maxAutopilotRows = 4

// autopilotNoActivePRLabel is shown when GetActivePRs() is empty. TASK-420/
// GH-4537: previously read "idle · no active PR" — "idle" reads as "nothing
// is happening", but this panel only tracks PR-side lifecycle (CI/approval/
// merge/release); it says nothing about executions in flight pre-PR. The
// 2026-07-24 22:17:07Z capture had this panel showing exactly that text while
// a Claude Code process was actively running toward GH-4536's first commit —
// correct for what the panel tracks, but routinely misread as "nothing is
// running" (see queue/history for that). Drop "idle" and point at the queue
// instead of implying total inactivity.
const autopilotNoActivePRLabel = "no active PR · check queue for running work"

// View renders the autopilot panel in the history-row grammar: one line per
// active PR — status glyph, #id, title, 5-cell lifecycle meter
// (ci→rebase→merge→tag→release), stage label, age — plus a ↳ detail line
// for retries/failures. The PR count renders as the border legend.
func (p *AutopilotPanel) View() string {
	tw := p.panelWidth
	if tw < panelTotalWidth {
		tw = panelTotalWidth
	}
	iw := tw - 4
	chrome := panelChrome
	if p.focused {
		chrome = focusChrome
	}

	if p.controller == nil {
		return renderPanelStyled("autopilot", "", "  Disabled", tw, chrome)
	}

	prs := p.controller.GetActivePRs()
	if len(prs) == 0 {
		return renderPanelStyled("autopilot", "", "  "+dimStyle.Render(autopilotNoActivePRLabel), tw, chrome)
	}

	cfg := p.controller.Config()
	maxFailures := cfg.MaxFailures
	if maxFailures <= 0 {
		maxFailures = 5
	}

	var lines []string
	for i, pr := range prs {
		if i == maxAutopilotRows {
			lines = append(lines, "  "+dimStyle.Render(fmt.Sprintf("+ %d more", len(prs)-maxAutopilotRows)))
			break
		}
		lines = append(lines, renderAutopilotRow(pr, p.controller.GetPRFailures(pr.PRNumber), maxFailures, iw)...)
	}

	count := fmt.Sprintf("%d prs", len(prs))
	if len(prs) == 1 {
		count = "1 pr"
	}
	legend := statusRunningStyle.Render("●") + " " + dimStyle.Render(count)
	return renderPanelStyled("autopilot", legend, strings.Join(lines, "\n"), tw, chrome)
}

// sortedActivePRs returns the controller's active PRs sorted by PR number.
// Used by the zoomed autopilot view: GetActivePRs iterates a map internally,
// so raw order is nondeterministic across calls — sorting gives a stable
// render order and lets selection be keyed by PR number instead of index
// (TASK-399: "survives live-pull reorder").
func (p *AutopilotPanel) sortedActivePRs() []*autopilot.PRState {
	if p == nil || p.controller == nil {
		return nil
	}
	prs := p.controller.GetActivePRs()
	sort.Slice(prs, func(i, j int) bool { return prs[i].PRNumber < prs[j].PRNumber })
	return prs
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

// autopilotNodes are the 5 lifecycle stages the meter and label measure.
// pos 1 is the approval gate (StageAwaitApproval / StageReviewRequested) —
// it must read "approval", never "rebase": GH-4383, an approval-delivery
// outage rendered awaiting_approval PRs as failed rebases in the live panel.
var autopilotNodes = [...]string{"ci", "approval", "merge", "tag", "release"}

// autopilotStageLabelWidth fits the longest node name ("approval").
const autopilotStageLabelWidth = 8

// renderAutopilotRow renders one active-PR line (+ optional ↳ detail line)
// in the history-row grammar:
//
//	● #4054   fix(executor): skip decompos…  ■■□□□ merge      2m
//	  ↳ ⟲ retry 2/3 · TestFoo failed · linux-amd64
//
// indent(2) + glyph(1) + sp(1) + id(6) + sp(2) + title(flex) + sp(2) +
// meter(5) + sp(1) + stage(8) + sp(2) + age(6) = iw
// Glyphs: ✗ pipeline failed, ⟲ retrying (CI failure or prior failures),
// ● climbing. Meter color follows: rose / amber / accent.
func renderAutopilotRow(pr *autopilot.PRState, failures, maxFailures, iw int) []string {
	pos := pipelineStagePosition(pr.Stage)
	label := autopilotStageLabel(pr, pos)

	glyph, glyphStyle, hex := "●", statusRunningStyle, gromTheme.Accent
	switch {
	case pr.Stage == autopilot.StageFailed:
		glyph, glyphStyle, hex = "✗", statusFailedStyle, gromTheme.Error
	case pr.CIStatus == autopilot.CIFailure || failures > 0:
		glyph, glyphStyle, hex = "⟲", warningStyle, gromTheme.Warning
	}

	titleWidth := iw - 2 - 1 - 1 - 6 - 2 - 2 - len(autopilotNodes) - 1 - autopilotStageLabelWidth - 2 - 6
	if titleWidth < 10 {
		titleWidth = 10
	}

	// Title fallback: states restored from SQLite before pr_title was
	// persisted (or before the poll backfill runs) identify by branch name
	// instead of a blank flex column.
	title := pr.PRTitle
	if title == "" {
		title = pr.BranchName
	}

	// GH-4383: the age column reads wait-time-in-approval, not time-since-PR-
	// opened, once a PR is parked on the approval gate — the panel's whole
	// point is telling the operator how long the outstanding ask has been
	// waiting, not how old the PR is.
	ageSince := pr.CreatedAt
	if pr.Stage == autopilot.StageAwaitApproval && !pr.ApprovalRequestedAt.IsZero() {
		ageSince = pr.ApprovalRequestedAt
	}

	row := fmt.Sprintf("  %s %-6s  %s  %s %s  %s",
		glyphStyle.Render(glyph),
		fmt.Sprintf("#%d", pr.PRNumber),
		padOrTruncate(title, titleWidth),
		segmentMeter(pos, len(autopilotNodes), len(autopilotNodes), hex),
		dimStyle.Render(padOrTruncate(label, autopilotStageLabelWidth)),
		dimStyle.Render(fmt.Sprintf("%6s", formatDurationShort(time.Since(ageSince)))),
	)
	lines := []string{row}

	// Detail line: retry progress (amber, only once failures exist — clean
	// runs carry no retry chrome) and/or the failure reason. The failure
	// reason is shown whenever PRState.Error is set, not just for the
	// terminal StageFailed row — a stage handler can fail (and set Error)
	// while the PR stays parked on a non-terminal stage, e.g. an
	// approval-submit send failure while Stage stays awaiting_approval
	// (GH-4383/GH-4380) — hiding that error is how the panel went silent
	// about the real root cause during the outage.
	detail := ""
	budget := iw - 6 // indent(4) + "↳ "
	if failures > 0 {
		retry := fmt.Sprintf("⟲ %s %d/%d", retryLabelFor(pr.Stage), failures, maxFailures)
		detail = warningStyle.Render(retry)
		budget -= lipgloss.Width(retry)
	}
	if pr.Error != "" {
		if detail != "" {
			detail += dimStyle.Render(" · ")
			budget -= 3
		}
		if budget < 5 {
			budget = 5
		}
		detail += dimStyle.Render(truncateString(pr.Error, budget))
	}
	if detail != "" {
		lines = append(lines, "    "+dimStyle.Render("↳ ")+detail)
	}
	return lines
}

// autopilotStageLabel resolves the truthful node-name label for a PR row.
// pos 1 (StageAwaitApproval / StageReviewRequested) always reads "approval"
// via autopilotNodes — never "rebase" (GH-4383). "rebase" only appears while
// StageMerging is actively re-attempting after a real auto-rebase
// (RebaseAttempts > 0); a merging PR that hasn't hit a conflict yet, or a PR
// parked on any other stage, is never mislabeled as rebasing.
func autopilotStageLabel(pr *autopilot.PRState, pos int) string {
	if pr.Stage == autopilot.StageFailed {
		return "failed"
	}
	if pr.Stage == autopilot.StageMerging && pr.RebaseAttempts > 0 {
		return "rebase"
	}
	return autopilotNodes[pos]
}

// retryLabelFor names what the failure-count chrome is actually retrying.
// GetPRFailures is a generic per-PR circuit breaker incremented on ANY
// stage-handler error (recordPRFailure, controller.go) — not a rebase
// counter — so pairing it with the approval-gate stage must say what's
// really being retried: the async approval-request submit call (GH-4383's
// approval-delivery outage was misread as failing rebases because the old
// "rebase" stage label made a generic "retry N/M" look rebase-specific).
// Every other stage keeps the generic wording.
func retryLabelFor(stage autopilot.PRStage) string {
	if stage == autopilot.StageAwaitApproval {
		return "send retry"
	}
	return "retry"
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

// truncateString truncates a string to maxLen, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// QueueStatus is the closed vocabulary for TaskDisplay.Status (TASK-420/
// GH-4537). It is a presentational rename of the executor package's
// TaskStatus/ExecStatus* vocabulary (internal/executor/monitor.go,
// internal/executor/lifecycle.go) — StatusCompleted -> "done", etc. — chosen
// once, in convertTaskStatesToDisplay (cmd/pilot/commands.go), the single
// function every UpdateTasks producer funnels through. Before this type
// existed the value was a bare string with no named source of truth; nothing
// stopped a sixth call site from inventing its own spelling of "done".
type QueueStatus string

const (
	QueueStatusDone    QueueStatus = "done"    // executor.StatusCompleted
	QueueStatusRunning QueueStatus = "running" // executor.StatusRunning
	QueueStatusQueued  QueueStatus = "queued"  // executor.StatusQueued
	QueueStatusFailed  QueueStatus = "failed"  // executor.StatusFailed (and cancelled/stalled — see terminalMonitorStatus)
	QueueStatusNoOp    QueueStatus = "no_op"   // executor.StatusNoOp
	QueueStatusPending QueueStatus = "pending" // executor.StatusPending / zero value
)

// TaskDisplay represents a task for display
type TaskDisplay struct {
	ID          string
	Title       string
	Status      QueueStatus
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
	// Stage is the pipeline-progress summary built from the execution's
	// execution_events timeline (GH-3849; structured for the grom segment
	// meter). Zero-value (Known=false) when the task wasn't hydrated from
	// the store (e.g. AddCompletedTask callers) — the ladder renders as an
	// empty track in that case.
	Stage StageInfo
	// PRUrl is the pull request URL for this execution (from Execution.PRUrl),
	// used by the zoomed history panel's enter/o open-URL action. Empty when
	// the task shipped without a PR or wasn't hydrated from the store.
	PRUrl string
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

	// Banner toggle (GH-1520)
	showBanner bool

	// stalenessBanner holds the formatted stale/archived-ledger warning
	// (GH-4569), computed once from the store on hydrate. Empty means the
	// ledger looks healthy — nothing to show.
	stalenessBanner string

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
	dbSyncTick         int    // Counter for periodic DB re-sync (GH-2248)
	projectPath        string // Working directory for git commands
	defaultProjectPath string // Fallback project path from config (GH-2167)
	metricsScopePath   string // Project path used to filter store/metrics queries (GH-3531)
	gitProjectName     string // Current project name shown in git panel title (GH-2167)

	// Spatial grid navigation (TASK-399): focus tracks the spatially-selected
	// panel; zoomed opens it full-screen. See grid.go/panels.go/zoomlist.go.
	focus       panelID
	zoomed      bool
	zoomSel     int
	zoomScroll  int
	zoomHistory []CompletedTask
	// zoomAutopilotPR is the selected PR number in the zoomed autopilot list.
	// Keyed by PR number rather than raw index because GetActivePRs iterates
	// a map — a live-pull reorder between frames must not move the selection.
	zoomAutopilotPR int
	// zoomLogsFollow tracks tail-follow state for the zoomed logs panel:
	// true (default) keeps the viewport pinned to the newest line as more
	// arrive; scrolling up breaks follow, 'G' resumes it.
	zoomLogsFollow bool

	// browserOpener opens a URL in the default browser; defaults to
	// openBrowser but is injectable so tests can capture opened URLs.
	browserOpener func(string) error
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
		focus:          panelQueue,
		gitGraphMode:   GitGraphVisible,
		browserOpener:  openBrowser,
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
		focus:          panelQueue,
		gitGraphMode:   GitGraphVisible,
		browserOpener:  openBrowser,
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
		focus:          panelQueue,
		gitGraphMode:   GitGraphVisible,
		browserOpener:  openBrowser,
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
		focus:          panelQueue,
		gitGraphMode:   GitGraphVisible,
		browserOpener:  openBrowser,
	}
	m.hydrateFromStore()
	return m
}

// hydrateFromStore loads persisted state from SQLite.
func (m *Model) hydrateFromStore() {
	if m.store == nil {
		return
	}

	// GH-4569: check for a stale/archived ledger before anything else reads
	// it — a frozen ledger answers every query successfully with wrong
	// data, so this must be visible in the header regardless of what else
	// hydration does below.
	if info, err := m.store.CheckStaleness(memory.StalenessThreshold()); err != nil {
		slog.Warn("failed to check ledger staleness", slog.Any("error", err))
	} else {
		m.stalenessBanner = info.Banner()
	}

	// GH-4368: one-shot archaeology heal, before anything below reads
	// execution_events — backfills the terminal ladder event on any row
	// stuck at status='completed' with a frozen/pre-H4 event stream so
	// HISTORY doesn't render it as still in-flight (e.g. "running").
	if _, err := m.store.HealFrozenHistoryLadders(); err != nil {
		slog.Warn("failed to heal frozen history ladders", slog.Any("error", err))
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
		m.metricsCard.CacheReadTokens = int(lifetime.CacheReadTokens)
		m.metricsCard.CacheWriteTokens = int(lifetime.CacheWriteTokens)
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

	// Populate history panel from the 5 most recent DISTINCT tasks. A single task
	// can have many execution rows (retries); capping on raw rows lets one retried
	// task fill the whole window and hide the rest of the history — after
	// groupedHistory dedups by task ID a 4×-retried task collapses the panel to a
	// couple of entries. Cap on task_id instead so 5 real tasks always show.
	for _, exec := range firstNDistinctByTask(executions, 5) {
		completedAt := exec.CreatedAt
		if exec.CompletedAt != nil {
			completedAt = *exec.CompletedAt
		}
		// GH-3849: fetch the stage timeline once here (hydrate runs once per
		// process start, not per render frame) and cache the derived strip on
		// the CompletedTask so View() never hits the store.
		events, err := m.store.ListExecutionEvents(exec.ID)
		if err != nil {
			slog.Warn("failed to load execution events", slog.Any("error", err), slog.String("execution_id", exec.ID))
		}
		// TASK-420/GH-4537: resolveHistoryStatus is the single resolver for
		// icon-status + stage label — see stage_strip.go.
		hs := resolveHistoryStatus(exec.Status, events)
		m.completedTasks = append(m.completedTasks, CompletedTask{
			ID:          exec.TaskID,
			Title:       exec.TaskTitle,
			Status:      hs.Status,
			Duration:    fmt.Sprintf("%dms", exec.DurationMs),
			CompletedAt: completedAt,
			PeakRSSMB:   exec.PeakRSSMB,
			Stage:       hs.Stage,
			PRUrl:       exec.PRUrl,
		})
	}

	// Compute cost per task
	if m.metricsCard.TotalTasks > 0 {
		m.metricsCard.CostPerTask = m.metricsCard.TotalCostUSD / float64(m.metricsCard.TotalTasks)
	}

	// Load sparkline history
	m.loadMetricsHistory()
}

// firstNDistinctByTask returns up to n executions, at most one per task_id,
// preserving input order. GetRecentExecutions is ordered created_at DESC, so the
// first row for each task is its latest execution. This keeps a task that retried
// several times (many rows) from consuming the whole history window — the panel
// dedups by task ID at render, so a raw-row cap otherwise collapses to 1-2 entries.
//
// Within a task's rows, the latest-with-non-empty-title row wins, falling back
// to the latest row if none carry a title. GH-4218 pattern: a task can retry
// several times with only one non-latest row carrying task_title (backfilled
// or set on an earlier attempt), so picking the bare-latest row can surface a
// blank title even though a resolved one exists among its retries.
func firstNDistinctByTask(execs []*memory.Execution, n int) []*memory.Execution {
	if n <= 0 {
		return nil
	}
	order := make([]string, 0, n)
	best := make(map[string]*memory.Execution, n)
	for _, e := range execs {
		cur, ok := best[e.TaskID]
		if !ok {
			if len(order) >= n {
				continue
			}
			order = append(order, e.TaskID)
			best[e.TaskID] = e
			continue
		}
		if cur.TaskTitle == "" && e.TaskTitle != "" {
			best[e.TaskID] = e
		}
	}
	out := make([]*memory.Execution, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	return out
}

// lastNDistinctByTask returns up to n tasks, at most one per task ID, keeping
// each task's most recent entry. Unlike firstNDistinctByTask, tasks arrive in
// chronological (oldest-first) append order here, so "most recent" is the
// last occurrence, not the first — same GH-4100 crowding fix, opposite input
// order. Output preserves chronological order (oldest first).
func lastNDistinctByTask(tasks []CompletedTask, n int) []CompletedTask {
	if n <= 0 {
		return nil
	}
	seen := make(map[string]bool, n)
	out := make([]CompletedTask, 0, n)
	for i := len(tasks) - 1; i >= 0 && len(out) < n; i-- {
		t := tasks[i]
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
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
	m.metricsCard.CachedTokenHistory = make([]int64, 7)
	m.metricsCard.CostHistory = make([]float64, 7)
	m.metricsCard.TaskHistory = make([]int, 7)
	m.metricsCard.SuccessHistory = make([]int, 7)
	m.metricsCard.FailedHistory = make([]int, 7)
	for i := 0; i < 7; i++ {
		day := now.AddDate(0, 0, -6+i).Format("2006-01-02")
		if dm, ok := byDate[day]; ok {
			m.metricsCard.TokenHistory[i] = dm.TotalTokens
			m.metricsCard.CachedTokenHistory[i] = dm.CacheReadTokens + dm.CacheWriteTokens
			m.metricsCard.CostHistory[i] = dm.TotalCostUSD
			m.metricsCard.TaskHistory[i] = dm.ExecutionCount
			m.metricsCard.SuccessHistory[i] = dm.SuccessCount
			m.metricsCard.FailedHistory[i] = dm.FailedCount
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
		focus:          panelQueue,
		gitGraphMode:   GitGraphVisible,
		browserOpener:  openBrowser,
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

// AdapterLegendForTest exposes the queue-panel adapter legend for the same
// cross-package wiring tests — adapter chips moved out of the banner into
// the queue border legend in the grom redesign (TASK-390).
func (m Model) AdapterLegendForTest() string { return buildAdapterLegend(m.bannerAdapters) }

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
	if m.gitGraphMode != GitGraphHidden {
		// TASK-399: git graph is visible by default — paint it on the first
		// frame instead of waiting for a manual 'g' toggle.
		cmds = append(cmds, refreshGitGraphCmd(m.projectPath), gitRefreshTickCmd())
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
		for _, exec := range firstNDistinctByTask(executions, 5) {
			completedAt := exec.CreatedAt
			if exec.CompletedAt != nil {
				completedAt = *exec.CompletedAt
			}
			// GH-3849: fetched once per periodic refresh (every 5th tick), not
			// per render frame — View() reads the cached CompletedTask.Stage.
			events, err := store.ListExecutionEvents(exec.ID)
			if err != nil {
				slog.Warn("store refresh: failed to load execution events", slog.Any("error", err), slog.String("execution_id", exec.ID))
			}
			// TASK-420/GH-4537: resolveHistoryStatus is the single resolver for
			// icon-status + stage label — see stage_strip.go.
			hs := resolveHistoryStatus(exec.Status, events)
			msg.completedTasks = append(msg.completedTasks, CompletedTask{
				ID:          exec.TaskID,
				Title:       exec.TaskTitle,
				Status:      hs.Status,
				Duration:    fmt.Sprintf("%dms", exec.DurationMs),
				CompletedAt: completedAt,
				PeakRSSMB:   exec.PeakRSSMB,
				Stage:       hs.Stage,
				PRUrl:       exec.PRUrl,
			})
		}

		lifetime, err := store.GetLifetimeTokens(projectPath)
		if err != nil {
			slog.Warn("store refresh: failed to load lifetime tokens", slog.Any("error", err))
		} else {
			msg.metricsCard.TotalTokens = int(lifetime.TotalTokens)
			msg.metricsCard.InputTokens = int(lifetime.InputTokens)
			msg.metricsCard.OutputTokens = int(lifetime.OutputTokens)
			msg.metricsCard.CacheReadTokens = int(lifetime.CacheReadTokens)
			msg.metricsCard.CacheWriteTokens = int(lifetime.CacheWriteTokens)
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
		if m.zoomed {
			return m.handleZoomedKey(msg)
		}
		return m.handleGridKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, tea.ClearScreen // GH-1249: Terminal resized → full repaint

	case tickMsg:
		m.sparklineTick = !m.sparklineTick
		m.dbSyncTick++
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
		// TASK-399: zoomed logs shows up to 1000 lines (grid mode still tails
		// a short window via renderLogs' capacity, independent of this cap).
		if len(m.logs) > 1000 {
			m.logs = m.logs[1:]
		}
		if m.zoomed && m.focus == panelLogs && m.zoomLogsFollow {
			m.zoomScroll = m.logsMaxScroll()
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
		m.completedTasks = lastNDistinctByTask(m.completedTasks, 5)

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
		// TASK-399: keep the zoomed history list in sync with the periodic
		// DB re-sync while it's open, same as the grid-mode panel.
		var zoomCmd tea.Cmd
		if m.zoomed && m.focus == panelHistory {
			zoomCmd = historyZoomCmd(m.store, m.metricsScopePath)
		}
		if len(m.completedTasks) != prevLen {
			return m, tea.Batch(zoomCmd, tea.ClearScreen)
		}
		if zoomCmd != nil {
			return m, zoomCmd
		}

	case historyZoomMsg:
		m.zoomHistory = msg.tasks

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

	var result string
	if m.zoomed {
		result = m.renderZoomed()
	} else {
		result = m.renderGrid()
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

// renderBanner returns the grom-style one-line header — the exact grammar of
// the grom gallery header (bold accent wordmark, dim ·-separated segments),
// with live status right-aligned:
//
//	pilot ● · v2.233.0 · stage · opus/sonnet           up 4m · 16:36 utc
//
// The dot after the wordmark is the daemon liveness mark; it pulses with the
// animation tick. Adapter status lives in the queue panel border legend
// (buildAdapterLegend), not in the header.
func (m Model) renderBanner() string {
	tw := m.effectivePanelTotalWidth()

	// Strip dev suffix (e.g. "-4-g14764db1-dirty") so the banner shows the
	// clean release version.
	ver := m.version
	if idx := strings.Index(ver, "-"); idx > 0 {
		ver = ver[:idx]
	}
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}

	dot := daemonDotBright.Render("●")
	if !m.sparklineTick {
		dot = daemonDotDim.Render("●")
	}

	sep := dimStyle.Render(" · ")
	right := ""
	if !m.startTime.IsZero() {
		right = dimStyle.Render("up ") + labelStyle.Render(formatDurationShort(time.Since(m.startTime))) + sep
	}
	right += dimStyle.Render(time.Now().UTC().Format("15:04")+" utc") + " "

	// Identity segments append only while they fit next to the right cluster:
	// narrow terminals drop env then model — never the wordmark/version.
	budget := tw - lipgloss.Width(right) - 1
	left := gromTheme.AccentStyle().Bold(true).Render(" pilot") + " " + dot + sep + labelStyle.Render(ver)
	var extras []string
	if m.envName != "" {
		extras = append(extras, statusRunningStyle.Render(strings.ToLower(m.envName)))
	}
	if m.modelStack != "" {
		extras = append(extras, labelStyle.Render(strings.ToLower(m.modelStack)))
	}
	for _, seg := range extras {
		if lipgloss.Width(left)+lipgloss.Width(sep)+lipgloss.Width(seg) > budget {
			break
		}
		left += sep + seg
	}

	return padLeftRightLine(tw, left, right)
}

// buildAdapterLegend renders adapter status in the grom border-legend grammar
// (dot before label, two-space separated): ● gh  ● tg  ○ 6 idle.
// Active adapters are named; idle ones (configured but not running) collapse
// to a count — they are config facts, not live status. The daemon itself has
// no entry: its liveness mark is the wordmark dot in renderBanner.
func buildAdapterLegend(adapters []AdapterStatus) string {
	segs := make([]string, 0, len(adapters)+1)
	idle := 0
	for _, a := range adapters {
		if !a.Active {
			idle++
			continue
		}
		segs = append(segs, statusRunningStyle.Render("●")+" "+dimStyle.Render(strings.ToLower(a.Name)))
	}
	if idle > 0 {
		segs = append(segs, dimStyle.Render(fmt.Sprintf("○ %d idle", idle)))
	}
	return strings.Join(segs, "  ")
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

// renderChromeHeader renders the non-navigable header chrome: banner, update
// notice, metrics cards, and eval stats. This sits above the spatial grid
// (TASK-399) and is never a focus target — computeLayout knows nothing
// about it, so its height must be subtracted before computing panel rects
// (see gridAvailHeight).
func (m Model) renderChromeHeader() string {
	var b strings.Builder

	// Header: bordered banner frame (GH-2455 / GH-2459).
	// The ASCII logo is shown only during the splash; steady-state dashboard
	// uses the compact banner frame to keep header real-estate small.
	if m.showBanner {
		b.WriteString(m.renderBanner())
		b.WriteString("\n")
	}

	// Stale/archived ledger warning (GH-4569) — shown above the update
	// notification so a frozen ledger is the first thing an operator sees.
	if m.stalenessBanner != "" {
		b.WriteString(m.renderStalenessBanner())
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

	// Eval stats
	if evalPanel := m.renderEvalStats(); evalPanel != "" {
		b.WriteString(evalPanel)
		b.WriteString("\n")
	}

	return b.String()
}

// gridAvailHeight returns the terminal height remaining for the spatial grid
// (queue/autopilot/history/logs/git) after the chrome header and the help
// footer's reserved line.
func (m Model) gridAvailHeight() int {
	if m.height <= 0 {
		return 0
	}
	h := m.height - lineCount(m.renderChromeHeader())
	if h < 0 {
		h = 0
	}
	return h
}

// lineCount returns the number of lines s renders as (1 for "", N+1 for N
// embedded newlines) — used to measure panel/header heights for layout.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// buildGridPanels renders each navigable left-column panel's content and
// measures its height, for feeding into computeLayout. Logs gets the same
// flex-vs-content-sized treatment as the pre-wiring renderDashboard: it
// stretches to fill remaining vertical space unless the layout is stacked
// (narrow terminal, git graph below), where it stays content-sized so the
// graph keeps its room.
func (m Model) buildGridPanels() (layoutHeights, map[panelID]string) {
	if m.autopilotPanel != nil {
		m.autopilotPanel.panelWidth = m.effectivePanelTotalWidth()
		m.autopilotPanel.focused = m.focus == panelAutopilot
	}

	contents := map[panelID]string{
		panelQueue:     m.renderTasks(),
		panelAutopilot: m.autopilotPanel.View(),
		panelHistory:   m.renderHistory(),
	}
	var h layoutHeights
	h.Queue = lineCount(contents[panelQueue])
	h.Autopilot = lineCount(contents[panelAutopilot])
	h.History = lineCount(contents[panelHistory])

	if m.showLogs {
		flex := 0
		if !m.isStackedMode() {
			availH := m.gridAvailHeight()
			used := h.Queue + h.Autopilot + h.History
			if remaining := availH - used; remaining >= logsFlexMinHeight {
				flex = remaining
			}
		}
		contents[panelLogs] = m.renderLogs(flex)
		h.Logs = lineCount(contents[panelLogs])
	}

	return h, contents
}

// computeRects returns the current frame's panel rects (queue/autopilot/
// history/logs/git), indexed identically to panelRegistry — feed directly
// to focusMove.
func (m Model) computeRects() []Rect {
	heights, _ := m.buildGridPanels()
	return computeLayout(m.width, m.gridAvailHeight(), heights, m.gitGraphMode != GitGraphHidden)
}

// navigablePanels returns the panelIDs currently eligible for spatial focus
// — panelGit is excluded when hidden, panelLogs when toggled off.
func (m Model) navigablePanels() []panelID {
	ids := make([]panelID, 0, len(panelRegistry))
	for _, p := range panelRegistry {
		if p.ID == panelGit && m.gitGraphMode == GitGraphHidden {
			continue
		}
		if p.ID == panelLogs && !m.showLogs {
			continue
		}
		ids = append(ids, p.ID)
	}
	return ids
}

// moveFocus updates m.focus to the panel spatially nearest in direction dir
// ('h'/'j'/'k'/'l'), restricted to currently-navigable panels.
func (m *Model) moveFocus(dir byte) {
	ids := m.navigablePanels()
	if len(ids) == 0 {
		return
	}
	all := m.computeRects()
	rects := make([]Rect, len(ids))
	cur := 0
	for i, id := range ids {
		rects[i] = all[panelIndex(id)]
		if id == m.focus {
			cur = i
		}
	}
	next := focusMove(rects, cur, dir)
	if next >= 0 && next < len(ids) {
		m.focus = ids[next]
	}
}

// renderDashboard builds the left-side dashboard column (all existing
// panels, no git graph) — kept as the pure left-column renderer for
// tests/callers that only want that piece.
func (m Model) renderDashboard() string {
	_, contents := m.buildGridPanels()

	var b strings.Builder
	b.WriteString(m.renderChromeHeader())
	b.WriteString(contents[panelQueue])
	b.WriteString("\n")
	b.WriteString(contents[panelAutopilot])
	b.WriteString("\n")
	b.WriteString(contents[panelHistory])
	b.WriteString("\n")
	if m.showLogs {
		b.WriteString(contents[panelLogs])
		b.WriteString("\n")
	}
	return b.String()
}

// renderGrid composes the full non-zoomed frame: chrome header, the left
// column (queue/autopilot/history/logs) placed via computeLayout, and the
// git graph panel beside or beneath it. Replaces the three ad-hoc View()
// layout branches (git hidden / stacked / side-by-side) with one geometry
// pass (TASK-399).
func (m Model) renderGrid() string {
	header := m.renderChromeHeader()
	heights, contents := m.buildGridPanels()
	gitVisible := m.gitGraphMode != GitGraphHidden
	rects := computeLayout(m.width, m.gridAvailHeight(), heights, gitVisible)

	var left []string
	for _, id := range []panelID{panelQueue, panelAutopilot, panelHistory} {
		idx := panelIndex(id)
		left = append(left, safeRender(panelRegistry[idx], rects[idx], contents[id]))
	}
	if m.showLogs {
		idx := panelIndex(panelLogs)
		left = append(left, safeRender(panelRegistry[idx], rects[idx], contents[panelLogs]))
	}
	// dashboardColumn includes the chrome header so side-by-side joining
	// (below) matches every row — including the banner/metrics rows —
	// against a git-panel row, keeping every composed line exactly m.width.
	dashboardColumn := header + strings.Join(left, "\n")

	if !gitVisible {
		return dashboardColumn
	}

	gi := panelIndex(panelGit)
	gr := rects[gi]
	sideBySide := gr.X > 0
	gitHeight := gr.H
	if sideBySide {
		// computeLayout's git height (gr.H) only spans the left-column panel
		// stack, not the chrome header above it — stretch to the full
		// dashboard column height so the join below covers every row.
		gitHeight = lineCount(dashboardColumn)
	}
	gitPanel := m.renderGitGraph(gr.W, gitHeight)
	if gitPanel == "" {
		return dashboardColumn
	}
	if sideBySide {
		return lipgloss.JoinHorizontal(lipgloss.Top, dashboardColumn, " ", gitPanel)
	}
	// Stacked below.
	return dashboardColumn + "\n" + gitPanel
}

// renderHelp returns a context-aware help footer that fits within
// effectivePanelTotalWidth. Grid mode shows spatial-navigation keys; zoomed
// mode shows the list/scroll keys for the zoomed panel.
func (m Model) renderHelp() string {
	var parts []string
	if m.zoomed {
		switch m.focus {
		case panelQueue, panelAutopilot, panelHistory:
			parts = []string{"q: quit", "esc: back", "j/k: move", "enter/o: open"}
		default:
			parts = []string{"q: quit", "esc: back", "j/k: scroll", "g/G: top/bottom"}
		}
	} else {
		parts = []string{"q: quit", "hjkl: focus", "enter: zoom", "g: graph", "L: logs", "b: banner"}
	}
	help := strings.Join(parts, "  ")
	tw := m.effectivePanelTotalWidth()
	if len(help) > tw {
		help = help[:tw-3] + "..."
	}
	return helpStyle.Render(help)
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

// --- Stat card renderers (grom demo gallery row-1 style) ---

// renderTokenCard renders the tokens stat card: grand total (input + output +
// cache read/write), cached share detail, 7-day stacked braille trend —
// dim accent base = cached volume, bright accent cap = fresh input+output.
// The detail line doubles as the color key ("cached" in the base tone).
func (m Model) renderTokenCard(cw int) string {
	grandTotal := m.metricsCard.TotalTokens + m.metricsCard.CacheReadTokens + m.metricsCard.CacheWriteTokens
	value := boldLabelStyle.Render(formatCompact(grandTotal))
	cachedTone := render.Dim(gromTheme.Accent, 0.45)
	cachedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(cachedTone))
	detail := cachedStyle.Render(fmt.Sprintf("%s cached", formatCompact(m.metricsCard.CacheReadTokens)))

	cached := make([]float64, len(m.metricsCard.CachedTokenHistory))
	for i, v := range m.metricsCard.CachedTokenHistory {
		cached[i] = float64(v)
	}
	fresh := make([]float64, len(m.metricsCard.TokenHistory))
	for i, v := range m.metricsCard.TokenHistory {
		fresh[i] = float64(v)
	}
	return buildStatCardStacked("tokens", value, detail,
		[][]float64{cached, fresh}, []string{cachedTone, gromTheme.Accent}, cw)
}

// renderCostCard renders the cumulative-cost stat card with 7-day cost trend.
func (m Model) renderCostCard(cw int) string {
	value := costStyle.Render(fmt.Sprintf("$%.2f", m.metricsCard.TotalCostUSD))
	detail := dimStyle.Render(fmt.Sprintf("~$%.2f/task", m.metricsCard.CostPerTask))
	return buildStatCard("cost", value, detail, m.metricsCard.CostHistory, gromTheme.Success, cw)
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

// renderTaskCard renders the queue stat card with the given card width.
// Value counts only active m.tasks entries (queued/pending/running), not
// terminal ones — len(m.tasks) is wrong because Monitor never evicts
// completed/failed/no-op tasks, so it grows with lifetime totals since
// daemon start instead of reflecting current queue depth (GH-4617).
// TASK-358: "failed" counts genuine failures only. Non-failure terminal
// outcomes (no-op / infra / …) are shown as a muted suffix so the numbers
// reconcile and a no-op is no longer miscounted as a failure. Only append the
// suffix when the whole line fits the card — otherwise show just the headline.
// Truncating a styled multi-segment string blanks the line (the empty "failed"
// row seen on v2.166.10).
func (m Model) renderTaskCard(cw int) string {
	ciw := cw - 4
	activeCount := 0
	for _, t := range m.tasks {
		switch t.Status {
		case QueueStatusQueued, QueueStatusPending, QueueStatusRunning:
			activeCount++
		}
	}
	value := boldLabelStyle.Render(fmt.Sprintf("%d", activeCount))
	detail := statusCompletedStyle.Render(fmt.Sprintf("✓ %d", m.metricsCard.Succeeded)) +
		"  " + statusFailedStyle.Render(fmt.Sprintf("✗ %d", m.metricsCard.Failed))
	if suffix := nonFailureSuffix(m.metricsCard); suffix != "" {
		withSuffix := detail + statusPendingStyle.Render(suffix)
		if lipgloss.Width(withSuffix) <= ciw {
			detail = withSuffix
		}
	}

	// Stacked outcome trend: sage base = succeeded/day, rose cap = failed/day,
	// matching the ✓/✗ colors in the detail line (which acts as the key).
	succeeded := make([]float64, len(m.metricsCard.SuccessHistory))
	for i, v := range m.metricsCard.SuccessHistory {
		succeeded[i] = float64(v)
	}
	failed := make([]float64, len(m.metricsCard.FailedHistory))
	for i, v := range m.metricsCard.FailedHistory {
		failed[i] = float64(v)
	}
	return buildStatCardStacked("queue depth", value, detail,
		[][]float64{succeeded, failed}, []string{gromTheme.Success, gromTheme.Error}, cw)
}

// renderMetricsCards renders the three stat cards side by side with no gaps,
// like the grom demo gallery row 1. The last card absorbs the remainder.
func (m Model) renderMetricsCards() string {
	epw := m.effectivePanelTotalWidth()
	cw := epw / 3
	lastW := epw - 2*cw
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderTokenCard(cw),
		m.renderCostCard(cw),
		m.renderTaskCard(lastW))
}

// taskStatePriority returns sort priority for task states (lower = higher in list).
func taskStatePriority(status QueueStatus) int {
	switch status {
	case QueueStatusDone:
		return 0
	case QueueStatusRunning:
		return 1
	case QueueStatusQueued:
		return 2
	case QueueStatusPending:
		return 3
	case QueueStatusFailed:
		return 4
	default:
		return 5
	}
}

// renderTasks renders the tasks list with state-aware sorting and rendering.
func (m Model) renderTasks() string {
	var content strings.Builder

	iw := m.effectivePanelTotalWidth() - 4

	if len(m.tasks) == 0 {
		content.WriteString("  No tasks in queue")
	} else {
		sorted := m.sortedTasks()
		idW := queueIDColumnWidth(sorted)

		queueIdx := 0 // position counter for queued items' "#N" label
		for i, task := range sorted {
			if i > 0 {
				content.WriteString("\n")
			}
			offset := 0
			if task.Status == QueueStatusQueued {
				offset = queueIdx
				queueIdx++
			}
			content.WriteString(m.renderTask(task, i == m.selectedTask, offset, iw, idW))
		}
	}

	// Legend in the top border (grom style): ┤ ● 2 running  ● gh  ● tg  ○ 6 idle ├
	// — running count first, then intake adapter status (buildAdapterLegend).
	running := 0
	for _, t := range m.tasks {
		if t.Status == QueueStatusRunning {
			running++
		}
	}
	segs := make([]string, 0, 2)
	if running > 0 {
		segs = append(segs, statusRunningStyle.Render("●")+" "+dimStyle.Render(fmt.Sprintf("%d running", running)))
	}
	if leg := buildAdapterLegend(m.bannerAdapters); leg != "" {
		segs = append(segs, leg)
	}
	info := strings.Join(segs, "  ")

	chrome := panelChrome
	if m.focus == panelQueue {
		chrome = focusChrome
	}
	return renderPanelStyled("queue", info, content.String(), m.effectivePanelTotalWidth(), chrome)
}

// sortedTasks returns m.tasks sorted the same way renderTasks displays them
// (state priority, then ID) — shared with the zoomed queue view so selection
// indices refer to the same order the user sees.
func (m Model) sortedTasks() []TaskDisplay {
	sorted := make([]TaskDisplay, len(m.tasks))
	copy(sorted, m.tasks)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := taskStatePriority(sorted[i].Status), taskStatePriority(sorted[j].Status)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// queueIDColumnWidth computes the shared ID column width for a queue render
// pass: wide enough for the longest visible ID, capped so a pathological ID
// (e.g. a deep sub-issue chain like "GH-4328-12") can't blow out row
// alignment. GH-4338.
func queueIDColumnWidth(tasks []TaskDisplay) int {
	longest := 0
	for _, t := range tasks {
		if w := lipgloss.Width(t.ID); w > longest {
			longest = w
		}
	}
	idW := longest
	if idW < 7 {
		idW = 7
	}
	if idW > 12 {
		idW = 12
	}
	return idW
}

// resolveParentTitle resolves the parent title and i/n position for a
// sub-issue ID like "GH-4328-1" (parent "GH-4328", position 1) from the
// currently known tasks/completed tasks. Used by the queue row title
// fallback (GH-4338) so sub-issue rows with an unhydrated title show parent
// context instead of duplicating the bare ID into the title column.
func (m Model) resolveParentTitle(id string) (title string, pos, total int, ok bool) {
	idx := strings.LastIndex(id, "-")
	if idx <= 0 || idx == len(id)-1 {
		return "", 0, 0, false
	}
	p, err := strconv.Atoi(id[idx+1:])
	if err != nil || p <= 0 {
		return "", 0, 0, false
	}
	parentID := id[:idx]

	for _, t := range m.tasks {
		if t.ID == parentID && t.Title != "" && t.Title != t.ID {
			title = t.Title
			break
		}
	}
	if title == "" {
		for _, t := range m.completedTasks {
			if t.ID == parentID && t.Title != "" && t.Title != t.ID {
				title = t.Title
				break
			}
		}
	}
	if title == "" {
		return "", 0, 0, false
	}

	total = p
	for _, t := range m.tasks {
		if strings.HasPrefix(t.ID, parentID+"-") {
			if n, err := strconv.Atoi(t.ID[len(parentID)+1:]); err == nil && n > total {
				total = n
			}
		}
	}
	return title, p, total, true
}

// renderTask renders a single task row with state-aware icons, bars, and meta.
//
// Layout (width-aware, minimum 65 inner chars):
//
//	sel(2) + icon+state(9) + space(1) + id(flex, 7-12) + space(1) + title(flex, min 20) + gap(2) + bar(16) + gap(1) + meta(5)
//
// Everything but the title and id is fixed; the title expands to fill iw
// (GH-3970) and the id column widens to fit the widest visible ID (GH-4338),
// mirroring how renderStandaloneLine flexes titles in HISTORY.
func (m Model) renderTask(task TaskDisplay, selected bool, queueOffset int, iw int, idW int) string {
	const nonFlexCols = 37             // fixed columns excluding id and title (see layout above)
	fixedCols := nonFlexCols + idW + 1 // +1 reserve so the row never exceeds iw at the min title width
	titleW := iw - fixedCols
	if titleW < 20 {
		titleW = 20
	}
	// Hard ceiling: a wide id column can make the 20-char title floor push
	// the row past iw (mem-024 — width math must never let a row overflow
	// its panel). When that happens, shrink the title below the floor
	// rather than overflow.
	if maxTitleW := iw - nonFlexCols - idW; titleW > maxTitleW {
		titleW = maxTitleW
	}
	if titleW < 1 {
		titleW = 1
	}
	var icon, stateLabel, meta string
	var iconStyle lipgloss.Style

	switch task.Status {
	case QueueStatusDone:
		icon = "✓"
		stateLabel = "done"
		meta = extractPRNumber(task.PRURL)
		iconStyle = statusDoneStyle
	case QueueStatusRunning:
		icon = "●"
		stateLabel = "running"
		meta = fmt.Sprintf("%4d%%", task.Progress)
		iconStyle = statusRunningStyle
	case QueueStatusQueued:
		icon = "◌"
		stateLabel = "queued"
		meta = fmt.Sprintf("  #%d", queueOffset+1)
		iconStyle = statusQueuedStyle
	case QueueStatusFailed:
		icon = "✗"
		stateLabel = "failed"
		meta = truncateVisual(task.Phase, 5)
		iconStyle = statusFailedStyle
	case QueueStatusNoOp:
		// GH-4490 subtask 2: mirrors statusIconStyle's existing "no_op" glyph
		// (TASK-358) — a no-commit run is a terminal, non-failure outcome, so
		// it gets the same subdued pending style rather than the red failed
		// styling.
		icon = "○"
		stateLabel = "no_op"
		meta = truncateVisual(task.Phase, 5)
		iconStyle = statusPendingStyle
	default: // pending
		icon = "·"
		stateLabel = "pending"
		meta = ""
		iconStyle = statusPendingStyle
	}

	// Pulse the running icon on animation tick
	renderedIcon := iconStyle.Render(icon)
	if task.Status == QueueStatusRunning && !m.sparklineTick {
		renderedIcon = dimStyle.Render(icon)
	}

	// Build icon+state column (8 chars visual: "● running" or "✓ done   ")
	iconState := renderedIcon + " " + iconStyle.Render(fmt.Sprintf("%-7s", stateLabel))

	// Selector
	selector := "  "
	if selected {
		selector = dimStyle.Render("▸") + " "
	}

	// Progress bar: grom segment meter (■■■□□), 16 cells — same grammar as
	// renderEpicProgressBar/stageMeter, replacing the old [████░░]/[▓▒░░]
	// bracket bars.
	const barWidth = 16
	var progressBar string
	switch task.Status {
	case QueueStatusDone:
		progressBar = segmentMeter(1, 1, barWidth, gromTheme.Success)
	case QueueStatusRunning:
		progressBar = segmentMeter(task.Progress, 100, barWidth, gromTheme.Accent)
	case QueueStatusFailed:
		progressBar = segmentMeter(task.Progress, 100, barWidth, gromTheme.Error)
	default: // queued, pending — no known progress yet
		progressBar = meterTrack.Render(strings.Repeat("■", barWidth))
	}

	// Render meta with state-appropriate color
	renderedMeta := iconStyle.Render(fmt.Sprintf("%5s", meta))

	// Title fallback (GH-4338): sub-issue rows can hydrate with an empty
	// title or one that just echoes the ID; render parent context instead of
	// duplicating the bare ID into the title column.
	displayTitle := task.Title
	if displayTitle == "" || displayTitle == task.ID {
		if parentTitle, pos, total, ok := m.resolveParentTitle(task.ID); ok {
			displayTitle = fmt.Sprintf("%s · %d/%d", parentTitle, pos, total)
		} else {
			displayTitle = dimStyle.Render(task.ID)
		}
	}

	// Left side: selector + icon+state + id + title
	// Right side: bar + meta (right-aligned)
	return fmt.Sprintf("%s%s %s %s  %s %s",
		selector,
		iconState,
		padOrTruncate(task.ID, idW),
		padOrTruncate(truncateVisual(displayTitle, titleW), titleW),
		progressBar,
		renderedMeta,
	)
}

// renderProgressBar renders a standard bracketed progress bar, used by the
// upgrade-notification panel (the queue panel uses the grom segment meter).
func (m Model) renderProgressBar(progress int, width int) string {
	filled := progress * width / 100
	empty := width - filled

	bar := progressBarStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", empty))

	return "[" + bar + "]"
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

// renderEpicProgressBar renders a compact epic progress meter (■■□□) in the
// grom segment-meter style, innerWidth cells wide.
func renderEpicProgressBar(done, total, innerWidth int) string {
	return segmentMeter(done, total, innerWidth, gromTheme.Accent)
}

// renderEvalStats renders a compact eval stats panel showing latest pass@1 rate
// with a trend indicator and optional regression warning.
func (m Model) renderEvalStats() string {
	if m.store == nil {
		return ""
	}

	tw := m.effectivePanelTotalWidth()

	tasks, err := m.store.ListEvalTasks(memory.EvalTaskFilter{ProjectPath: m.defaultProjectPath, Limit: 200})
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

	info := ""
	if m.metricsScopePath != "" {
		info = dimStyle.Render("global")
	}
	return renderPanelInfo("eval", info, line, tw)
}

// renderHistory renders completed tasks history with epic-aware grouping.
// Active epics show expanded with sub-issue tree; completed epics collapse to one line.
func (m Model) renderHistory() string {
	var content strings.Builder
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4
	chrome := panelChrome
	if m.focus == panelHistory {
		chrome = focusChrome
	}

	if len(m.completedTasks) == 0 {
		content.WriteString("  No completed tasks yet")
		return renderPanelStyled("HISTORY", "", content.String(), tw, chrome)
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

	return renderPanelStyled("HISTORY", "", content.String(), tw, chrome)
}

// stageLabelWidth is the fixed column for the stage name after the ladder
// meter ("released", "ci_failed", …) so history rows stay aligned.
const stageLabelWidth = 10

// renderStandaloneLine renders a standalone (non-epic) task line with fixed
// columns so rows align regardless of pipeline state:
//
//	✓ GH-4018  Aggregated scope notes…  ■■■■■■■ released    40m ago 2.0G
//
// indent(2) + glyph(1) + sp(1) + id(7) + sp(2) + title(flex) + sp(2) +
// ladder(7) + sp(1) + stage(10) + sp(2) + time(8) + rss(5) = iw
// The RSS column (GH-3028, "4.2G") is fixed-width — blank when the sampler
// had no data — so rows align regardless. GH-3849: the ladder is the 7-rung
// pipeline segment meter built from the cached StageInfo; rows without stage
// evidence show a dim track.
func renderStandaloneLine(task CompletedTask, iw int) string {
	icon, style := statusIconStyle(task.Status)

	label := "–"
	if task.Stage.Known {
		label = task.Stage.Label
	}

	rssStr := "     "
	if task.PeakRSSMB > 0 {
		rssStr = fmt.Sprintf(" %4s", formatRSSMB(task.PeakRSSMB))
	}

	titleWidth := iw - 2 - 1 - 1 - 7 - 2 - 2 - stageLadderTotal - 1 - stageLabelWidth - 2 - 8 - len(rssStr)
	if titleWidth < 10 {
		titleWidth = 10
	}

	return fmt.Sprintf("  %s %-7s  %s  %s %s  %s%s",
		style.Render(icon),
		task.ID,
		padOrTruncate(task.Title, titleWidth),
		stageMeter(task.Stage, stageLadderTotal),
		dimStyle.Render(padOrTruncate(label, stageLabelWidth)),
		dimStyle.Render(fmt.Sprintf("%8s", formatTimeAgo(task.CompletedAt))),
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

// renderActiveEpicLine renders the parent line for an active epic:
//
//	● GH-491  Enable decomposition by default  ■■□□ 2/4    3m
//
// indent(2) + glyph(1) + sp(1) + id(7) + sp(2) + title(flex) + sp(1) +
// meter(4) + sp(1) + counts(5) + sp(1) + time(5) = iw
func renderActiveEpicLine(task CompletedTask, iw int) string {
	const progressInnerWidth = 4

	bar := renderEpicProgressBar(task.DoneSubs, task.TotalSubs, progressInnerWidth)
	counts := fmt.Sprintf("%d/%d", task.DoneSubs, task.TotalSubs)
	timeStr := task.Duration
	if timeStr == "" {
		timeStr = formatTimeAgo(task.CompletedAt)
	}

	rightPart := fmt.Sprintf(" %s %-5s %5s", bar, counts, timeStr)
	rightLen := lipgloss.Width(rightPart) // meter carries ANSI styling

	// Title gets whatever remains
	tWidth := iw - 2 - 1 - 1 - 7 - 2 - rightLen
	if tWidth < 10 {
		tWidth = 10
	}

	titleStr := padOrTruncate(task.Title, tWidth)

	return fmt.Sprintf("  %s %-7s  %s%s",
		statusRunningStyle.Render("●"),
		task.ID,
		titleStr,
		rightPart,
	)
}

// renderCompletedEpicLine renders a collapsed completed epic.
func renderCompletedEpicLine(task CompletedTask, iw int) string {
	counts := fmt.Sprintf("%d/%d", task.DoneSubs, task.TotalSubs)
	timeAgoStr := formatTimeAgo(task.CompletedAt)

	// Right part: " N/N    Xm ago"
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

	return fmt.Sprintf("    %s %-7s  %s  %s",
		style.Render(icon),
		task.ID,
		titleStr,
		dimStyle.Render(fmt.Sprintf("%8s", timeStr)),
	)
}

// statusIconStyle returns the glyph and style for a task status (top-level
// tasks) — the design-system vocabulary from the grom_chrome doc block.
func statusIconStyle(status string) (string, lipgloss.Style) {
	switch status {
	case "success":
		return "✓", statusCompletedStyle
	case "failed":
		return "✗", statusFailedStyle
	case "stalled":
		return "✗", statusFailedStyle
	case "no_op":
		return "○", statusPendingStyle // TASK-358: no-change run, not a failure
	case "declined":
		return "○", statusPendingStyle // TASK-358: agent declined as unactionable
	case "rate_limited":
		return "⟲", statusPendingStyle // TASK-358: provider quota hit, transient
	case "infra":
		return "!", statusPendingStyle // TASK-358: plumbing/resource failure, not the work
	case "skipped":
		return "·", statusPendingStyle // TASK-358: never ran / cancelled
	case "cancelled":
		// TASK-420/GH-4537: explicit case rather than falling through to the
		// default glyph — cancelled is a real, muted terminal outcome (see
		// mutedOutcomes in stage_strip.go), not an unaccounted-for row.
		return "○", statusPendingStyle
	case "canceled":
		// GH-4678: the live single-L operator-cancel status (`pilot task
		// cancel`). Same treatment as "cancelled" above — a real, muted
		// terminal outcome, not an unaccounted-for row.
		return "○", statusPendingStyle
	case "running":
		return "●", statusRunningStyle
	case "pending":
		return "◌", statusPendingStyle
	default:
		return "·", statusPendingStyle
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

// logsFlexMinHeight is the smallest total panel height worth stretching to:
// 2 borders + 2 padding lines + 1 log line.
const logsFlexMinHeight = 5

// renderLogs renders the logs section. flexHeight > 0 stretches the panel to
// exactly that many terminal lines (log tail fills the space, blank-padded
// below); flexHeight <= 0 keeps the content-sized panel with a 10-line tail —
// used in stacked mode so the git graph below keeps its room.
func (m Model) renderLogs(flexHeight int) string {
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4
	w := iw - 4 // Account for indent (2 spaces each side)

	capacity := 10
	flex := flexHeight >= logsFlexMinHeight
	if flex {
		capacity = flexHeight - 4 // 2 borders + 2 padding lines
	}

	var lines []string
	if len(m.logs) == 0 {
		lines = append(lines, "  No logs yet")
	} else {
		start := len(m.logs) - capacity
		if start < 0 {
			start = 0
		}
		for _, log := range m.logs[start:] {
			lines = append(lines, "  "+truncateVisual(log, w))
		}
	}
	chrome := panelChrome
	if m.focus == panelLogs {
		chrome = focusChrome
	}
	if flex {
		for len(lines) < capacity {
			lines = append(lines, "")
		}
		padded := "\n" + strings.Join(lines, "\n") + "\n"
		return render.Panel("logs", padded, tw, flexHeight, chrome)
	}
	return renderPanelStyled("LOGS", "", strings.Join(lines, "\n"), tw, chrome)
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

// renderStalenessBanner renders the stale/archived-ledger warning card
// (GH-4569): a ledger DB that silently stopped being written to (wrong
// path, stale copy, retired archive) answers every query successfully with
// wrong data, so this uses the same loud amber chrome as the update
// notification.
func (m Model) renderStalenessBanner() string {
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4
	var content strings.Builder
	content.WriteString(formatPanelRow(m.stalenessBanner, "", iw))
	return renderPanelStyled("ledger stale", "", content.String(), tw, warnChrome)
}

// renderUpdateNotification renders the update notification panel (amber
// chrome; the "u: upgrade" hint sits in the top-border legend, grom style).
func (m Model) renderUpdateNotification() string {
	var content strings.Builder
	var title string
	var info string
	tw := m.effectivePanelTotalWidth()
	iw := tw - 4

	switch m.upgradeState {
	case UpgradeStateAvailable:
		title = "update"
		leftText := fmt.Sprintf("%s -> %s available", m.updateInfo.CurrentVersion, m.updateInfo.LatestVersion)
		content.WriteString(formatPanelRow(leftText, "", iw))
		info = dimStyle.Render("u: upgrade")

	case UpgradeStateInProgress:
		title = "upgrading"
		bar := m.renderProgressBar(m.upgradeProgress, 30)
		content.WriteString(fmt.Sprintf("  Installing %s... %s %d%%", m.updateInfo.LatestVersion, bar, m.upgradeProgress))
		if m.upgradeMessage != "" {
			content.WriteString("\n  " + m.upgradeMessage)
		}

	case UpgradeStateComplete:
		title = "upgraded"
		content.WriteString(fmt.Sprintf("  Upgrade to %s installed — restart Pilot manually to apply.", m.updateInfo.LatestVersion))

	case UpgradeStateFailed:
		title = "upgrade failed"
		if m.upgradeError != "" {
			content.WriteString("  " + m.upgradeError)
		} else {
			content.WriteString("  " + m.upgradeMessage)
		}
		// GH-3600: a failed upgrade means the old binary is still running —
		// say so explicitly and point at the durable log.
		content.WriteString(fmt.Sprintf("\n  Daemon still running %s — see ~/.pilot/logs/daemon.log", m.version))

	default:
		return ""
	}

	return renderPanelStyled(title, info, content.String(), tw, warnChrome)
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
