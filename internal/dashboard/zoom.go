package dashboard

// TASK-399: grid/zoomed key dispatch and zoomed-panel rendering. Consumes
// the merged grid.go/panels.go/zoomlist.go helpers (TASK-398/#4200) to wire
// spatial focus navigation and the "see everything" zoom view into the
// dashboard's Update/View loop.

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
)

// indexOfPanel returns the position of id in ids, or -1 if absent.
func indexOfPanel(ids []panelID, id panelID) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}

// clampInt clamps v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// minInt returns the smaller of a, b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// spliceSelector replaces a rendered row's leading 2-space indent with the
// zoomlist selector marker, matching renderTask's own selector treatment.
func spliceSelector(line string, selected bool) string {
	if len(line) < 2 {
		return line
	}
	return zoomListSelector(selected) + line[2:]
}

// zoomAutopilotIndex finds prNumber's position in prs, falling back to 0
// when not found (e.g. the PR just left the active set) and -1 when prs is
// empty.
func zoomAutopilotIndex(prs []*autopilot.PRState, prNumber int) int {
	for i, pr := range prs {
		if pr.PRNumber == prNumber {
			return i
		}
	}
	if len(prs) > 0 {
		return 0
	}
	return -1
}

// handleGridKey processes a key press while the dashboard is in grid
// (non-zoomed) mode: spatial focus movement, panel toggles, and entering
// zoom.
func (m Model) handleGridKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "b":
		m.showBanner = !m.showBanner
		return m, tea.ClearScreen // GH-1249: banner toggle changes height

	case "L":
		m.showLogs = !m.showLogs
		if !m.showLogs && m.focus == panelLogs {
			m.focus = panelQueue
		}
		return m, tea.ClearScreen // GH-1249: logs toggle changes height

	case "g":
		if m.gitGraphMode == GitGraphHidden {
			m.gitGraphMode = GitGraphVisible
			return m, tea.Batch(refreshGitGraphCmd(m.projectPath), gitRefreshTickCmd(), tea.ClearScreen)
		}
		m.gitGraphMode = GitGraphHidden
		if m.focus == panelGit {
			m.focus = panelQueue
		}
		return m, tea.ClearScreen

	case "tab":
		ids := m.navigablePanels()
		if len(ids) > 0 {
			cur := indexOfPanel(ids, m.focus)
			if cur < 0 {
				cur = 0
			}
			m.focus = ids[(cur+1)%len(ids)]
		}

	case "h", "left":
		m.moveFocus('h')
	case "l", "right":
		m.moveFocus('l')
	case "k", "up":
		m.moveFocus('k')
	case "j", "down":
		m.moveFocus('j')

	case "ctrl+d":
		if m.focus == panelGit && m.gitGraphState != nil {
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
		if m.focus == panelGit {
			viewportH := m.gitGraphViewportHeight()
			m.gitGraphScroll -= viewportH / 2
			if m.gitGraphScroll < 0 {
				m.gitGraphScroll = 0
			}
		}

	case "enter":
		m.zoomed = true
		m.zoomSel = 0
		m.zoomScroll = 0
		m.zoomLogsFollow = true
		switch m.focus {
		case panelHistory:
			return m, tea.Batch(historyZoomCmd(m.store, m.metricsScopePath), tea.ClearScreen)
		case panelAutopilot:
			if prs := m.autopilotPanel.sortedActivePRs(); len(prs) > 0 {
				m.zoomAutopilotPR = prs[0].PRNumber
			} else {
				m.zoomAutopilotPR = -1
			}
		case panelGit:
			m.zoomScroll = m.gitGraphScroll
		}
		return m, tea.ClearScreen

	case "u":
		// Trigger upgrade if update is available and not already upgrading
		if m.updateInfo != nil && m.upgradeState == UpgradeStateAvailable && m.upgradeCh != nil {
			m.upgradeState = UpgradeStateInProgress
			m.upgradeProgress = 0
			m.upgradeMessage = "Starting upgrade..."
			select {
			case m.upgradeCh <- struct{}{}:
			default:
			}
		}
	}

	return m, nil
}

// handleZoomedKey processes a key press while a panel is zoomed full-screen.
func (m Model) handleZoomedKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.zoomed = false
		return m, tea.ClearScreen
	case "j", "down":
		return m.zoomStep(1)
	case "k", "up":
		return m.zoomStep(-1)
	case "ctrl+d", "pgdown":
		return m.zoomHalfPage(1)
	case "ctrl+u", "pgup":
		return m.zoomHalfPage(-1)
	case "g":
		return m.zoomToTop()
	case "G":
		return m.zoomToBottom()
	case "enter":
		return m.zoomEnter()
	case "o":
		return m.zoomOpenOnly()
	}
	return m, nil
}

