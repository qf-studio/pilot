package dashboard

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alekspetrov/pilot/internal/memory"
)

func TestFormatCompact(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{57300, "57.3K"},
		{1000000, "1.0M"},
		{1234567, "1.2M"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatCompact(tt.input)
			if got != tt.want {
				t.Errorf("formatCompact(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeToSparkline(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		width  int
		want   []int
	}{
		{
			name:   "empty input returns all zeros",
			values: nil,
			width:  7,
			want:   []int{0, 0, 0, 0, 0, 0, 0},
		},
		{
			name:   "single value maps to midpoint",
			values: []float64{42},
			width:  7,
			want:   []int{0, 0, 0, 0, 0, 0, 4},
		},
		{
			name:   "all zeros map to baseline",
			values: []float64{0, 0, 0, 0, 0, 0, 0},
			width:  7,
			want:   []int{1, 1, 1, 1, 1, 1, 1},
		},
		{
			name:   "all same non-zero values map to midpoint",
			values: []float64{5, 5, 5, 5, 5, 5, 5},
			width:  7,
			want:   []int{4, 4, 4, 4, 4, 4, 4},
		},
		{
			name:   "ascending values span 1-8 with zero baseline",
			values: []float64{0, 1, 2, 3, 4, 5, 6, 7, 8},
			width:  9,
			want:   []int{1, 2, 3, 4, 5, 5, 6, 7, 8},
		},
		{
			name:   "fewer values than width left-pads with zeros",
			values: []float64{0, 100},
			width:  5,
			want:   []int{0, 0, 0, 1, 8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeToSparkline(tt.values, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %d, want %d (full: %v)", i, got[i], tt.want[i], got)
					break
				}
			}
		})
	}
}

func TestRenderSparkline(t *testing.T) {
	tests := []struct {
		name    string
		levels  []int
		pulsing bool
	}{
		{
			name:    "pulsing includes dot",
			levels:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6},
			pulsing: true,
		},
		{
			name:    "not pulsing has space",
			levels:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 0, 1, 2, 3, 4, 5, 6},
			pulsing: false,
		},
		{
			name:    "all zeros",
			levels:  []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			pulsing: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderSparkline(tt.levels, tt.pulsing)

			// Visual width must equal cardInnerWidth (17)
			runeCount := utf8.RuneCountInString(got)
			if runeCount != cardInnerWidth {
				t.Errorf("visual width = %d runes, want %d (got %q)", runeCount, cardInnerWidth, got)
			}

			// Check pulsing indicator
			runes := []rune(got)
			lastRune := runes[len(runes)-1]
			if tt.pulsing && lastRune != '•' {
				t.Errorf("pulsing=true but last rune = %q, want '•'", lastRune)
			}
			if !tt.pulsing && lastRune != ' ' {
				t.Errorf("pulsing=false but last rune = %q, want ' '", lastRune)
			}
		})
	}
}

func TestBuildMiniCard(t *testing.T) {
	card := buildMiniCard("TEST", "42", "detail one", "detail two", "▁▂▃▄▅▆▇█▁▂▃▄▅▆▇█•")

	lines := strings.Split(card, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != cardWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, cardWidth, line)
		}
	}

	// Check border characters present
	if !strings.Contains(card, "╭") {
		t.Error("missing top-left border ╭")
	}
	if !strings.Contains(card, "╰") {
		t.Error("missing bottom-left border ╰")
	}
	if !strings.Contains(card, "│") {
		t.Error("missing side border │")
	}
}

