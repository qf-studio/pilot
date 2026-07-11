package dashboard

// TASK-399 acceptance tests: Update()/View()-level integration coverage for
// grom-style spatial navigation and zoom — these are the guard that was
// missing from TASK-398/#4200 (helpers merged and unit-tested, but never
// wired into tui.go, so nothing actually shipped).

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
)

// navTestModel builds a Model with every panel populated (tasks, active PRs,
// history, logs, git graph) for navigation tests that need all 5 panels
// present and navigable.
func navTestModel(width, height int) Model {
	m := NewModel("test")
	m.width = width
	m.height = height
	m.tasks = []TaskDisplay{
		{ID: "GH-1", Title: "Task A", Status: "running", IssueURL: "https://example.com/issues/1"},
		{ID: "GH-2", Title: "Task B", Status: "queued", IssueURL: "https://example.com/issues/2"},
	}
	m.autopilotPanel = &AutopilotPanel{controller: newFakeCtl([]*autopilot.PRState{
		{PRNumber: 501, PRTitle: "fix: something", Stage: autopilot.StageWaitingCI, CreatedAt: time.Now()},
	}, 3, nil)}
	m.completedTasks = []CompletedTask{
		{ID: "GH-3", Title: "Shipped", Status: "success", CompletedAt: time.Now()},
	}
	m.logs = []string{"[00:00] boot", "[00:01] ready"}
	m.gitGraphMode = GitGraphVisible
	m.gitGraphState = &GitGraphState{Lines: []GitGraphLine{
		{GraphChars: "● ", SHA: "abc1234", Author: "Alice", Message: "initial commit"},
	}}
	return m
}

// TestNav_FocusMovesOnHJKL verifies h/j/k/l drive spatial focus movement via
// focusMove over the current frame's panel rects.
func TestNav_FocusMovesOnHJKL(t *testing.T) {
	m := navTestModel(140, 50)
	if m.focus != panelQueue {
		t.Fatalf("initial focus = %v, want panelQueue", m.focus)
	}

	// l from the queue (left column) should land on the git panel — it's
	// the only panel to the right in side-by-side layout.
	updated, _ := m.Update(makeKey("l"))
	m = updated.(Model)
	if m.focus != panelGit {
		t.Errorf("l from panelQueue: focus = %v, want panelGit", m.focus)
	}

	// h from the git panel should move back onto one of the left-column panels.
	updated, _ = m.Update(makeKey("h"))
	m = updated.(Model)
	switch m.focus {
	case panelQueue, panelAutopilot, panelHistory, panelLogs:
		// ok — any left-column panel is spatially correct
	default:
		t.Errorf("h from panelGit: focus = %v, want a left-column panel", m.focus)
	}

	// j/k should walk down/up the left-column stack.
	m.focus = panelQueue
	updated, _ = m.Update(makeKey("j"))
	m = updated.(Model)
	if m.focus != panelAutopilot {
		t.Errorf("j from panelQueue: focus = %v, want panelAutopilot", m.focus)
	}
	updated, _ = m.Update(makeKey("k"))
	m = updated.(Model)
	if m.focus != panelQueue {
		t.Errorf("k from panelAutopilot: focus = %v, want panelQueue", m.focus)
	}
}

// TestNav_EnterZoomsEscRestores verifies enter zooms the focused panel to
// full screen (siblings absent from the render) and esc restores the grid
// with focus/scroll intact.
func TestNav_EnterZoomsEscRestores(t *testing.T) {
	m := navTestModel(140, 50)
	m.focus = panelQueue

	updated, _ := m.Update(makeKey("enter"))
	m = updated.(Model)
	if !m.zoomed {
		t.Fatal("enter should set zoomed=true")
	}

	out := stripANSI(m.View())
	if !strings.Contains(out, "queue") {
		t.Errorf("zoomed view missing the focused queue panel:\n%s", out)
	}
	for _, sibling := range []string{"autopilot", "history", "logs", "git graph"} {
		if strings.Contains(out, sibling) {
			t.Errorf("zoomed view should not render sibling panel %q:\n%s", sibling, out)
		}
	}

	updated, _ = m.Update(makeKey("esc"))
	m = updated.(Model)
	if m.zoomed {
		t.Error("esc should restore zoomed=false")
	}
	if m.focus != panelQueue {
		t.Errorf("focus after esc = %v, want panelQueue (unchanged)", m.focus)
	}
}