// zoomListViewportH returns the row/line capacity of the zoomed panel's
// viewport for the current terminal height (help footer already reserved).
func (m Model) zoomListViewportH() int {
	h := m.height - 1
	if h < 1 {
		h = 1
	}
	return zoomListViewportHeight(h)
}

// logsMaxScroll returns the highest valid zoomScroll offset for the zoomed
// logs viewport.
func (m Model) logsMaxScroll() int {
	max := len(m.logs) - m.zoomListViewportH()
	if max < 0 {
		max = 0
	}
	return max
}

// zoomStep moves the zoomed panel's selection/scroll by delta — one row for
// list panels (queue/autopilot/history), one line for scroll panels
// (logs/git).
func (m Model) zoomStep(delta int) (Model, tea.Cmd) {
	switch m.focus {
	case panelQueue:
		total := len(m.tasks)
		m.zoomSel, m.zoomScroll = ensureSelVisible(m.zoomSel+delta, m.zoomScroll, total, m.zoomListViewportH())
		return m.syncZoomQueueSelection()

	case panelAutopilot:
		prs := m.autopilotPanel.sortedActivePRs()
		if len(prs) == 0 {
			return m, nil
		}
		idx := clampInt(zoomAutopilotIndex(prs, m.zoomAutopilotPR)+delta, 0, len(prs)-1)
		m.zoomAutopilotPR = prs[idx].PRNumber
		return m, nil

	case panelHistory:
		total := len(m.zoomHistory)
		m.zoomSel, m.zoomScroll = ensureSelVisible(m.zoomSel+delta, m.zoomScroll, total, m.zoomListViewportH())
		return m, nil

	case panelLogs:
		if delta < 0 {
			m.zoomLogsFollow = false
		}
		m.zoomScroll = clampInt(m.zoomScroll+delta, 0, m.logsMaxScroll())
		return m, nil

	case panelGit:
		if m.gitGraphState != nil {
			max := len(m.gitGraphState.Lines) - m.zoomListViewportH()
			if max < 0 {
				max = 0
			}
			m.zoomScroll = clampInt(m.zoomScroll+delta, 0, max)
		}
		return m, nil
	}
	return m, nil
}

// zoomHalfPageStep is a large sentinel used by zoomToTop/zoomToBottom to
// jump to either edge via the same clamping logic as zoomStep.
const zoomHalfPageStep = 1 << 30

// zoomHalfPage moves the zoomed panel by half a viewport in direction dir
// (+1 down, -1 up).
func (m Model) zoomHalfPage(dir int) (Model, tea.Cmd) {
	half := m.zoomListViewportH() / 2
	if half < 1 {
		half = 1
	}
	return m.zoomStep(dir * half)
}

// zoomToTop jumps the zoomed panel to its first item/line.
func (m Model) zoomToTop() (Model, tea.Cmd) {
	if m.focus == panelLogs {
		m.zoomLogsFollow = false
		m.zoomScroll = 0
		return m, nil
	}
	return m.zoomStep(-zoomHalfPageStep)
}

// zoomToBottom jumps the zoomed panel to its last item/line. For logs this
// also resumes tail-follow.
func (m Model) zoomToBottom() (Model, tea.Cmd) {
	if m.focus == panelLogs {
		m.zoomLogsFollow = true
		m.zoomScroll = m.logsMaxScroll()
		return m, nil
	}
	return m.zoomStep(zoomHalfPageStep)
}

// zoomEnter handles Enter in zoomed mode: list panels open the selected
// item's URL; scroll panels (logs/git) exit zoom.
func (m Model) zoomEnter() (Model, tea.Cmd) {
	switch m.focus {
	case panelQueue, panelAutopilot, panelHistory:
		return m.zoomOpenSelection()
	default:
		m.zoomed = false
		return m, tea.ClearScreen
	}
}

// zoomOpenOnly handles 'o' in zoomed mode: list panels open the selected
// item's URL; scroll panels are a no-op.
func (m Model) zoomOpenOnly() (Model, tea.Cmd) {
	switch m.focus {
	case panelQueue, panelAutopilot, panelHistory:
		return m.zoomOpenSelection()
	}
	return m, nil
}