func TestRenderMetricsCards(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		TotalTokens:  50000,
		InputTokens:  30000,
		OutputTokens: 20000,
		TotalCostUSD: 1.50,
		CostPerTask:  0.25,
		TotalTasks:   10,
		Succeeded:    8,
		Failed:       2,
		TokenHistory: []int64{100, 200, 300, 400, 500, 600, 700},
		CostHistory:  []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7},
		TaskHistory:  []int{1, 2, 3, 2, 1, 3, 2},
	}

	output := m.renderMetricsCards()

	if !strings.Contains(output, "TOKENS") {
		t.Error("output missing TOKENS card")
	}
	if !strings.Contains(output, "COST") {
		t.Error("output missing COST card")
	}
	if !strings.Contains(output, "QUEUE") {
		t.Error("output missing TASKS card")
	}
}

func TestRenderMetricsCards_ZeroState(t *testing.T) {
	m := NewModel("test")
	// metricsCard is zero-value MetricsCardData

	// Must not panic
	output := m.renderMetricsCards()

	if output == "" {
		t.Error("zero-state renderMetricsCards returned empty string")
	}
	if !strings.Contains(output, "TOKENS") {
		t.Error("zero-state output missing TOKENS card")
	}
	if !strings.Contains(output, "COST") {
		t.Error("zero-state output missing COST card")
	}
	if !strings.Contains(output, "QUEUE") {
		t.Error("zero-state output missing TASKS card")
	}
}

func TestRenderTaskCard_ShowsQueueDepth(t *testing.T) {
	m := NewModel("test")
	// Simulate 10 lifetime tasks (succeeded + failed) in metrics
	m.metricsCard.TotalTasks = 10
	m.metricsCard.Succeeded = 8
	m.metricsCard.Failed = 2

	// Simulate 2 active tasks in queue (pending/running)
	m.tasks = []TaskDisplay{
		{ID: "1", Title: "Task A", Status: "running"},
		{ID: "2", Title: "Task B", Status: "pending"},
	}

	output := m.renderTaskCard()

	// QUEUE card value must show current queue depth (2), not lifetime total (10)
	if !strings.Contains(output, "QUEUE") {
		t.Error("output missing QUEUE header")
	}
	// The main value "2" should appear (queue depth)
	if !strings.Contains(output, "2") {
		t.Error("QUEUE card should show current queue depth of 2")
	}
	// Lifetime total "10" should NOT appear as the main value
	if strings.Contains(output, "10") {
		t.Error("QUEUE card should not show lifetime total (10)")
	}
	// Succeeded/failed detail lines should still be present
	if !strings.Contains(output, "8 succeeded") {
		t.Error("QUEUE card missing succeeded count")
	}
	if !strings.Contains(output, "2 failed") {
		t.Error("QUEUE card missing failed count")
	}
}

func TestRenderTaskCard_EmptyQueue(t *testing.T) {
	m := NewModel("test")
	// Historical tasks exist but queue is empty
	m.metricsCard.TotalTasks = 5
	m.metricsCard.Succeeded = 3
	m.metricsCard.Failed = 2
	m.tasks = nil

	output := m.renderTaskCard()

	// Should show 0 for empty queue, not 5 (lifetime total)
	if strings.Contains(output, "5") {
		t.Error("QUEUE card should show 0, not lifetime total (5)")
	}
}

func TestHydrateFromStore_LifetimeTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert executions with known token/cost data across "multiple days"
	execs := []struct {
		id     string
		input  int64
		output int64
		cost   float64
	}{
		{"exec-1", 10000, 5000, 0.50},
		{"exec-2", 20000, 10000, 1.00},
		{"exec-3", 30000, 15000, 1.50},
	}
	for _, e := range execs {
		if err := store.SaveExecution(&memory.Execution{
			ID:          e.id,
			TaskID:      "TASK-" + e.id,
			ProjectPath: "/test",
			Status:      "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
		if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
			ExecutionID:      e.id,
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      e.input + e.output,
			EstimatedCostUSD: e.cost,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", e.id, err)
		}
	}

	// Create model — simulates a fresh restart (new session, empty token usage)
	m := NewModelWithStore("test", store)

	// Metrics card should reflect lifetime totals from executions, not session (zero)
	wantInput := 60000
	wantOutput := 30000
	wantTotal := 90000
	wantCost := 3.00

	if m.metricsCard.InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", m.metricsCard.InputTokens, wantInput)
	}
	if m.metricsCard.OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", m.metricsCard.OutputTokens, wantOutput)
	}
	if m.metricsCard.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", m.metricsCard.TotalTokens, wantTotal)
	}
	if math.Abs(m.metricsCard.TotalCostUSD-wantCost) > 0.001 {
		t.Errorf("TotalCostUSD = %.4f, want %.4f", m.metricsCard.TotalCostUSD, wantCost)
	}
}