// TestNav_GitGraphVisibleOnFirstFrame verifies a freshly-constructed Model
// defaults gitGraphMode to Visible and paints the git panel on the very
// first View() — no manual 'g' press required.
func TestNav_GitGraphVisibleOnFirstFrame(t *testing.T) {
	m := NewModel("test")
	if m.gitGraphMode != GitGraphVisible {
		t.Fatalf("gitGraphMode on construction = %v, want GitGraphVisible", m.gitGraphMode)
	}
	if cmd := m.Init(); cmd == nil {
		t.Error("Init() should batch a git-graph refresh when visible by default")
	}

	m.width = 140
	m.height = 50
	m.gitGraphState = &GitGraphState{Lines: []GitGraphLine{
		{GraphChars: "● ", SHA: "abc1234", Message: "initial commit"},
	}}

	out := stripANSI(m.View())
	if !strings.Contains(out, "git") {
		t.Errorf("View() before any 'g' press should render the git panel:\n%s", out)
	}
}

// TestNav_LogsRebind verifies 'L' toggles the logs panel and 'l' does NOT —
// 'l' now moves spatial focus to the right.
func TestNav_LogsRebind(t *testing.T) {
	m := navTestModel(140, 50)
	initial := m.showLogs

	updated, _ := m.Update(makeKey("L"))
	m = updated.(Model)
	if m.showLogs != !initial {
		t.Fatalf("L should toggle showLogs: got %v, want %v", m.showLogs, !initial)
	}

	afterL := m.showLogs
	updated, _ = m.Update(makeKey("l"))
	m = updated.(Model)
	if m.showLogs != afterL {
		t.Errorf("l should not toggle logs; showLogs changed from %v to %v", afterL, m.showLogs)
	}
}

// TestNav_ZoomedQueueOpensURL verifies zooming the queue, selecting an item,
// and pressing enter opens that item's IssueURL via the injected
// browserOpener.
func TestNav_ZoomedQueueOpensURL(t *testing.T) {
	m := navTestModel(100, 40)
	m.tasks = []TaskDisplay{
		{ID: "GH-1", Title: "A", Status: "done", IssueURL: "https://example.com/1"},
		{ID: "GH-2", Title: "B", Status: "done", IssueURL: "https://example.com/2"},
		{ID: "GH-3", Title: "C", Status: "done", IssueURL: "https://example.com/3"},
	}
	var opened string
	m.browserOpener = func(url string) error { opened = url; return nil }
	m.focus = panelQueue
	m.zoomed = true
	m.zoomSel = 0

	// Move to the second item, then open it.
	updated, _ := m.Update(makeKey("j"))
	m = updated.(Model)
	updated, _ = m.Update(makeKey("enter"))
	m = updated.(Model)

	want := m.sortedTasks()[1].IssueURL
	if opened != want {
		t.Errorf("opened URL = %q, want %q (2nd sorted task)", opened, want)
	}
}

// TestNav_ZoomedAutopilotUncapped verifies the zoomed autopilot panel lists
// every active PR (no maxAutopilotRows cap) and that selection — keyed by PR
// number, not index — survives a live-pull reorder (GetActivePRs iterates a
// map, so raw order is nondeterministic between calls).
func TestNav_ZoomedAutopilotUncapped(t *testing.T) {
	prs := make([]*autopilot.PRState, 8)
	for i := range prs {
		prs[i] = &autopilot.PRState{
			PRNumber:  100 + i,
			PRTitle:   fmt.Sprintf("pr %d", i),
			Stage:     autopilot.StageWaitingCI,
			CreatedAt: time.Now(),
		}
	}
	ctl := newFakeCtl(prs, 3, nil)

	m := navTestModel(100, 60)
	m.autopilotPanel = &AutopilotPanel{controller: ctl}
	m.focus = panelAutopilot
	m.zoomed = true
	m.zoomAutopilotPR = 103

	assertSelected := func(out string) {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "#103") {
				if !strings.Contains(line, "▸") {
					t.Errorf("PR #103's row missing the selector marker: %q", line)
				}
				return
			}
		}
		t.Error("PR #103 not found in zoomed autopilot output")
	}

	out := stripANSI(m.View())
	for i := range prs {
		want := fmt.Sprintf("#%d", 100+i)
		if !strings.Contains(out, want) {
			t.Errorf("zoomed autopilot missing PR %s (uncapped list)", want)
		}
	}
	if strings.Contains(out, "more") {
		t.Error("zoomed autopilot should not show a '+N more' cap")
	}
	assertSelected(out)

	// Simulate a live-pull reorder.
	ctl.prs = []*autopilot.PRState{prs[7], prs[0], prs[3], prs[1], prs[2], prs[4], prs[5], prs[6]}
	assertSelected(stripANSI(m.View()))
	if m.zoomAutopilotPR != 103 {
		t.Errorf("selection PR number changed across reorder: %d, want 103", m.zoomAutopilotPR)
	}
}