// zoomOpenSelection opens the currently-selected list item's URL via
// browserOpener (defaults to openBrowser; injectable for tests).
func (m Model) zoomOpenSelection() (Model, tea.Cmd) {
	opener := m.browserOpener
	if opener == nil {
		opener = openBrowser
	}
	switch m.focus {
	case panelQueue:
		sorted := m.sortedTasks()
		if m.zoomSel >= 0 && m.zoomSel < len(sorted) {
			if url := sorted[m.zoomSel].IssueURL; url != "" {
				_ = opener(url)
			}
		}
	case panelAutopilot:
		prs := m.autopilotPanel.sortedActivePRs()
		idx := zoomAutopilotIndex(prs, m.zoomAutopilotPR)
		if idx >= 0 && idx < len(prs) {
			if url := prs[idx].PRURL; url != "" {
				_ = opener(url)
			}
		}
	case panelHistory:
		if m.zoomSel >= 0 && m.zoomSel < len(m.zoomHistory) {
			if url := m.zoomHistory[m.zoomSel].PRUrl; url != "" {
				_ = opener(url)
			}
		}
	}
	return m, nil
}

// syncZoomQueueSelection maps the zoomed queue's sorted-list selection back
// onto m.selectedTask (an index into the unsorted m.tasks) and syncs the
// git graph to the newly-selected task's project, mirroring grid mode's
// j/k-driven sync (GH-2167).
func (m Model) syncZoomQueueSelection() (Model, tea.Cmd) {
	sorted := m.sortedTasks()
	if m.zoomSel < 0 || m.zoomSel >= len(sorted) {
		return m, nil
	}
	id := sorted[m.zoomSel].ID
	for i, t := range m.tasks {
		if t.ID == id {
			m.selectedTask = i
			break
		}
	}
	cmd := m.syncGitGraphToSelectedTask()
	return m, cmd
}

// renderZoomed renders only the currently-focused panel at full screen size
// (width x height-1, the help footer reserves the last line).
func (m Model) renderZoomed() string {
	w := m.width
	h := m.height - 1
	if h < 1 {
		h = 1
	}
	switch m.focus {
	case panelQueue:
		return m.renderZoomedQueue(w, h)
	case panelAutopilot:
		return m.renderZoomedAutopilot(w, h)
	case panelHistory:
		return m.renderZoomedHistory(w, h)
	case panelLogs:
		return m.renderZoomedLogs(w, h)
	case panelGit:
		return m.renderGitGraphZoomed(w, h)
	}
	return ""
}

// renderZoomedQueue renders every task (uncapped), preserving the same
// state-priority sort order as the grid QUEUE panel.
func (m Model) renderZoomedQueue(w, h int) string {
	sorted := m.sortedTasks()
	idW := queueIDColumnWidth(sorted)
	offsets := make([]int, len(sorted))
	qi := 0
	for i, t := range sorted {
		if t.Status == "queued" {
			offsets[i] = qi
			qi++
		}
	}

	total := len(sorted)
	visible := zoomListViewportHeight(h)
	sel, scroll := ensureSelVisible(m.zoomSel, m.zoomScroll, total, visible)
	iw := w - 4

	var lines []string
	if total == 0 {
		lines = append(lines, "  No tasks in queue")
	} else {
		end := minInt(scroll+visible, total)
		for i := scroll; i < end; i++ {
			lines = append(lines, m.renderTask(sorted[i], i == sel, offsets[i], iw, idW))
		}
	}
	indicator := dimStyle.Render(zoomListIndicator(scroll, minInt(scroll+visible, total), total))
	return renderPanelStyled("queue", indicator, strings.Join(lines, "\n"), w, focusChrome)
}

// renderZoomedAutopilot renders every active PR (uncapped), sorted and
// selected by PR number so a live-pull reorder can't move the selection.
// Relies on View()'s height truncation rather than its own viewport window
// since each PR can render 1 or 2 lines (retry/failure detail).
func (m Model) renderZoomedAutopilot(w, h int) string {
	_ = h
	if m.autopilotPanel == nil || m.autopilotPanel.controller == nil {
		return renderPanelStyled("autopilot", "", "  Disabled", w, focusChrome)
	}
	prs := m.autopilotPanel.sortedActivePRs()
	if len(prs) == 0 {
		return renderPanelStyled("autopilot", "", "  "+dimStyle.Render("idle · no active PR"), w, focusChrome)
	}

	cfg := m.autopilotPanel.controller.Config()
	maxFailures := cfg.MaxFailures
	if maxFailures <= 0 {
		maxFailures = 5
	}
	selIdx := zoomAutopilotIndex(prs, m.zoomAutopilotPR)
	iw := w - 4

	var lines []string
	for i, pr := range prs {
		rows := renderAutopilotRow(pr, m.autopilotPanel.controller.GetPRFailures(pr.PRNumber), maxFailures, iw)
		rows[0] = spliceSelector(rows[0], i == selIdx)
		lines = append(lines, rows...)
	}
	info := fmt.Sprintf("%d prs", len(prs))
	return renderPanelStyled("autopilot", dimStyle.Render(info), strings.Join(lines, "\n"), w, focusChrome)
}