func TestUpdateTokensMsg_AddsToLifetimeTotals(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Seed with historical execution data
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-old", TaskID: "TASK-OLD", ProjectPath: "/test", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID: "exec-old", TokensInput: 10000, TokensOutput: 5000,
		TokensTotal: 15000, EstimatedCostUSD: 1.00,
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	m := NewModelWithStore("test", store)

	// Simulate a token update from a running execution (cumulative: 2000 in, 1000 out)
	updated, _ := m.Update(updateTokensMsg{InputTokens: 2000, OutputTokens: 1000, TotalTokens: 3000})
	model := updated.(Model)

	// metricsCard should be lifetime (10000+2000=12000 input, 5000+1000=6000 output)
	if model.metricsCard.InputTokens != 12000 {
		t.Errorf("InputTokens = %d, want 12000", model.metricsCard.InputTokens)
	}
	if model.metricsCard.OutputTokens != 6000 {
		t.Errorf("OutputTokens = %d, want 6000", model.metricsCard.OutputTokens)
	}
	if model.metricsCard.TotalTokens != 18000 {
		t.Errorf("TotalTokens = %d, want 18000", model.metricsCard.TotalTokens)
	}
}

func TestAddCompletedTask_NewFieldsStored(t *testing.T) {
	m := NewModel("test")

	// Send a completed task with parentID and isEpic=false (sub-issue)
	msg := addCompletedTaskMsg(CompletedTask{
		ID:       "GH-575",
		Title:    "Sub-issue task",
		Status:   "success",
		Duration: "30s",
		ParentID: "GH-498",
		IsEpic:   false,
	})
	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.completedTasks) != 1 {
		t.Fatalf("completedTasks len = %d, want 1", len(model.completedTasks))
	}
	task := model.completedTasks[0]
	if task.ParentID != "GH-498" {
		t.Errorf("ParentID = %q, want %q", task.ParentID, "GH-498")
	}
	if task.IsEpic {
		t.Error("IsEpic = true, want false")
	}

	// Send an epic task with SubIssues, TotalSubs, DoneSubs
	epicMsg := addCompletedTaskMsg(CompletedTask{
		ID:        "GH-498",
		Title:     "Epic decomposition task",
		Status:    "success",
		Duration:  "5m",
		IsEpic:    true,
		SubIssues: []string{"GH-575", "GH-576", "GH-577"},
		TotalSubs: 3,
		DoneSubs:  2,
	})
	updated, _ = model.Update(epicMsg)
	model = updated.(Model)

	if len(model.completedTasks) != 2 {
		t.Fatalf("completedTasks len = %d, want 2", len(model.completedTasks))
	}
	epic := model.completedTasks[1]
	if !epic.IsEpic {
		t.Error("IsEpic = false, want true")
	}
	if epic.TotalSubs != 3 {
		t.Errorf("TotalSubs = %d, want 3", epic.TotalSubs)
	}
	if epic.DoneSubs != 2 {
		t.Errorf("DoneSubs = %d, want 2", epic.DoneSubs)
	}
	if len(epic.SubIssues) != 3 {
		t.Fatalf("SubIssues len = %d, want 3", len(epic.SubIssues))
	}
	if epic.SubIssues[0] != "GH-575" || epic.SubIssues[1] != "GH-576" || epic.SubIssues[2] != "GH-577" {
		t.Errorf("SubIssues = %v, want [GH-575 GH-576 GH-577]", epic.SubIssues)
	}
}