// TestNav_ZoomedHistoryUncapped verifies historyZoomCmd/historyZoomMsg pull
// up to 100 distinct-by-task rows from the store — well past the grid
// mode's 5-item cap.
func TestNav_ZoomedHistoryUncapped(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-nav-history-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const n = 30
	for i := 0; i < n; i++ {
		if err := store.SaveExecution(&memory.Execution{
			ID:          fmt.Sprintf("exec-%02d", i),
			TaskID:      fmt.Sprintf("TASK-%02d", i),
			ProjectPath: "/test-nav-history",
			Status:      "completed",
			TaskTitle:   fmt.Sprintf("task %02d", i),
		}); err != nil {
			t.Fatalf("SaveExecution(%d): %v", i, err)
		}
	}

	m := NewModelWithStore("test", store)
	m.SetMetricsScopePath("/test-nav-history")
	m.focus = panelHistory
	m.zoomed = true

	msg := historyZoomCmd(m.store, m.metricsScopePath)()
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if len(m.zoomHistory) != n {
		t.Fatalf("zoomHistory len = %d, want %d", len(m.zoomHistory), n)
	}
}

// TestNav_ZoomedLogsFollow verifies the zoomed logs panel tail-follows by
// default, stops moving once the user scrolls up, and resumes on 'G'.
func TestNav_ZoomedLogsFollow(t *testing.T) {
	m := navTestModel(100, 30)
	m.logs = nil
	for i := 0; i < 50; i++ {
		m.logs = append(m.logs, fmt.Sprintf("line %d", i))
	}
	m.focus = panelLogs
	m.zoomed = true
	m.zoomLogsFollow = true
	m.zoomScroll = m.logsMaxScroll()

	// Scroll up — breaks follow.
	updated, _ := m.Update(makeKey("k"))
	m = updated.(Model)
	if m.zoomLogsFollow {
		t.Fatal("scrolling up should break tail-follow")
	}
	scrollAfterUp := m.zoomScroll

	// New lines arrive while scrolled up — the viewport must not move.
	updated, _ = m.Update(addLogMsg("new line"))
	m = updated.(Model)
	if m.zoomScroll != scrollAfterUp {
		t.Errorf("zoomScroll moved while scrolled up: got %d, want %d", m.zoomScroll, scrollAfterUp)
	}

	// 'G' resumes tail-follow and jumps back to the bottom.
	updated, _ = m.Update(makeKey("G"))
	m = updated.(Model)
	if !m.zoomLogsFollow {
		t.Error("G should resume tail-follow")
	}
	if m.zoomScroll != m.logsMaxScroll() {
		t.Errorf("zoomScroll after G = %d, want max %d", m.zoomScroll, m.logsMaxScroll())
	}
}

// TestNav_WidthInvariantMatrix verifies every composed line (grid and
// zoomed, across every panel) is exactly m.width visual columns at a range
// of terminal widths spanning stacked and side-by-side layouts. The help
// footer is excluded: it intentionally renders at effectivePanelTotalWidth
// (a fixed-width legend), not the full terminal width, in side-by-side mode.
func TestNav_WidthInvariantMatrix(t *testing.T) {
	widths := []int{80, 96, 140, 200}
	panels := []panelID{panelQueue, panelAutopilot, panelHistory, panelLogs, panelGit}

	for _, w := range widths {
		t.Run(fmt.Sprintf("width=%d/grid", w), func(t *testing.T) {
			m := navTestModel(w, 50)
			checkLinesWidth(t, m.View(), w)
		})
		for _, p := range panels {
			t.Run(fmt.Sprintf("width=%d/zoomed/%s", w, p), func(t *testing.T) {
				m := navTestModel(w, 50)
				m.zoomed = true
				m.focus = p
				checkLinesWidth(t, m.View(), w)
			})
		}
	}
}

// checkLinesWidth asserts every non-blank line of out except the last (help
// footer) is exactly w visual columns wide. Blank filler lines appended by
// View()'s height-padding step are bare "" (not space-padded to width) —
// pre-existing behavior, not part of this invariant (mirrors the same
// skip in TestFullDashboardRender_WidthInvariants).
func checkLinesWidth(t *testing.T, out string, w int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines[:len(lines)-1] {
		if line == "" {
			continue
		}
		if got := lipgloss.Width(line); got != w {
			t.Errorf("line %d width = %d, want %d: %q", i, got, w, stripANSI(line))
		}
	}
}
