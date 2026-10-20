package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func ids(ts []TaskDisplay) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return strings.Join(out, ",")
}

func TestGroupQueueTasks_Order(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name       string
		tasks      []TaskDisplay
		wantGroups []string // "name:ids"
		wantHidden int
	}{
		{
			name: "finished-only project newer than active project still sorts after it",
			tasks: []TaskDisplay{
				{ID: "A-1", ProjectName: "fresh", ProjectPath: "/p/fresh", Status: QueueStatusDone, StartedAt: ago(10 * time.Minute), CompletedAt: ago(1 * time.Minute)},
				{ID: "B-1", ProjectName: "busy", ProjectPath: "/p/busy", Status: QueueStatusQueued},
			},
			wantGroups: []string{"busy:B-1", "fresh:A-1"},
		},
		{
			name: "queued order is kept, running first, finished newest first",
			tasks: []TaskDisplay{
				{ID: "GH-9", ProjectPath: "/p/x", Status: QueueStatusDone, CompletedAt: ago(3 * time.Hour)},
				{ID: "GH-8", ProjectPath: "/p/x", Status: QueueStatusQueued},
				{ID: "GH-3", ProjectPath: "/p/x", Status: QueueStatusQueued},
				{ID: "GH-7", ProjectPath: "/p/x", Status: QueueStatusFailed, CompletedAt: ago(1 * time.Hour)},
				{ID: "GH-5", ProjectPath: "/p/x", Status: QueueStatusPending},
				{ID: "GH-6", ProjectPath: "/p/x", Status: QueueStatusRunning, StartedAt: ago(time.Minute)},
			},
			// cap of 3 applies after ordering
			wantGroups: []string{"x:GH-6,GH-8,GH-3"},
		},
		{
			name: "finished sort by CompletedAt desc with ID tiebreak",
			tasks: []TaskDisplay{
				{ID: "GH-2", ProjectName: "p", Status: QueueStatusDone, CompletedAt: ago(5 * time.Hour)},
				{ID: "GH-4", ProjectName: "p", Status: QueueStatusNoOp, CompletedAt: ago(1 * time.Hour)},
				{ID: "GH-1", ProjectName: "p", Status: QueueStatusDone, CompletedAt: ago(1 * time.Hour)},
			},
			wantGroups: []string{"p:GH-1,GH-4,GH-2"},
		},
		{
			name: "same base name in two paths stays two groups; name tiebreak",
			tasks: []TaskDisplay{
				{ID: "GH-1", ProjectPath: "/a/app", Status: QueueStatusQueued},
				{ID: "GH-2", ProjectPath: "/b/app", Status: QueueStatusQueued},
			},
			wantGroups: []string{"app:GH-1", "app:GH-2"},
		},
		{
			name: "no project info groups under (no project)",
			tasks: []TaskDisplay{
				{ID: "GH-1", Status: QueueStatusQueued},
				{ID: "GH-2", Status: QueueStatusQueued},
			},
			wantGroups: []string{"(no project):GH-1,GH-2"},
		},
		{
			name: "empty name falls back to path base; key falls back to name",
			tasks: []TaskDisplay{
				{ID: "GH-1", ProjectPath: "/work/widgets", Status: QueueStatusQueued},
				{ID: "GH-2", ProjectName: "named", Status: QueueStatusQueued},
			},
			wantGroups: []string{"named:GH-2", "widgets:GH-1"},
		},
		{
			name: "newest activity first among finished-only projects; cap at 3 projects",
			tasks: []TaskDisplay{
				{ID: "1", ProjectName: "p1", Status: QueueStatusDone, CompletedAt: ago(4 * time.Hour)},
				{ID: "2", ProjectName: "p2", Status: QueueStatusDone, CompletedAt: ago(1 * time.Hour)},
				{ID: "3", ProjectName: "p3", Status: QueueStatusDone, CompletedAt: ago(3 * time.Hour)},
				{ID: "4", ProjectName: "p4", Status: QueueStatusDone, CompletedAt: ago(2 * time.Hour)},
				{ID: "5", ProjectName: "p5", Status: QueueStatusDone, CompletedAt: ago(5 * time.Hour)},
			},
			wantGroups: []string{"p2:2", "p4:4", "p3:3"},
			wantHidden: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups, hidden := groupQueueTasks(tc.tasks, now)
			var got []string
			for _, g := range groups {
				got = append(got, g.Name+":"+ids(g.Tasks))
			}
			if strings.Join(got, "|") != strings.Join(tc.wantGroups, "|") {
				t.Errorf("groups = %v, want %v", got, tc.wantGroups)
			}
			if hidden != tc.wantHidden {
				t.Errorf("hidden = %d, want %d", hidden, tc.wantHidden)
			}
		})
	}
}