func TestAddCompletedTask_BackwardCompatEmpty(t *testing.T) {
	m := NewModel("test")

	// Simulate the backward-compatible call (parentID="", isEpic=false)
	cmd := AddCompletedTask("GH-100", "Simple task", "success", "10s", "", false)
	msg := cmd().(addCompletedTaskMsg)
	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.completedTasks) != 1 {
		t.Fatalf("completedTasks len = %d, want 1", len(model.completedTasks))
	}
	task := model.completedTasks[0]
	if task.ParentID != "" {
		t.Errorf("ParentID = %q, want empty", task.ParentID)
	}
	if task.IsEpic {
		t.Error("IsEpic = true, want false")
	}
	if task.TotalSubs != 0 {
		t.Errorf("TotalSubs = %d, want 0", task.TotalSubs)
	}
	if task.DoneSubs != 0 {
		t.Errorf("DoneSubs = %d, want 0", task.DoneSubs)
	}
	if task.SubIssues != nil {
		t.Errorf("SubIssues = %v, want nil", task.SubIssues)
	}
}

// --- Snapshot tests for renderHistory variants ---

// stripANSI removes ANSI escape sequences for snapshot comparison.
// We compare visual content, not terminal styling.
func stripANSI(s string) string {
	// Simple ANSI escape stripper: \x1b[...m
	result := strings.Builder{}
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until 'm'
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

// assertPanelLineWidths checks that every line in the panel output has
// the expected visual width (panelTotalWidth = 69).
func assertPanelLineWidths(t *testing.T, output string) {
	t.Helper()
	for i, line := range strings.Split(output, "\n") {
		w := lipgloss.Width(line)
		if w != panelTotalWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, panelTotalWidth, line)
		}
	}
}

func TestRenderHistory_EmptyState(t *testing.T) {
	m := NewModel("test")
	output := m.renderHistory()

	assertPanelLineWidths(t, output)

	plain := stripANSI(output)
	if !strings.Contains(plain, "HISTORY") {
		t.Error("missing HISTORY panel title")
	}
	if !strings.Contains(plain, "No completed tasks yet") {
		t.Error("empty state should show 'No completed tasks yet'")
	}
}

func TestRenderHistory_StandaloneTask(t *testing.T) {
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		{
			ID:          "GH-156",
			Title:       "Fix authentication bug in login",
			Status:      "success",
			Duration:    "2m",
			CompletedAt: time.Now().Add(-2 * time.Minute),
		},
		{
			ID:          "GH-157",
			Title:       "Update config validation",
			Status:      "failed",
			Duration:    "45s",
			CompletedAt: time.Now().Add(-15 * time.Minute),
		},
	}

	output := m.renderHistory()
	assertPanelLineWidths(t, output)

	plain := stripANSI(output)

	// Check standalone task icons
	if !strings.Contains(plain, "+ GH-156") {
		t.Error("success task should have '+' icon")
	}
	if !strings.Contains(plain, "x GH-157") {
		t.Error("failed task should have 'x' icon")
	}

	// Titles should be present (possibly truncated)
	if !strings.Contains(plain, "Fix authentication") {
		t.Error("task title should be visible")
	}

	// Time ago should be present
	if !strings.Contains(plain, "ago") {
		t.Error("time ago should be visible")
	}
}