// renderZoomedHistory renders m.zoomHistory (up to 100 distinct tasks,
// populated by historyZoomCmd).
func (m Model) renderZoomedHistory(w, h int) string {
	tasks := m.zoomHistory
	total := len(tasks)
	visible := zoomListViewportHeight(h)
	sel, scroll := ensureSelVisible(m.zoomSel, m.zoomScroll, total, visible)
	iw := w - 4

	var lines []string
	if total == 0 {
		lines = append(lines, "  No completed tasks yet")
	} else {
		end := minInt(scroll+visible, total)
		for i := scroll; i < end; i++ {
			line := renderStandaloneLine(tasks[i], iw)
			lines = append(lines, spliceSelector(line, i == sel))
		}
	}
	indicator := dimStyle.Render(zoomListIndicator(scroll, minInt(scroll+visible, total), total))
	return renderPanelStyled("history", indicator, strings.Join(lines, "\n"), w, focusChrome)
}

// renderZoomedLogs renders a scrollable window over m.logs (up to 1000
// retained lines), tail-following by default.
func (m Model) renderZoomedLogs(w, h int) string {
	iw := w - 4
	visible := zoomListViewportHeight(h)
	total := len(m.logs)
	scroll := clampInt(m.zoomScroll, 0, m.logsMaxScroll())
	end := minInt(scroll+visible, total)

	var lines []string
	if total == 0 {
		lines = append(lines, "  No logs yet")
	} else {
		for _, line := range m.logs[scroll:end] {
			lines = append(lines, "  "+truncateVisual(line, iw-2))
		}
	}
	indicator := dimStyle.Render(zoomListIndicator(scroll, end, total))
	return renderPanelStyled("logs", indicator, strings.Join(lines, "\n"), w, focusChrome)
}

// renderGitGraphZoomed renders the git graph panel at full screen size,
// using zoomScroll instead of gitGraphScroll for its offset so grid-mode
// scroll position is untouched (esc-return is free).
func (m Model) renderGitGraphZoomed(w, h int) string {
	mm := m
	mm.gitGraphScroll = m.zoomScroll
	return mm.renderGitGraph(w, h)
}

// historyZoomMsg carries the zoomed history panel's full dataset, fetched
// asynchronously by historyZoomCmd.
type historyZoomMsg struct {
	tasks []CompletedTask
}

// historyZoomCmd fetches up to 100 distinct-by-task completed executions
// (scanning up to 500 raw rows so retried tasks don't crowd out the rest —
// same rationale as firstNDistinctByTask) for the zoomed history panel,
// hydrating each task's stage strip inside the Cmd so View() never touches
// the store.
func historyZoomCmd(store *memory.Store, projectPath string) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return historyZoomMsg{}
		}
		executions, err := store.GetRecentExecutions(500, projectPath)
		if err != nil {
			slog.Warn("history zoom: failed to load executions", slog.Any("error", err))
			return historyZoomMsg{}
		}

		var tasks []CompletedTask
		for _, exec := range firstNDistinctByTask(executions, 100) {
			status := displayStatus(exec.Status)
			completedAt := exec.CreatedAt
			if exec.CompletedAt != nil {
				completedAt = *exec.CompletedAt
			}
			events, err := store.ListExecutionEvents(exec.ID)
			if err != nil {
				slog.Warn("history zoom: failed to load execution events",
					slog.Any("error", err), slog.String("execution_id", exec.ID))
			}
			tasks = append(tasks, CompletedTask{
				ID:          exec.TaskID,
				Title:       exec.TaskTitle,
				Status:      status,
				Duration:    fmt.Sprintf("%dms", exec.DurationMs),
				CompletedAt: completedAt,
				PeakRSSMB:   exec.PeakRSSMB,
				Stage:       stageInfoForExecution(events, status),
				PRUrl:       exec.PRUrl,
			})
		}
		return historyZoomMsg{tasks: tasks}
	}
}