func TestGroupQueueTasks_RightValue(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name  string
		tasks []TaskDisplay
		want  string
	}{
		{"running", []TaskDisplay{{ID: "1", Status: QueueStatusRunning, Phase: "Implementing", StartedAt: ago(3*time.Minute + 5*time.Second)}}, "implementing · 3m"},
		{"two running uses first started", []TaskDisplay{
			{ID: "1", Status: QueueStatusRunning, Phase: "Testing", StartedAt: ago(2 * time.Minute)},
			{ID: "2", Status: QueueStatusRunning, Phase: "Implementing", StartedAt: ago(9 * time.Minute)},
		}, "implementing · 9m"},
		{"running beats done", []TaskDisplay{
			{ID: "1", Status: QueueStatusDone, CompletedAt: ago(time.Hour)},
			{ID: "2", Status: QueueStatusRunning, Phase: "build", StartedAt: ago(time.Minute)},
		}, "build · 1m"},
		{"done", []TaskDisplay{
			{ID: "1", Status: QueueStatusDone, CompletedAt: ago(25 * time.Minute)},
			{ID: "2", Status: QueueStatusDone, CompletedAt: ago(8 * time.Hour)},
			{ID: "3", Status: QueueStatusFailed, CompletedAt: ago(time.Minute)},
		}, "last ✓ 25m ago"},
		{"only failed", []TaskDisplay{{ID: "1", Status: QueueStatusFailed, CompletedAt: ago(2 * time.Hour)}}, "last ✗ 2h ago"},
		{"only queued", []TaskDisplay{{ID: "1", Status: QueueStatusQueued}, {ID: "2", Status: QueueStatusPending}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups, _ := groupQueueTasks(tc.tasks, now)
			if len(groups) != 1 || groups[0].RightValue != tc.want {
				t.Errorf("RightValue = %+v, want %q", groups, tc.want)
			}
		})
	}
}

func manyProjectTasks(projects, perProject int) []TaskDisplay {
	now := time.Now()
	var tasks []TaskDisplay
	for p := 0; p < projects; p++ {
		for k := 0; k < perProject; k++ {
			tasks = append(tasks, TaskDisplay{
				ID:          fmt.Sprintf("GH-%d%d", p+1, k),
				Title:       "a fairly long ticket title for width checks",
				Status:      QueueStatusDone,
				PRURL:       "https://github.com/o/r/pull/7",
				ProjectPath: fmt.Sprintf("/work/proj%d", p),
				ProjectName: fmt.Sprintf("proj%d", p),
				StartedAt:   now.Add(-time.Duration(p*10+k+20) * time.Minute),
				CompletedAt: now.Add(-time.Duration(p*10+k+5) * time.Minute),
			})
		}
	}
	return tasks
}

func TestRenderTasks_GroupedCapped(t *testing.T) {
	m := NewModel("test")
	m.tasks = manyProjectTasks(5, 4)
	out := m.renderTasks()
	lines := strings.Split(out, "\n")

	if !strings.Contains(out, "+2 projects") {
		t.Errorf("missing +2 projects line:\n%s", out)
	}
	rows := 0
	for _, l := range lines {
		if strings.Contains(l, "✓ done") {
			rows++
		}
	}
	if rows != 9 {
		t.Errorf("visible ticket rows = %d, want 9\n%s", rows, out)
	}
	// borders excluded
	if inner := len(lines) - 2; inner > 17 {
		t.Errorf("inner height = %d, want <= 17\n%s", inner, out)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > m.effectivePanelTotalWidth() {
			t.Errorf("line width %d exceeds panel: %q", w, l)
		}
	}
}