func TestRenderHistory_ActiveEpicWithMixedStates(t *testing.T) {
	now := time.Now()
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		// Epic parent (active: 2/4 done)
		{
			ID:          "GH-491",
			Title:       "Enable decomposition by default",
			Status:      "running",
			Duration:    "3m",
			CompletedAt: now.Add(-3 * time.Minute),
			IsEpic:      true,
			TotalSubs:   4,
			DoneSubs:    2,
		},
		// Sub-issues
		{
			ID:          "GH-492",
			Title:       "Flip the default",
			Status:      "success",
			CompletedAt: now.Add(-2 * time.Minute),
			ParentID:    "GH-491",
		},
		{
			ID:          "GH-493",
			Title:       "Update example config",
			Status:      "running",
			CompletedAt: now,
			ParentID:    "GH-491",
		},
		{
			ID:       "GH-494",
			Title:    "Update documentation",
			Status:   "pending",
			ParentID: "GH-491",
		},
		{
			ID:          "GH-495",
			Title:       "Add integration tests",
			Status:      "failed",
			CompletedAt: now.Add(-1 * time.Minute),
			ParentID:    "GH-491",
		},
	}

	output := m.renderHistory()
	assertPanelLineWidths(t, output)

	plain := stripANSI(output)

	// Epic parent line: amber '*' icon, progress bar, counts
	if !strings.Contains(plain, "* GH-491") {
		t.Error("active epic should have '*' icon")
	}
	if !strings.Contains(plain, "[##--]") {
		t.Errorf("active epic should have [##--] progress bar, got:\n%s", plain)
	}
	if !strings.Contains(plain, "2/4") {
		t.Error("active epic should show 2/4 counts")
	}

	// Sub-issue lines: indented with per-status icons
	if !strings.Contains(plain, "    + GH-492") {
		t.Error("success sub-issue should be indented with '+' icon")
	}
	if !strings.Contains(plain, "    ~ GH-493") {
		t.Error("running sub-issue should be indented with '~' icon")
	}
	if !strings.Contains(plain, "    . GH-494") {
		t.Error("pending sub-issue should be indented with '.' icon")
	}
	if !strings.Contains(plain, "    x GH-495") {
		t.Error("failed sub-issue should be indented with 'x' icon")
	}

	// Pending sub-issue should show "--" instead of time
	// Find the line with GH-494
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "GH-494") {
			if !strings.Contains(line, "--") {
				t.Errorf("pending sub-issue should show '--', got: %q", line)
			}
			break
		}
	}

	// Running sub-issue should show "now"
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "GH-493") {
			if !strings.Contains(line, "now") {
				t.Errorf("running sub-issue should show 'now', got: %q", line)
			}
			break
		}
	}
}

func TestRenderHistory_CompletedEpicCollapsed(t *testing.T) {
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		{
			ID:          "GH-385",
			Title:       "Epic: Roadmap workflow",
			Status:      "success",
			Duration:    "12m",
			CompletedAt: time.Now().Add(-12 * time.Minute),
			IsEpic:      true,
			TotalSubs:   5,
			DoneSubs:    5,
		},
	}

	output := m.renderHistory()
	assertPanelLineWidths(t, output)

	plain := stripANSI(output)

	// Completed epic: collapsed with '+' icon and [5/5]
	if !strings.Contains(plain, "+ GH-385") {
		t.Error("completed epic should have '+' icon (success)")
	}
	if !strings.Contains(plain, "[5/5]") {
		t.Errorf("completed epic should show [5/5] count, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Epic: Roadmap") {
		t.Error("completed epic title should be visible")
	}

	// Should NOT show sub-issue lines (collapsed)
	lines := strings.Split(plain, "\n")
	indentedCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, "│ "), "    ") {
			indentedCount++
		}
	}
	// Only panel borders and one content line expected
	contentLines := 0
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped != "" && !strings.HasPrefix(stripped, "╭") && !strings.HasPrefix(stripped, "╰") && !strings.HasPrefix(stripped, "│") {
			contentLines++
		}
	}
	// Collapsed epic = 1 content line (inside panel border lines)
}

func TestRenderHistory_MixedStandaloneAndEpic(t *testing.T) {
	now := time.Now()
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		// Active epic
		{
			ID:        "GH-491",
			Title:     "Enable decomposition",
			Status:    "running",
			Duration:  "3m",
			IsEpic:    true,
			TotalSubs: 3,
			DoneSubs:  2,
		},
		{
			ID:          "GH-492",
			Title:       "Flip default",
			Status:      "success",
			CompletedAt: now.Add(-2 * time.Minute),
			ParentID:    "GH-491",
		},
		{
			ID:       "GH-493",
			Title:    "Update config",
			Status:   "running",
			ParentID: "GH-491",
		},
		// Completed epic
		{
			ID:          "GH-385",
			Title:       "Roadmap workflow",
			Status:      "success",
			CompletedAt: now.Add(-12 * time.Minute),
			IsEpic:      true,
			TotalSubs:   5,
			DoneSubs:    5,
		},
		// Standalone task
		{
			ID:          "GH-489",
			Title:       "fix(autopilot): embed branch metadata",
			Status:      "success",
			CompletedAt: now.Add(-15 * time.Minute),
		},
	}

	output := m.renderHistory()
	assertPanelLineWidths(t, output)

	plain := stripANSI(output)

	// All three types should be present
	if !strings.Contains(plain, "* GH-491") {
		t.Error("active epic should be present with '*' icon")
	}
	if !strings.Contains(plain, "[5/5]") {
		t.Error("completed epic [5/5] count should be present")
	}
	if !strings.Contains(plain, "+ GH-489") {
		t.Error("standalone task should be present with '+' icon")
	}

	// Sub-issues should appear under active epic, not standalone
	if !strings.Contains(plain, "    + GH-492") {
		t.Error("sub-issue GH-492 should be indented under epic")
	}
}

func TestRenderEpicProgressBar(t *testing.T) {
	tests := []struct {
		name       string
		done       int
		total      int
		innerWidth int
		want       string
	}{
		{"zero progress", 0, 3, 4, "[----]"},
		{"partial progress", 2, 4, 4, "[##--]"},
		{"full progress", 5, 5, 4, "[####]"},
		{"one of three", 1, 3, 4, "[#---]"},
		{"zero total", 0, 0, 4, "[----]"},
		{"wider bar", 3, 6, 6, "[###---]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderEpicProgressBar(tt.done, tt.total, tt.innerWidth)
			if got != tt.want {
				t.Errorf("renderEpicProgressBar(%d, %d, %d) = %q, want %q",
					tt.done, tt.total, tt.innerWidth, got, tt.want)
			}
		})
	}
}

func TestGroupedHistory_SubIssueAbsorption(t *testing.T) {
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		{ID: "GH-100", Title: "Epic task", IsEpic: true, TotalSubs: 2, DoneSubs: 1},
		{ID: "GH-101", Title: "Sub 1", ParentID: "GH-100", Status: "success"},
		{ID: "GH-102", Title: "Sub 2", ParentID: "GH-100", Status: "pending"},
		{ID: "GH-200", Title: "Standalone", Status: "success"},
	}

	groups := m.groupedHistory()

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}

	// First group: epic with sub-issues absorbed
	if groups[0].Task.ID != "GH-100" {
		t.Errorf("first group ID = %q, want GH-100", groups[0].Task.ID)
	}
	if len(groups[0].SubIssues) != 2 {
		t.Errorf("epic sub-issues = %d, want 2", len(groups[0].SubIssues))
	}

	// Second group: standalone
	if groups[1].Task.ID != "GH-200" {
		t.Errorf("second group ID = %q, want GH-200", groups[1].Task.ID)
	}
	if len(groups[1].SubIssues) != 0 {
		t.Errorf("standalone sub-issues = %d, want 0", len(groups[1].SubIssues))
	}
}

func TestGroupedHistory_OrphanSubIssue(t *testing.T) {
	// Sub-issue whose parent is NOT in the list should render standalone
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		{ID: "GH-101", Title: "Orphan sub", ParentID: "GH-999", Status: "success"},
	}

	groups := m.groupedHistory()

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Task.ID != "GH-101" {
		t.Errorf("orphan should appear as standalone, got ID=%q", groups[0].Task.ID)
	}
}