func TestRenderTasks_SingleProjectNoOverflowNoTrailingBlank(t *testing.T) {
	m := NewModel("test")
	m.tasks = manyProjectTasks(1, 2)
	out := m.renderTasks()
	if strings.Contains(out, "projects") {
		t.Errorf("unexpected overflow line:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	// top border, pad, header, 2 rows, bottom border
	if len(lines) != 6 {
		t.Errorf("got %d lines, want 6 (no trailing blank line):\n%s", len(lines), out)
	}
}

func TestRenderTasks_ProjectHeaderNames(t *testing.T) {
	m := NewModel("test")
	m.tasks = []TaskDisplay{
		{ID: "GH-1", Status: QueueStatusQueued, ProjectPath: "/work/widgets"},
		{ID: "GH-2", Status: QueueStatusQueued},
	}
	out := m.renderTasks()
	for _, want := range []string{"widgets", "(no project)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTasks_FinishedAndRunningRows(t *testing.T) {
	now := time.Now()
	m := NewModel("test")
	m.tasks = []TaskDisplay{
		{ID: "GH-1", Title: "done one", Status: QueueStatusDone, PRURL: "https://github.com/o/r/pull/419",
			ProjectName: "p", StartedAt: now.Add(-54 * time.Minute), CompletedAt: now.Add(-40 * time.Minute)},
		{ID: "GH-2", Title: "running one", Status: QueueStatusRunning, Progress: 12, Phase: "Implementing", ProjectName: "p", StartedAt: now.Add(-3 * time.Minute)},
	}
	out := m.renderTasks()
	for _, want := range []string{"14m · 40m ago", "#419", "12%", "■", "implementing · 3m"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTasks_SelectionMatchedByID(t *testing.T) {
	now := time.Now()
	m := NewModel("test")
	// m.tasks order differs from display order: index 0 is the oldest done row.
	m.tasks = []TaskDisplay{
		{ID: "GH-1", Status: QueueStatusDone, ProjectName: "p", CompletedAt: now.Add(-5 * time.Hour)},
		{ID: "GH-2", Status: QueueStatusRunning, ProjectName: "p", StartedAt: now.Add(-time.Minute)},
	}
	m.selectedTask = 0
	rowWith := func(out, id string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, id) {
				return l
			}
		}
		return ""
	}
	out := m.renderTasks()
	if !strings.Contains(rowWith(out, "GH-1"), "▸") || strings.Contains(rowWith(out, "GH-2"), "▸") {
		t.Errorf("marker should be on GH-1 only:\n%s", out)
	}

	// Selected task hidden (4th ticket of the project): no marker anywhere.
	m.tasks = manyProjectTasks(1, 4)
	m.selectedTask = 3 // oldest-completed per ordering -> hidden
	if hidden := m.tasks[3].ID; strings.Contains(m.renderTasks(), hidden) {
		t.Fatalf("test setup: %s should be hidden", hidden)
	}
	if strings.Contains(m.renderTasks(), "▸") {
		t.Error("marker rendered although selected task is hidden")
	}
	m.selectedTask = 99 // out of range must not panic
	_ = m.renderTasks()
}

func TestRenderTasks_LegendCountsHiddenProjects(t *testing.T) {
	now := time.Now()
	m := NewModel("test")
	m.tasks = manyProjectTasks(3, 1)
	// A fourth project with a running task is active, so it sorts first; make
	// 4 more finished-only projects' worth of noise and several running ones.
	for i := 0; i < 5; i++ {
		m.tasks = append(m.tasks, TaskDisplay{ID: fmt.Sprintf("R-%d", i), Status: QueueStatusRunning,
			ProjectName: fmt.Sprintf("run%d", i), StartedAt: now.Add(-time.Minute)})
	}
	out := m.renderTasks()
	if !strings.Contains(out, "5 running") {
		t.Errorf("legend should count all 5 running tasks:\n%s", out)
	}
	if !strings.Contains(out, "+5 projects") {
		t.Errorf("expected 5 hidden projects:\n%s", out)
	}
}

func TestZoomedQueue_StaysFlatAndUncapped(t *testing.T) {
	m := navTestModel(100, 60)
	m.tasks = manyProjectTasks(5, 4)
	m.zoomed = true
	m.focus = panelQueue
	out := m.renderZoomedQueue(100, 60)
	if strings.Contains(out, "projects") {
		t.Errorf("zoom must not show overflow line:\n%s", out)
	}
	for _, tk := range m.tasks {
		if !strings.Contains(out, tk.ID) {
			t.Errorf("zoom missing %s", tk.ID)
		}
	}
}

func TestRenderTasks_LineWidthsAcrossPanelWidths(t *testing.T) {
	for _, tw := range []int{panelTotalWidth, 80, 120} {
		m := NewModel("test")
		m.width = tw
		m.tasks = manyProjectTasks(5, 4)
		m.tasks = append(m.tasks, TaskDisplay{ID: "GH-9", Status: QueueStatusRunning, Phase: "a-very-long-phase-name-here",
			ProjectName: "an-extremely-long-project-name-that-should-be-truncated-by-the-header", StartedAt: time.Now().Add(-90 * time.Minute)})
		for _, l := range strings.Split(m.renderTasks(), "\n") {
			if w := lipgloss.Width(l); w > m.effectivePanelTotalWidth() {
				t.Errorf("tw=%d: line width %d exceeds panel %d: %q", tw, w, m.effectivePanelTotalWidth(), l)
			}
		}
	}
}