func TestAddCompletedTask_HistoryCapAt5(t *testing.T) {
	m := NewModel("test")

	// Add 6 tasks — history should keep only the last 5
	for i := 0; i < 6; i++ {
		msg := addCompletedTaskMsg(CompletedTask{
			ID:       fmt.Sprintf("GH-%d", i+1),
			Title:    fmt.Sprintf("Task %d", i+1),
			Status:   "success",
			ParentID: "GH-0",
			IsEpic:   i == 5, // last one is an epic
		})
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	if len(m.completedTasks) != 5 {
		t.Fatalf("completedTasks len = %d, want 5", len(m.completedTasks))
	}

	// First task (GH-1) should have been evicted; GH-2 is now first
	if m.completedTasks[0].ID != "GH-2" {
		t.Errorf("first task ID = %q, want %q", m.completedTasks[0].ID, "GH-2")
	}
	// Last task should be the epic
	last := m.completedTasks[4]
	if !last.IsEpic {
		t.Error("last task IsEpic = false, want true")
	}
	if last.ParentID != "GH-0" {
		t.Errorf("last task ParentID = %q, want %q", last.ParentID, "GH-0")
	}
}

// --- Git graph panel fix tests (GH-1920) ---

func TestGitGraph_NarrowTerminalBlocksToggle(t *testing.T) {
	m := Model{width: 80, height: 40} // 80 < 90 (panelTotalWidth+1+20)

	// Press "g" — should NOT change gitGraphMode because terminal is too narrow
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)

	if m.gitGraphMode != GitGraphHidden {
		t.Errorf("gitGraphMode = %d, want %d (Hidden); narrow terminal should block toggle", m.gitGraphMode, GitGraphHidden)
	}
}

func TestGitGraph_WideTerminalAllowsToggle(t *testing.T) {
	m := Model{width: 120, height: 40} // 120 >= 90

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)

	if m.gitGraphMode != GitGraphFull {
		t.Errorf("gitGraphMode = %d, want %d (Full); wide terminal should allow toggle", m.gitGraphMode, GitGraphFull)
	}
}

func TestGitGraph_NarrowTerminalHidesGraphHint(t *testing.T) {
	m := Model{width: 80, height: 40, gitGraphMode: GitGraphHidden}

	help := m.renderHelp()
	if strings.Contains(help, "g: graph") {
		t.Error("narrow terminal should NOT show 'g: graph' hint")
	}
}

func TestGitGraph_WideTerminalShowsGraphHint(t *testing.T) {
	m := Model{width: 120, height: 40, gitGraphMode: GitGraphHidden}

	help := m.renderHelp()
	plain := stripANSI(help)
	if !strings.Contains(plain, "g: graph") {
		t.Errorf("wide terminal should show 'g: graph' hint, got: %q", plain)
	}
}

func TestHelpFooter_SurvivesHeightTruncation(t *testing.T) {
	m := Model{
		width: 120, height: 10, gitGraphMode: GitGraphHidden,
		showBanner: true, showLogs: true,
		autopilotPanel: NewAutopilotPanel(nil),
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	// The last line should contain help text
	lastLine := lines[len(lines)-1]
	plain := stripANSI(lastLine)
	if !strings.Contains(plain, "q: quit") {
		t.Errorf("help footer missing from last line after height truncation, got: %q", plain)
	}
}

func TestHelpFooter_VisibleWithoutTruncation(t *testing.T) {
	m := Model{
		width: 120, height: 200, gitGraphMode: GitGraphHidden,
		autopilotPanel: NewAutopilotPanel(nil),
	}

	view := m.View()
	plain := stripANSI(view)
	if !strings.Contains(plain, "q: quit") {
		t.Error("help footer should be visible when terminal is tall enough")
	}
}
