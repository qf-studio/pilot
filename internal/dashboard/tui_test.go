package dashboard

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/qf-studio/grom/pkg/tui/render"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
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
		{1_000_000_000, "1.0B"},
		{6_745_700_000, "6.7B"},
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

func TestBuildStatCard(t *testing.T) {
	card := buildStatCard("test", "42", "detail", []float64{1, 2, 3, 4, 5, 6, 7}, gromTheme.Accent, cardWidth)

	lines := strings.Split(card, "\n")
	if len(lines) != statCardHeight {
		t.Errorf("card height = %d lines, want %d", len(lines), statCardHeight)
	}
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != cardWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, cardWidth, line)
		}
	}

	// Check grom card chrome: rounded borders + lowercase title in top border
	if !strings.Contains(card, "╭") {
		t.Error("missing top-left border ╭")
	}
	if !strings.Contains(card, "╰") {
		t.Error("missing bottom-left border ╰")
	}
	if !strings.Contains(card, "│") {
		t.Error("missing side border │")
	}
	if !strings.Contains(card, "test") {
		t.Error("missing lowercase title in top border")
	}
}

// Logs is the flex panel: in full-width / side-by-side layouts it stretches
// so its bottom border sits directly above the help footer instead of a
// ghost-line void; the log tail grows to fill the space.
func TestLogsPanelFlexHeight(t *testing.T) {
	m := NewModel("test")
	m.width = 100
	m.height = 40
	m.logs = []string{"alpha", "beta"}

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != m.height {
		t.Fatalf("View() height = %d lines, want %d", len(lines), m.height)
	}
	// Last line is the help footer; the line above it must be the logs
	// panel's bottom border, not blank padding.
	if !strings.Contains(lines[m.height-2], "╰") {
		t.Errorf("expected logs bottom border above help footer, got %q", lines[m.height-2])
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Error("log lines missing from flexed panel")
	}
}

// In stacked mode (graph visible, terminal too narrow for side-by-side) the
// logs panel stays content-sized so the git graph below keeps its height.
func TestLogsPanelStackedStaysCompact(t *testing.T) {
	m := NewModel("test")
	m.width = 60 // < panelTotalWidth+1+20 → stacked once the graph is visible
	m.height = 50
	m.logs = []string{"alpha"}

	m.gitGraphMode = GitGraphVisible
	stacked := strings.Count(m.renderDashboard(), "\n")
	m.gitGraphMode = GitGraphHidden
	flexed := strings.Count(m.renderDashboard(), "\n")

	if stacked >= flexed {
		t.Errorf("stacked dashboard (%d lines) should be shorter than flexed (%d)", stacked, flexed)
	}
}

// TASK-390: two-series stacked variant (tokens cached/fresh, queue ✓/✗)
// keeps the exact card geometry of buildStatCard and fills the band even
// with a 7-point history (BrailleStacked right-aligns unstretched series).
func TestBuildStatCardStacked(t *testing.T) {
	card := buildStatCardStacked("test", "42", "detail",
		[][]float64{{10, 20, 30, 40, 30, 20, 10}, {1, 2, 3, 4, 3, 2, 1}},
		[]string{render.Dim(gromTheme.Accent, 0.45), gromTheme.Accent}, cardWidth)

	lines := strings.Split(card, "\n")
	if len(lines) != statCardHeight {
		t.Errorf("card height = %d lines, want %d", len(lines), statCardHeight)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != cardWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, cardWidth, line)
		}
	}

	// Chart rows must contain braille cells across the band, including the
	// left edge — a right-aligned (unstretched) series would leave it blank.
	chartRow := lines[statCardHeight-2] // last inner line above bottom border
	plain := stripANSI(chartRow)
	inner := strings.TrimSuffix(strings.TrimPrefix(plain, "│"), "│")
	if strings.TrimSpace(inner) == "" {
		t.Fatalf("bottom chart row is blank: %q", plain)
	}
	if strings.HasPrefix(strings.TrimPrefix(inner, " "), "      ") {
		t.Errorf("chart band left edge blank — series not stretched: %q", plain)
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

	if !strings.Contains(output, "tokens") {
		t.Error("output missing tokens card")
	}
	if !strings.Contains(output, "cost") {
		t.Error("output missing cost card")
	}
	if !strings.Contains(output, "queue") {
		t.Error("output missing queue card")
	}
	// GH-4735: cost card detail line carries the rolling window length.
	if !strings.Contains(output, "30d") {
		t.Errorf("output missing windowed cost detail (%q): %s", "30d", output)
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
	if !strings.Contains(output, "tokens") {
		t.Error("zero-state output missing tokens card")
	}
	if !strings.Contains(output, "cost") {
		t.Error("zero-state output missing cost card")
	}
	if !strings.Contains(output, "queue") {
		t.Error("zero-state output missing queue card")
	}
}

func TestRenderTokenCard_CacheBreakdown(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		TotalTokens:      10_000, // input+output
		InputTokens:      7_000,
		OutputTokens:     3_000,
		CacheReadTokens:  90_000, // dominates throughput
		CacheWriteTokens: 5_000,
		TokenHistory:     []int64{100, 200, 300, 400, 500, 600, 700},
	}

	out := m.renderTokenCard(cardWidth)

	// Grand total = 10_000 + 90_000 + 5_000 = 105_000
	if !strings.Contains(out, "105.0K") {
		t.Errorf("renderTokenCard: expected grand total 105.0K in output, got:\n%s", out)
	}
	// Detail = cache read + write (95_000), matching the headline's
	// composition (GH-5192) — not CacheReadTokens alone.
	if !strings.Contains(out, "95.0K cached") {
		t.Errorf("renderTokenCard: expected '95.0K cached' detail in output, got:\n%s", out)
	}
	// Window label: full "· all-time" on wide cards, terse "· all" once the
	// card is too narrow to fit the whole word — either way "· all" is a
	// substring, so this assertion holds regardless of card width.
	if !strings.Contains(out, "· all") {
		t.Errorf("renderTokenCard: expected a '· all[-time]' window label in output, got:\n%s", out)
	}
}

// TestRenderTokenCard_ProductionScale guards GH-5197: at production
// (billions-of-tokens) magnitude, formatCompact used to top out at an "M"
// tier, so "6745.7M cached" (14 chars) plus the " · all" suffix (6 chars)
// blew the 19-char detail budget (cardWidth 23, inner width cw-4) and the
// window label silently dropped — K-scale fixtures like
// TestRenderTokenCard_CacheBreakdown never caught this because they never
// crossed the M/B boundary.
func TestRenderTokenCard_ProductionScale(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		TotalTokens:      100_000_000,
		InputTokens:      70_000_000,
		OutputTokens:     30_000_000,
		CacheReadTokens:  6_700_000_000,
		CacheWriteTokens: 45_700_000,
		TokenHistory:     []int64{100, 200, 300, 400, 500, 600, 700},
	}

	out := m.renderTokenCard(cardWidth)

	// Cache total = 6_700_000_000 + 45_700_000 = 6_745_700_000 -> "6.7B".
	if !strings.Contains(out, "6.7B cached") {
		t.Errorf("renderTokenCard production-scale: expected '6.7B cached' detail in output, got:\n%s", out)
	}
	// The window label must survive at this magnitude, not be width-dropped.
	if !strings.Contains(out, "· all") {
		t.Errorf("renderTokenCard production-scale: expected a '· all[-time]' window label in output, got:\n%s", out)
	}
}

func TestRenderTokenCard_ZeroCacheTokens(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		TotalTokens:  50_000,
		InputTokens:  30_000,
		OutputTokens: 20_000,
		// CacheReadTokens and CacheWriteTokens both zero
		TokenHistory: []int64{100, 200, 300, 400, 500, 600, 700},
	}

	out := m.renderTokenCard(cardWidth)

	// When cache is zero, grand total == TotalTokens
	if !strings.Contains(out, "50.0K") {
		t.Errorf("renderTokenCard zero-cache: expected 50.0K total, got:\n%s", out)
	}
	if !strings.Contains(out, "0 cached") {
		t.Errorf("renderTokenCard zero-cache: expected '0 cached' detail, got:\n%s", out)
	}
}

func TestRenderTaskCard_ShowsQueueDepth(t *testing.T) {
	m := NewModel("test")
	// Simulate 10 lifetime tasks (succeeded + failed) in metrics
	m.metricsCard.TotalTasks = 10
	m.metricsCard.Succeeded = 8
	m.metricsCard.Failed = 2

	// Simulate 2 active tasks in queue (pending/running) alongside terminal
	// tasks retained since daemon start (GH-4617: Monitor never evicts
	// completed/failed/no-op tasks, so m.tasks accumulates lifetime history).
	// The card value must count only the active ones (2), not len(m.tasks) (5).
	m.tasks = []TaskDisplay{
		{ID: "1", Title: "Task A", Status: QueueStatusRunning},
		{ID: "2", Title: "Task B", Status: QueueStatusPending},
		{ID: "3", Title: "Task C", Status: QueueStatusDone},
		{ID: "4", Title: "Task D", Status: QueueStatusFailed},
		{ID: "5", Title: "Task E", Status: QueueStatusNoOp},
	}

	output := m.renderTaskCard(cardWidth)

	// queue card value must show current queue depth (2), not lifetime total (10)
	// and not len(m.tasks) (5, once terminal tasks are included in the fixture)
	if !strings.Contains(output, "queue") {
		t.Error("output missing queue title")
	}
	// The main value "2" should appear (queue depth)
	if !strings.Contains(output, "2") {
		t.Error("queue card should show current queue depth of 2")
	}
	// Lifetime total "10" should NOT appear as the main value
	if strings.Contains(output, "10") {
		t.Error("queue card should not show lifetime total (10)")
	}
	// len(m.tasks) is 5 (2 active + 3 terminal); the pre-fix implementation
	// rendered this as the value instead of the active count.
	if strings.Contains(output, "5") {
		t.Error("queue card should not show len(m.tasks) (5), only active task count")
	}
	// Succeeded/failed detail line should still be present
	if !strings.Contains(output, "✓ 8") {
		t.Error("queue card missing succeeded count")
	}
	if !strings.Contains(output, "✗ 2") {
		t.Error("queue card missing failed count")
	}
}

func TestRenderTaskCard_EmptyQueue(t *testing.T) {
	m := NewModel("test")
	// Historical tasks exist but queue is empty
	m.metricsCard.TotalTasks = 5
	m.metricsCard.Succeeded = 3
	m.metricsCard.Failed = 2
	m.tasks = nil

	output := m.renderTaskCard(cardWidth)

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

	// Metrics card should reflect lifetime token totals from executions, not
	// session (zero). TotalCostUSD is GH-4735-windowed (rolling 30d default),
	// but all rows above default to CreatedAt=now, so they fall inside the
	// window and the value matches the lifetime sum.
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

// TestHydrateFromStore_WindowedStatsExcludeOldRows is the GH-4735 regression:
// hydrateFromStore's cost/task-count headline numbers must be windowed
// (rolling m.statsWindowDays), not lifetime — an execution older than the
// window must not contribute to TotalCostUSD/TotalTasks even though it still
// counts toward GetLifetimeTokens.
func TestHydrateFromStore_WindowedStatsExcludeOldRows(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-window-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	oldTime := time.Now().AddDate(0, 0, -60)
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-old", TaskID: "TASK-OLD", ProjectPath: "/test",
		Status: "completed", CreatedAt: oldTime,
	}); err != nil {
		t.Fatalf("SaveExecution exec-old: %v", err)
	}
	if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID: "exec-old", TokensInput: 1000, TokensOutput: 500,
		TokensTotal: 1500, EstimatedCostUSD: 5.00,
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics exec-old: %v", err)
	}

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-new", TaskID: "TASK-NEW", ProjectPath: "/test", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution exec-new: %v", err)
	}
	if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID: "exec-new", TokensInput: 100, TokensOutput: 50,
		TokensTotal: 150, EstimatedCostUSD: 0.25,
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics exec-new: %v", err)
	}

	m := NewModelWithStore("test", store)

	// TotalTokens is lifetime — both rows count.
	if want := 1650; m.metricsCard.TotalTokens != want {
		t.Errorf("TotalTokens = %d, want %d (lifetime, both rows)", m.metricsCard.TotalTokens, want)
	}
	// TotalCostUSD/TotalTasks are windowed (default 30d) — only exec-new counts.
	if want := 0.25; math.Abs(m.metricsCard.TotalCostUSD-want) > 0.001 {
		t.Errorf("TotalCostUSD = %.4f, want %.4f (windowed, excludes 60d-old row)", m.metricsCard.TotalCostUSD, want)
	}
	if want := 1; m.metricsCard.TotalTasks != want {
		t.Errorf("TotalTasks = %d, want %d (windowed, excludes 60d-old row)", m.metricsCard.TotalTasks, want)
	}
}

// TestHydrateFromStore_StaleLedgerShowsBanner is the GH-4569 regression: a
// ledger whose newest execution is far older than the staleness threshold
// must surface a warning in the dashboard header, not just answer queries
// silently with stale data.
func TestHydrateFromStore_StaleLedgerShowsBanner(t *testing.T) {
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

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/test", Status: "completed",
		CreatedAt: time.Now().Add(-10 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewModelWithStore("test", store)
	if m.stalenessBanner == "" {
		t.Fatal("expected stalenessBanner to be set for a 10-day-old ledger")
	}
	if !strings.Contains(m.stalenessBanner, "LEDGER STALE") {
		t.Errorf("stalenessBanner = %q, want it to mention LEDGER STALE", m.stalenessBanner)
	}

	header := m.renderChromeHeader()
	if !strings.Contains(header, "ledger stale") {
		t.Errorf("renderChromeHeader() does not surface the stale-ledger card; header:\n%s", header)
	}
}

// TestHydrateFromStore_FreshLedgerNoBanner is the healthy-path counterpart:
// a recently-written ledger must not show any staleness banner.
func TestHydrateFromStore_FreshLedgerNoBanner(t *testing.T) {
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

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/test", Status: "completed",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewModelWithStore("test", store)
	if m.stalenessBanner != "" {
		t.Fatalf("expected no stalenessBanner for a fresh ledger, got %q", m.stalenessBanner)
	}
}

// TestHydrateFromStore_HealsFrozenLadderRow is the GH-4368 regression: a row
// that predates the H4 heal fixes (GH-4277/GH-4298) sits at status='completed'
// with its execution_events ladder frozen at a non-terminal rung (e.g.
// "running") because the event stream died mid-incident before the terminal
// event was ever recorded. hydrateFromStore's one-shot archaeology heal must
// backfill the missing terminal event before the HISTORY panel reads the
// event timeline, so the row's label reflects reality instead of staying
// frozen forever.
func TestHydrateFromStore_HealsFrozenLadderRow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-heal-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-frozen", TaskID: "GH-4278", ProjectPath: "/test",
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/4278",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	// Ladder frozen mid-flight — the daemon died before the terminal event
	// was ever written, matching the GH-4278 evidence in the issue.
	if err := store.InsertExecutionEvent("exec-frozen", memory.StageRunning, "seed"); err != nil {
		t.Fatalf("seed InsertExecutionEvent: %v", err)
	}

	m := NewModelWithStore("test", store)

	if len(m.completedTasks) != 1 {
		t.Fatalf("expected 1 completed task, got %d", len(m.completedTasks))
	}
	got := m.completedTasks[0].Stage
	if !got.Known || got.Label != "merged" {
		t.Errorf("frozen row after hydration heal: got %+v, want Known=true Label=%q", got, "merged")
	}
}

// TestHydrateFromStore_OldRowsCacheZero verifies that executions saved without
// cache token fields (simulating pre-GH-3615 rows) result in zero cache token
// counts in the metricsCard — i.e., the migration DEFAULT 0 is honoured.
func TestHydrateFromStore_OldRowsCacheZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-cache-zero-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Save execution without cache token fields (they default to 0)
	if err := store.SaveExecution(&memory.Execution{
		ID: "old-exec", TaskID: "TASK-OLD", ProjectPath: "/test", Status: "completed",
		TokensInput: 10000, TokensOutput: 5000, TokensTotal: 15000,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewModelWithStore("test", store)

	if m.metricsCard.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0 for old-style row", m.metricsCard.CacheReadTokens)
	}
	if m.metricsCard.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 for old-style row", m.metricsCard.CacheWriteTokens)
	}
	if m.metricsCard.TotalTokens != 15000 {
		t.Errorf("TotalTokens = %d, want 15000", m.metricsCard.TotalTokens)
	}
}

// TestHydrateFromStore_CacheTokensRoundTrip verifies that executions saved with
// non-zero cache token fields are reflected correctly in metricsCard.
func TestHydrateFromStore_CacheTokensRoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-cache-rt-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []struct {
		id         string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
	}{
		{"exec-cache-1", 5000, 2000, 80000, 3000},
		{"exec-cache-2", 3000, 1000, 40000, 2000},
	}
	for _, e := range execs {
		if err := store.SaveExecution(&memory.Execution{
			ID: e.id, TaskID: "TASK-" + e.id, ProjectPath: "/test", Status: "completed",
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      e.input + e.output,
			TokensCacheRead:  e.cacheRead,
			TokensCacheWrite: e.cacheWrite,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
	}

	m := NewModelWithStore("test", store)

	wantCacheRead := 120000 // 80000 + 40000
	wantCacheWrite := 5000  // 3000 + 2000
	wantTotal := 11000      // (5000+2000) + (3000+1000)

	if m.metricsCard.CacheReadTokens != wantCacheRead {
		t.Errorf("CacheReadTokens = %d, want %d", m.metricsCard.CacheReadTokens, wantCacheRead)
	}
	if m.metricsCard.CacheWriteTokens != wantCacheWrite {
		t.Errorf("CacheWriteTokens = %d, want %d", m.metricsCard.CacheWriteTokens, wantCacheWrite)
	}
	if m.metricsCard.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", m.metricsCard.TotalTokens, wantTotal)
	}

	// Verify the grand total in the rendered card includes cache tokens
	out := m.renderTokenCard(cardWidth)
	grandTotal := wantTotal + wantCacheRead + wantCacheWrite // 11000+120000+5000=136000
	wantStr := formatCompact(grandTotal)
	if !strings.Contains(out, wantStr) {
		t.Errorf("renderTokenCard: expected grand total %s in output, got:\n%s", wantStr, out)
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
	// GH-4829: CostPerTask now only ever carries the store's CostPerDelivered
	// value (set by hydrateFromStore/GetWindowedStats) — pin a sentinel here
	// so the assertion below fails if updateTokensMsg recomputes it.
	m.metricsCard.CostPerTask = 42.0
	m.metricsCard.TotalTasks = 3

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
	// GH-4829: CostPerTask must NOT be recomputed from TotalCostUSD/TotalTasks.
	if model.metricsCard.CostPerTask != 42.0 {
		t.Errorf("CostPerTask = %v, want unchanged 42.0 (must not be recomputed live)", model.metricsCard.CostPerTask)
	}
}

func TestAddCompletedTask_NewFieldsStored(t *testing.T) {
	m := NewModel("test")
	// GH-4829: pin a sentinel CostPerTask so we can assert addCompletedTaskMsg
	// does not recompute it from TotalCostUSD/TotalTasks.
	m.metricsCard.CostPerTask = 42.0
	m.metricsCard.TotalCostUSD = 10.0

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
	// GH-4829: CostPerTask must NOT be recomputed from TotalCostUSD/TotalTasks.
	if model.metricsCard.CostPerTask != 42.0 {
		t.Errorf("CostPerTask = %v, want unchanged 42.0 (must not be recomputed live)", model.metricsCard.CostPerTask)
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
	if !strings.Contains(plain, "history") {
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

	// Check standalone task glyphs
	if !strings.Contains(plain, "✓ GH-156") {
		t.Error("success task should have '✓' glyph")
	}
	if !strings.Contains(plain, "✗ GH-157") {
		t.Error("failed task should have '✗' glyph")
	}

	// Titles should be present (possibly truncated)
	if !strings.Contains(plain, "Fix auth") {
		t.Error("task title should be visible")
	}

	// Time ago should be present
	if !strings.Contains(plain, "ago") {
		t.Error("time ago should be visible")
	}
}

// TestRenderHistory_StageStrip verifies the 7-rung pipeline segment meter
// (GH-3849; grom restyle) renders with its stage label in a fixed column for
// happy path, in-progress, and failed-at-stage-N executions, and that the
// fixed card width invariant holds regardless of stage state.
func TestRenderHistory_StageStrip(t *testing.T) {
	m := NewModel("test")
	m.completedTasks = []CompletedTask{
		{
			ID:          "GH-200",
			Title:       "Happy path task",
			Status:      "success",
			Duration:    "3m",
			CompletedAt: time.Now().Add(-3 * time.Minute),
			Stage: buildStageInfo([]*memory.Event{
				evt(memory.StageQueued),
				evt(memory.StageSpecValidated),
				evt(memory.StageRunning),
				evt(memory.StageCommit),
				evt(memory.StagePRCreated),
				evt(memory.StageMerged),
			}, false),
		},
		{
			ID:          "GH-201",
			Title:       "In progress task",
			Status:      "success",
			Duration:    "1m",
			CompletedAt: time.Now().Add(-1 * time.Minute),
			Stage: buildStageInfo([]*memory.Event{
				evt(memory.StageQueued),
				evt(memory.StageSpecValidated),
				evt(memory.StageRunning),
			}, false),
		},
		{
			ID:          "GH-202",
			Title:       "Failed at stage N task",
			Status:      "failed",
			Duration:    "45s",
			CompletedAt: time.Now().Add(-10 * time.Minute),
			Stage: buildStageInfo([]*memory.Event{
				evt(memory.StageQueued),
				evt(memory.StageSpecValidated),
				evt(memory.StageRunning),
				evt(memory.StageFailed),
			}, true),
		},
		// No stage evidence (pre-events execution): dim track + "–" label.
		{
			ID:          "GH-203",
			Title:       "Legacy execution",
			Status:      "success",
			CompletedAt: time.Now().Add(-20 * time.Minute),
		},
	}

	output := m.renderHistory()
	assertPanelLineWidths(t, output)

	plain := stripANSI(output)

	// Stage labels render in the fixed column after the 7-cell meter.
	for _, want := range []string{"■■■■■■■ merged", "■■■■■■■ running", "■■■■■■■ –"} {
		if !strings.Contains(plain, want) {
			t.Errorf("stage meter+label %q not found in output:\n%s", want, plain)
		}
	}
	// The N/7 fraction text is gone.
	if strings.Contains(plain, "/7") {
		t.Errorf("fraction text should be replaced by the segment meter:\n%s", plain)
	}
	// Failed run carries the ✗ status glyph.
	if !strings.Contains(plain, "✗ GH-202") {
		t.Errorf("failed run should carry the ✗ glyph:\n%s", plain)
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

	// Epic parent line: ● active glyph, segment meter, counts
	if !strings.Contains(plain, "● GH-491") {
		t.Error("active epic should have '●' glyph")
	}
	if !strings.Contains(plain, "■■■■ 2/4") {
		t.Errorf("active epic should have a 4-cell segment meter with counts, got:\n%s", plain)
	}

	// Sub-issue lines: indented with per-status glyphs
	if !strings.Contains(plain, "    ✓ GH-492") {
		t.Error("success sub-issue should be indented with '✓' glyph")
	}
	if !strings.Contains(plain, "    ● GH-493") {
		t.Error("running sub-issue should be indented with '●' glyph")
	}
	if !strings.Contains(plain, "    ◌ GH-494") {
		t.Error("pending sub-issue should be indented with '◌' glyph")
	}
	if !strings.Contains(plain, "    ✗ GH-495") {
		t.Error("failed sub-issue should be indented with '✗' glyph")
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

	// Completed epic: collapsed with '✓' glyph and 5/5 count
	if !strings.Contains(plain, "✓ GH-385") {
		t.Error("completed epic should have '✓' glyph (success)")
	}
	if !strings.Contains(plain, "5/5") {
		t.Errorf("completed epic should show 5/5 count, got:\n%s", plain)
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
	if !strings.Contains(plain, "● GH-491") {
		t.Error("active epic should be present with '●' glyph")
	}
	if !strings.Contains(plain, "5/5") {
		t.Error("completed epic 5/5 count should be present")
	}
	if !strings.Contains(plain, "✓ GH-489") {
		t.Error("standalone task should be present with '✓' glyph")
	}

	// Sub-issues should appear under active epic, not standalone
	if !strings.Contains(plain, "    ✓ GH-492") {
		t.Error("sub-issue GH-492 should be indented under epic")
	}
}

func TestRenderEpicProgressBar(t *testing.T) {
	// The meter is styled (filled gradient vs track color), so plain text is
	// innerWidth ■ cells in every state — assert the visual-width invariant.
	// Fill-fraction styling itself is covered by grom's SegmentMeter tests
	// (and colors are stripped in the no-TTY test environment anyway).
	tests := []struct {
		name       string
		done       int
		total      int
		innerWidth int
	}{
		{"zero progress", 0, 3, 4},
		{"partial progress", 2, 4, 4},
		{"full progress", 5, 5, 4},
		{"zero total", 0, 0, 4},
		{"wider bar", 3, 6, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderEpicProgressBar(tt.done, tt.total, tt.innerWidth)
			if w := lipgloss.Width(got); w != tt.innerWidth {
				t.Errorf("renderEpicProgressBar(%d, %d, %d) visual width = %d, want %d",
					tt.done, tt.total, tt.innerWidth, w, tt.innerWidth)
			}
			if plain := stripANSI(got); plain != strings.Repeat("■", tt.innerWidth) {
				t.Errorf("renderEpicProgressBar(%d, %d, %d) cells = %q, want %d ■ cells",
					tt.done, tt.total, tt.innerWidth, plain, tt.innerWidth)
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

// --- Help footer truncation fix tests ---

func TestGitGraph_ToggleAlwaysWorks(t *testing.T) {
	// "g" should cycle gitGraphMode regardless of terminal width
	for _, width := range []int{80, 120} {
		m := Model{width: width, height: 40}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		m = updated.(Model)
		if m.gitGraphMode != GitGraphVisible {
			t.Errorf("width=%d: gitGraphMode = %d, want %d (Full)", width, m.gitGraphMode, GitGraphVisible)
		}
	}
}

func TestHelpFooter_AlwaysShowsGraphHint(t *testing.T) {
	// "g: graph" should appear in help regardless of terminal width
	for _, width := range []int{80, 120} {
		m := Model{width: width, height: 40, gitGraphMode: GitGraphHidden}
		plain := stripANSI(m.renderHelp())
		if !strings.Contains(plain, "g: graph") {
			t.Errorf("width=%d: help should show 'g: graph', got: %q", width, plain)
		}
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

// --- Responsive stacked git graph tests ---

func TestGitGraph_StackedLayoutUsesFullWidth(t *testing.T) {
	// On narrow terminal (<90 cols), graph should stack below dashboard at full terminal width
	m := Model{
		width: 80, height: 40, gitGraphMode: GitGraphVisible,
		autopilotPanel: NewAutopilotPanel(nil),
		gitGraphState: &GitGraphState{
			Lines: []GitGraphLine{
				{GraphChars: "●", SHA: "abc1234", Author: "Test", Message: "Initial commit"},
				{GraphChars: "●", SHA: "def5678", Author: "Test", Message: "Second commit"},
			},
		},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find the git graph panel top border in the stacked output
	var graphBorderLine string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "git graph") && strings.Contains(plain, "╭") {
			graphBorderLine = plain
			break
		}
	}
	if graphBorderLine == "" {
		t.Fatal("stacked graph panel not found in narrow terminal output")
	}

	// The graph panel border should span close to full terminal width (80), not panelTotalWidth (69)
	borderWidth := lipgloss.Width(graphBorderLine)
	if borderWidth <= panelTotalWidth {
		t.Errorf("stacked graph width = %d, want > %d (panelTotalWidth); should use full terminal width", borderWidth, panelTotalWidth)
	}
	if borderWidth != m.width {
		t.Errorf("stacked graph width = %d, want %d (m.width)", borderWidth, m.width)
	}
}

func TestGitGraph_SideBySideOnWideTerminal(t *testing.T) {
	// On wide terminal (≥90 cols), graph renders side-by-side
	m := Model{
		width: 120, height: 40, gitGraphMode: GitGraphVisible,
		autopilotPanel: NewAutopilotPanel(nil),
		gitGraphState: &GitGraphState{
			Lines: []GitGraphLine{
				{GraphChars: "●", SHA: "abc1234", Author: "Test", Message: "Initial commit"},
			},
		},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	// In side-by-side mode, the git graph border should NOT be at full terminal width
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "git graph") && strings.Contains(plain, "╭") {
			borderWidth := lipgloss.Width(plain)
			if borderWidth == m.width {
				t.Errorf("side-by-side graph should not be full terminal width (%d)", m.width)
			}
			break
		}
	}
}

func TestGitGraph_StackedHelpFooterVisible(t *testing.T) {
	// Help footer must be visible at bottom even when graph is stacked
	m := Model{
		width: 75, height: 30, gitGraphMode: GitGraphVisible,
		autopilotPanel: NewAutopilotPanel(nil),
		gitGraphState: &GitGraphState{
			Lines: []GitGraphLine{
				{GraphChars: "●", SHA: "abc1234", Author: "Test", Message: "commit"},
			},
		},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	lastLine := lines[len(lines)-1]
	plain := stripANSI(lastLine)
	if !strings.Contains(plain, "q: quit") {
		t.Errorf("help footer missing from stacked layout, last line: %q", plain)
	}
}

func TestGitGraph_NarrowTerminalNotSilent(t *testing.T) {
	// On narrow terminal with graph enabled, pressing 'g' should produce visible graph output
	m := Model{
		width: 60, height: 30, gitGraphMode: GitGraphVisible,
		autopilotPanel: NewAutopilotPanel(nil),
		gitGraphState: &GitGraphState{
			Lines: []GitGraphLine{
				{GraphChars: "●", SHA: "abc1234", Author: "Test", Message: "Initial commit"},
			},
		},
	}

	view := m.View()
	plain := stripANSI(view)
	// At 60 cols stacked, auto-size picks medium (title "GIT")
	if !strings.Contains(plain, "git") {
		t.Error("narrow terminal (60 cols) should show stacked GIT panel, got silent/empty")
	}
}

func TestDashboardPanels_StretchInStackedMode(t *testing.T) {
	// GH-1909: In stacked mode, dashboard panels should stretch to full terminal width,
	// matching the git graph panel width for visual consistency.
	m := Model{
		width: 80, height: 40, gitGraphMode: GitGraphVisible,
		autopilotPanel: NewAutopilotPanel(nil),
		gitGraphState: &GitGraphState{
			Lines: []GitGraphLine{
				{GraphChars: "●", SHA: "abc1234", Author: "Test", Message: "Initial commit"},
			},
		},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find QUEUE panel border (a dashboard panel, not the git graph)
	var queueBorderLine string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "queue") && strings.Contains(plain, "╭") {
			queueBorderLine = plain
			break
		}
	}
	if queueBorderLine == "" {
		t.Fatal("QUEUE panel not found in stacked layout output")
	}

	// Dashboard panels should stretch to full terminal width (80), not stay at panelTotalWidth (69)
	borderWidth := lipgloss.Width(queueBorderLine)
	if borderWidth != m.width {
		t.Errorf("stacked QUEUE panel width = %d, want %d (full terminal width); panels should stretch in stacked mode", borderWidth, m.width)
	}

	// Also verify HISTORY panel stretches
	var historyBorderLine string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "history") && strings.Contains(plain, "╭") {
			historyBorderLine = plain
			break
		}
	}
	if historyBorderLine == "" {
		t.Fatal("HISTORY panel not found in stacked layout output")
	}
	historyWidth := lipgloss.Width(historyBorderLine)
	if historyWidth != m.width {
		t.Errorf("stacked HISTORY panel width = %d, want %d", historyWidth, m.width)
	}
}

func TestDashboardPanels_DefaultWidthWhenNoGraph(t *testing.T) {
	// When graph is hidden (no stacked mode), panels should use the default panelTotalWidth (69)
	m := Model{
		width: 120, height: 40, gitGraphMode: GitGraphHidden,
		autopilotPanel: NewAutopilotPanel(nil),
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find QUEUE panel border
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "queue") && strings.Contains(plain, "╭") {
			borderWidth := lipgloss.Width(plain)
			if borderWidth != panelTotalWidth {
				t.Errorf("default QUEUE panel width = %d, want %d (panelTotalWidth)", borderWidth, panelTotalWidth)
			}
			return
		}
	}
	t.Fatal("QUEUE panel not found in default layout output")
}

// TestStoreRefreshMsg_UpdatesHistoryAndMetrics verifies that storeRefreshMsg
// replaces stale in-memory history and metrics with live DB state (GH-2248).
func TestStoreRefreshMsg_UpdatesHistoryAndMetrics(t *testing.T) {
	m := NewModel("test")
	// Seed stale in-memory state
	m.completedTasks = []CompletedTask{
		{ID: "stale-1", Title: "Stale Task", Status: "failed"},
	}
	m.metricsCard = MetricsCardData{TotalTasks: 1, Failed: 1}

	// Simulate a store refresh with different data (as if the DB row was deleted)
	msg := storeRefreshMsg{
		completedTasks: []CompletedTask{
			{ID: "fresh-1", Title: "Fresh Task", Status: "success"},
			{ID: "fresh-2", Title: "Another Task", Status: "success"},
		},
		metricsCard: MetricsCardData{
			TotalTasks:  2,
			Succeeded:   2,
			Failed:      0,
			TotalTokens: 5000,
		},
	}

	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.completedTasks) != 2 {
		t.Fatalf("completedTasks len = %d, want 2", len(model.completedTasks))
	}
	if model.completedTasks[0].ID != "fresh-1" {
		t.Errorf("completedTasks[0].ID = %q, want %q", model.completedTasks[0].ID, "fresh-1")
	}
	if model.metricsCard.TotalTasks != 2 {
		t.Errorf("TotalTasks = %d, want 2", model.metricsCard.TotalTasks)
	}
	if model.metricsCard.Failed != 0 {
		t.Errorf("Failed = %d, want 0", model.metricsCard.Failed)
	}
}

// TestStoreRefreshCmd_QueriesDB verifies storeRefreshCmd returns correct data
// from SQLite (GH-2248).
func TestStoreRefreshCmd_QueriesDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-refresh-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert a completed execution
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-1", TaskID: "TASK-1", TaskTitle: "Test Task",
		ProjectPath: "/test", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID: "exec-1", TokensInput: 1000, TokensOutput: 500,
		TokensTotal: 1500, EstimatedCostUSD: 0.10,
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	// Run the refresh command
	cmd := storeRefreshCmd(store, "", defaultStatsWindowDays)
	rawMsg := cmd()
	msg, ok := rawMsg.(storeRefreshMsg)
	if !ok {
		t.Fatalf("expected storeRefreshMsg, got %T", rawMsg)
	}

	if len(msg.completedTasks) != 1 {
		t.Fatalf("completedTasks len = %d, want 1", len(msg.completedTasks))
	}
	if msg.completedTasks[0].ID != "TASK-1" {
		t.Errorf("completedTasks[0].ID = %q, want %q", msg.completedTasks[0].ID, "TASK-1")
	}
	if msg.completedTasks[0].Status != "success" {
		t.Errorf("Status = %q, want %q", msg.completedTasks[0].Status, "success")
	}
	if msg.metricsCard.TotalTasks != 1 {
		t.Errorf("TotalTasks = %d, want 1", msg.metricsCard.TotalTasks)
	}
	if msg.metricsCard.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", msg.metricsCard.Succeeded)
	}

	// Now delete the row and verify refresh picks up the change
	_, err = store.DB().Exec("DELETE FROM executions WHERE id = 'exec-1'")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}

	cmd = storeRefreshCmd(store, "", defaultStatsWindowDays)
	rawMsg = cmd()
	msg = rawMsg.(storeRefreshMsg)

	if len(msg.completedTasks) != 0 {
		t.Errorf("after DELETE: completedTasks len = %d, want 0", len(msg.completedTasks))
	}
	if msg.metricsCard.TotalTasks != 0 {
		t.Errorf("after DELETE: TotalTasks = %d, want 0", msg.metricsCard.TotalTasks)
	}
}

// TestStoreRefreshCmd_ProjectFilter verifies that passing a non-empty projectPath
// scopes the refresh query to that project only (GH-3517).
func TestStoreRefreshCmd_ProjectFilter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-dash-refresh-filter-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Two executions from different projects
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-a", TaskID: "TASK-A", TaskTitle: "Project A Task",
		ProjectPath: "/projects/alpha", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution A: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-b", TaskID: "TASK-B", TaskTitle: "Project B Task",
		ProjectPath: "/projects/beta", Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution B: %v", err)
	}

	// Scoped to alpha — should see only exec-a
	cmd := storeRefreshCmd(store, "/projects/alpha", defaultStatsWindowDays)
	msg, ok := cmd().(storeRefreshMsg)
	if !ok {
		t.Fatalf("expected storeRefreshMsg")
	}
	if len(msg.completedTasks) != 1 {
		t.Fatalf("completedTasks len = %d, want 1 (alpha only)", len(msg.completedTasks))
	}
	if msg.completedTasks[0].ID != "TASK-A" {
		t.Errorf("completedTasks[0].ID = %q, want TASK-A", msg.completedTasks[0].ID)
	}
	if msg.metricsCard.TotalTasks != 1 {
		t.Errorf("TotalTasks = %d, want 1 (alpha only)", msg.metricsCard.TotalTasks)
	}

	// Unscoped — should see both
	cmd = storeRefreshCmd(store, "", defaultStatsWindowDays)
	msg, ok = cmd().(storeRefreshMsg)
	if !ok {
		t.Fatalf("expected storeRefreshMsg")
	}
	if msg.metricsCard.TotalTasks != 2 {
		t.Errorf("unscoped TotalTasks = %d, want 2", msg.metricsCard.TotalTasks)
	}
}

// --- GH-2455: avionics redesign tests ---

func TestRenderBanner(t *testing.T) {
	m := NewModel("2.102.3")
	start := time.Now().Add(-90 * time.Minute) // 1h30m ago
	m.SetBannerMeta("prod", "opus/sonnet", nil, start)
	m.SetBannerAdapters([]AdapterStatus{
		{Name: "GH", Active: true},
		{Name: "SLACK", Active: false},
	})

	out := m.renderBanner()

	// One grom-grammar line: wordmark + liveness dot, lowercased segments,
	// uptime + clock. Adapter chips moved to the queue border legend.
	for _, want := range []string{" pilot ●", "v2.102.3", "prod", "opus/sonnet", "up ", "utc"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderBanner() missing %q\nout:\n%s", want, out)
		}
	}
	for _, reject := range []string{"daemon", "gh", "slack", "\n"} {
		if strings.Contains(out, reject) {
			t.Errorf("renderBanner() should not contain %q\nout:\n%s", reject, out)
		}
	}

	if w := lipgloss.Width(out); w != panelTotalWidth {
		t.Errorf("renderBanner() visual width = %d, want %d", w, panelTotalWidth)
	}
}

func TestRenderBannerNarrowDropsSegments(t *testing.T) {
	// An over-long model stack must degrade by dropping trailing identity
	// segments — never by corrupting the line or dropping the wordmark.
	m := NewModel("2.102.3")
	m.SetBannerMeta("prod", "opus:plan | sonnet:exec | haiku:triage", nil, time.Now().Add(-time.Minute))

	out := m.renderBanner()

	for _, want := range []string{" pilot ●", "v2.102.3", "utc"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderBanner() narrow missing %q\nout:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sonnet:exec") {
		t.Errorf("renderBanner() narrow should drop over-long model stack\nout:\n%s", out)
	}
	if w := lipgloss.Width(out); w != panelTotalWidth {
		t.Errorf("renderBanner() narrow visual width = %d, want %d", w, panelTotalWidth)
	}
}

func TestBuildAdapterLegend(t *testing.T) {
	legend := buildAdapterLegend([]AdapterStatus{
		{Name: "GH", Active: true},
		{Name: "TG", Active: true},
		{Name: "SLACK", Active: false},
		{Name: "DISCORD", Active: false},
		{Name: "LINEAR", Active: false},
	})

	// Active adapters named with ● prefix; idle collapsed to a single count.
	for _, want := range []string{"● gh", "● tg", "○ 3 idle"} {
		if !strings.Contains(legend, want) {
			t.Errorf("buildAdapterLegend() missing %q\nout: %q", want, legend)
		}
	}
	for _, reject := range []string{"slack", "discord", "linear"} {
		if strings.Contains(legend, reject) {
			t.Errorf("buildAdapterLegend() should not name idle adapter %q\nout: %q", reject, legend)
		}
	}

	if got := buildAdapterLegend(nil); got != "" {
		t.Errorf("buildAdapterLegend(nil) = %q, want empty", got)
	}
}

func TestRenderTasksAdapterLegend(t *testing.T) {
	m := NewModel("1.0.0")
	m.SetBannerAdapters([]AdapterStatus{
		{Name: "GH", Active: true},
		{Name: "SLACK", Active: false},
	})
	m.tasks = []TaskDisplay{{ID: "GH-1", Title: "task", Status: "running", Progress: 10}}

	out := m.renderTasks()

	// Queue border legend: running count first, then adapter status.
	for _, want := range []string{"● 1 running", "● gh", "○ 1 idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTasks() legend missing %q\nout:\n%s", want, out)
		}
	}
}

func TestRenderBannerDefaults(t *testing.T) {
	// Banner renders without metadata set (env defaults to "─", no adapters, no uptime).
	m := NewModel("1.0.0")
	out := m.renderBanner()

	if !strings.Contains(out, "v1.0.0") {
		t.Errorf("renderBanner() missing version")
	}
	if !strings.Contains(out, "utc") {
		t.Errorf("renderBanner() missing UTC clock")
	}

	for _, line := range strings.Split(out, "\n") {
		w := lipgloss.Width(line)
		if w != panelTotalWidth {
			t.Errorf("renderBanner() default: line width = %d, want %d", w, panelTotalWidth)
		}
	}
}

func TestAutopilotPanelDisabled(t *testing.T) {
	p := NewAutopilotPanel(nil)
	out := p.View()

	if !strings.Contains(out, "Disabled") {
		t.Errorf("AutopilotPanel(nil).View() missing 'Disabled'")
	}
	for _, line := range strings.Split(out, "\n") {
		w := lipgloss.Width(line)
		if w != panelTotalWidth {
			t.Errorf("AutopilotPanel disabled: line width = %d, want %d", w, panelTotalWidth)
		}
	}
}

func TestRenderAutopilotRow(t *testing.T) {
	// One history-grammar row per PR: glyph + #id + title + 5-cell meter +
	// stage label + age; ↳ detail line only for retries/failures.
	now := time.Now()
	tests := []struct {
		name           string
		stage          autopilot.PRStage
		ciStatus       autopilot.CIStatus
		failures       int
		err            string
		rebaseAttempts int
		wantGlyph      string
		wantLabel      string
		wantLines      int
	}{
		{"waiting ci", autopilot.StageWaitingCI, autopilot.CIPending, 0, "", 0, "●", "ci", 1},
		{"merging", autopilot.StageMerging, autopilot.CISuccess, 0, "", 0, "●", "merge", 1},
		{"post-merge ci", autopilot.StagePostMergeCI, autopilot.CISuccess, 0, "", 0, "●", "tag", 1},
		{"releasing", autopilot.StageReleasing, autopilot.CISuccess, 0, "", 0, "●", "release", 1},
		{"ci failed, retrying", autopilot.StageCIFailed, autopilot.CIFailure, 1, "", 0, "⟲", "ci", 2},
		{"pipeline failed", autopilot.StageFailed, autopilot.CIPending, 0, "boom", 0, "✗", "failed", 2},
		// GH-4383: awaiting_approval must render its own "approval" label,
		// never "rebase" — regardless of the generic per-PR failure counter.
		{"awaiting approval", autopilot.StageAwaitApproval, autopilot.CISuccess, 0, "", 0, "●", "approval", 1},
		// "rebase" is only truthful once a real auto-rebase has happened —
		// StageMerging alone (no RebaseAttempts) stays "merge" (see the
		// "merging" case above); StageMerging + RebaseAttempts>0 is "rebase".
		{"merging after auto-rebase", autopilot.StageMerging, autopilot.CIPending, 0, "", 1, "●", "rebase", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &autopilot.PRState{
				PRNumber: 4054, PRTitle: "fix(executor): skip decomposer",
				Stage: tt.stage, CIStatus: tt.ciStatus, Error: tt.err,
				RebaseAttempts: tt.rebaseAttempts,
				CreatedAt:      now.Add(-2 * time.Minute),
			}
			lines := renderAutopilotRow(pr, tt.failures, 3, panelInnerWidth)
			if len(lines) != tt.wantLines {
				t.Fatalf("line count = %d, want %d:\n%s", len(lines), tt.wantLines, strings.Join(lines, "\n"))
			}
			plain := stripANSI(lines[0])
			if !strings.HasPrefix(plain, "  "+tt.wantGlyph+" #4054") {
				t.Errorf("row should start with %q glyph + id: %q", tt.wantGlyph, plain)
			}
			if !strings.Contains(plain, "■■■■■ "+tt.wantLabel) {
				t.Errorf("row missing meter + stage label %q: %q", tt.wantLabel, plain)
			}
		})
	}
}

func TestRenderAutopilotRow_DetailLine(t *testing.T) {
	pr := &autopilot.PRState{
		PRNumber: 100, PRTitle: "test PR",
		Stage: autopilot.StageFailed, CIStatus: autopilot.CIFailure,
		Error: "TestFoo failed · linux-amd64", CreatedAt: time.Now(),
	}
	lines := renderAutopilotRow(pr, 2, 3, panelInnerWidth)
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	detail := stripANSI(lines[1])
	for _, want := range []string{"↳", "⟲ retry 2/3", "TestFoo failed"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail line missing %q: %q", want, detail)
		}
	}

	// Clean run: no retry chrome at zero failures.
	pr.Stage = autopilot.StageWaitingCI
	pr.Error = ""
	lines = renderAutopilotRow(pr, 0, 3, panelInnerWidth)
	if len(lines) != 1 {
		t.Fatalf("clean run line count = %d, want 1 (no detail line)", len(lines))
	}
	if strings.Contains(stripANSI(lines[0]), "0/3") {
		t.Errorf("clean run should carry no retry fraction: %q", stripANSI(lines[0]))
	}
}

// TestRenderAutopilotRow_AwaitingApprovalNotRebase is the GH-4383 regression:
// a PR parked in awaiting_approval with an approval-submit send failure (the
// GH-4380 outage) must render "approval" + the error hint — never "rebase",
// and never the bare "retry N/M" wording that reads as a rebase failure once
// paired with the (formerly wrong) "rebase" stage label.
func TestRenderAutopilotRow_AwaitingApprovalNotRebase(t *testing.T) {
	approvalRequestedAt := time.Now().Add(-90 * time.Minute)
	pr := &autopilot.PRState{
		PRNumber: 4379, PRTitle: "fix(gateway): add webhook retry",
		Stage:    autopilot.StageAwaitApproval,
		CIStatus: autopilot.CISuccess,
		Error:    "approval-submit send failed: context deadline exceeded",
		// RebaseAttempts is 0 — no rebase was ever attempted (ledger truth
		// from the GH-4383 incident evidence).
		RebaseAttempts:      0,
		CreatedAt:           approvalRequestedAt,
		ApprovalRequestedAt: approvalRequestedAt,
	}
	lines := renderAutopilotRow(pr, 3, 3, panelInnerWidth)
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2 (row + detail)", len(lines))
	}

	row := stripANSI(lines[0])
	if !strings.Contains(row, "■■■■■ approval") {
		t.Errorf("row should render the approval stage label: %q", row)
	}
	if strings.Contains(row, "rebase") {
		t.Errorf("row must never render 'rebase' for awaiting_approval: %q", row)
	}
	if !strings.Contains(row, "1h30m") {
		t.Errorf("row should show wait time since ApprovalRequestedAt (1h30m): %q", row)
	}

	detail := stripANSI(lines[1])
	for _, want := range []string{"⟲ send retry 3/3", "approval-submit send failed"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail line missing %q: %q", want, detail)
		}
	}
	if strings.Contains(detail, "rebase") {
		t.Errorf("detail line must never mention 'rebase' for an approval-submit failure: %q", detail)
	}
}

// fakeAutopilotCtl implements autopilotController for testing without a real Controller.
type fakeAutopilotCtl struct {
	prs      []*autopilot.PRState
	cfg      *autopilot.Config
	failures map[int]int
}

func (f *fakeAutopilotCtl) GetActivePRs() []*autopilot.PRState { return f.prs }
func (f *fakeAutopilotCtl) Config() *autopilot.Config          { return f.cfg }
func (f *fakeAutopilotCtl) GetPRFailures(n int) int            { return f.failures[n] }

func newFakeCtl(prs []*autopilot.PRState, maxFailures int, failures map[int]int) *fakeAutopilotCtl {
	if failures == nil {
		failures = map[int]int{}
	}
	return &fakeAutopilotCtl{
		prs:      prs,
		cfg:      &autopilot.Config{MaxFailures: maxFailures},
		failures: failures,
	}
}

func TestAutopilotPanelView_AllStates(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name        string
		ctl         autopilotController
		wantLines   int    // total output lines (borders included)
		wantContain string // substring in plain text
		wantAbsent  string // substring that must NOT appear
	}{
		{
			name:        "disabled",
			ctl:         nil,
			wantLines:   5, // top border + empty + content + empty + bottom border
			wantContain: "Disabled",
		},
		{
			name:        "idle",
			ctl:         newFakeCtl(nil, 3, nil),
			wantLines:   5, // top border + empty + content + empty + bottom border
			wantContain: "no active PR · check queue for running work",
			wantAbsent:  "STATE",
		},
		{
			name: "ci-running steady state",
			ctl: newFakeCtl([]*autopilot.PRState{{
				PRNumber:  2565,
				PRTitle:   "fix(upgrade): atomic binary replacement",
				Stage:     autopilot.StageWaitingCI,
				CIStatus:  autopilot.CIRunning,
				CreatedAt: now.Add(-90 * time.Second),
			}}, 3, nil),
			wantLines:   5, // border + empty + row + empty + border
			wantContain: "#2565",
			wantAbsent:  "0/3", // no retry chrome on a clean run
		},
		{
			name: "ci-failed with error",
			ctl: newFakeCtl([]*autopilot.PRState{{
				PRNumber:  2565,
				PRTitle:   "fix(upgrade): atomic binary replacement",
				Stage:     autopilot.StageFailed,
				CIStatus:  autopilot.CIFailure,
				Error:     "TestInstallToBinaryPath_Cleanup failed · linux-amd64",
				CreatedAt: now.Add(-4 * time.Minute),
			}}, 3, map[int]int{2565: 2}),
			wantLines:   6, // border + empty + row + detail(retry+error) + empty + border
			wantContain: "↳",
			wantAbsent:  "STATE",
		},
		{
			// GH-4383: awaiting_approval must render "approval", never
			// "rebase" — this case used to assert the bug (wantContain:
			// "rebase") until the panel's stage-label mapping was fixed.
			name: "awaiting approval",
			ctl: newFakeCtl([]*autopilot.PRState{{
				PRNumber:  2565,
				PRTitle:   "fix(upgrade): atomic binary replacement",
				Stage:     autopilot.StageAwaitApproval,
				CIStatus:  autopilot.CISuccess,
				CreatedAt: now.Add(-3 * time.Minute),
			}}, 3, nil),
			wantLines:   5, // border + empty + row + empty + border
			wantContain: "approval",
			wantAbsent:  "rebase",
		},
		{
			name: "released",
			ctl: newFakeCtl([]*autopilot.PRState{{
				PRNumber:  2565,
				PRTitle:   "fix(upgrade): atomic binary replacement",
				Stage:     autopilot.StageReleasing,
				CIStatus:  autopilot.CISuccess,
				CreatedAt: now.Add(-10 * time.Minute),
			}}, 3, nil),
			wantLines:   5, // border + empty + row + empty + border
			wantContain: "release",
			wantAbsent:  "STATE",
		},
		{
			name: "failed no error message",
			ctl: newFakeCtl([]*autopilot.PRState{{
				PRNumber:  2565,
				PRTitle:   "fix(upgrade): atomic binary replacement",
				Stage:     autopilot.StageFailed,
				CIStatus:  autopilot.CIPending,
				Error:     "",
				CreatedAt: now.Add(-2 * time.Minute),
			}}, 3, nil),
			wantLines:  5, // border + empty + row + empty + border (no detail line)
			wantAbsent: "↳",
		},
		{
			name: "two active prs",
			ctl: newFakeCtl([]*autopilot.PRState{
				{PRNumber: 2565, PRTitle: "fix(upgrade): atomic binary replacement", Stage: autopilot.StageWaitingCI, CIStatus: autopilot.CIRunning, CreatedAt: now.Add(-time.Minute)},
				{PRNumber: 2566, PRTitle: "feat(api): rate limiting", Stage: autopilot.StageMerging, CIStatus: autopilot.CISuccess, CreatedAt: now.Add(-5 * time.Minute)},
			}, 3, nil),
			wantLines:   6, // border + empty + row + row + empty + border
			wantContain: "#2566",
			wantAbsent:  "more PR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &AutopilotPanel{
				controller: tt.ctl,
				panelWidth: panelTotalWidth,
			}
			out := p.View()
			plain := stripANSI(out)
			lines := strings.Split(out, "\n")

			if len(lines) != tt.wantLines {
				t.Errorf("line count = %d, want %d\noutput:\n%s", len(lines), tt.wantLines, plain)
			}

			if tt.wantContain != "" && !strings.Contains(plain, tt.wantContain) {
				t.Errorf("output missing %q\nplain:\n%s", tt.wantContain, plain)
			}
			if tt.wantAbsent != "" && strings.Contains(plain, tt.wantAbsent) {
				t.Errorf("output must not contain %q\nplain:\n%s", tt.wantAbsent, plain)
			}

			// No fake progress bars in any state
			if strings.Contains(plain, "[█") || strings.Contains(plain, "[░") {
				t.Errorf("output contains fake progress bar chars\nplain:\n%s", plain)
			}

			// Every line must be panelTotalWidth wide
			for i, line := range lines {
				w := lipgloss.Width(line)
				if w != panelTotalWidth {
					t.Errorf("line %d visual width = %d, want %d: %q", i, w, panelTotalWidth, line)
				}
			}
		})
	}
}

func TestUpdateTokens_ModelAwareCost(t *testing.T) {
	m := NewModel("test")

	// Sonnet pricing: $3/M input, $15/M output
	// 1M input + 1M output = $3 + $15 = $18
	updated, _ := m.Update(updateTokensMsg{InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000, Model: "claude-sonnet-4-6"})
	sonnetModel := updated.(Model)
	wantSonnet := memory.EstimateCost(1_000_000, 1_000_000, "claude-sonnet-4-6")
	if sonnetModel.metricsCard.TotalCostUSD != wantSonnet {
		t.Errorf("Sonnet cost = %.4f, want %.4f", sonnetModel.metricsCard.TotalCostUSD, wantSonnet)
	}

	// Opus pricing: $5/M input, $25/M output = $30 for same token count
	m2 := NewModel("test")
	updated2, _ := m2.Update(updateTokensMsg{InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000, Model: "claude-opus-4-6"})
	opusModel := updated2.(Model)
	wantOpus := memory.EstimateCost(1_000_000, 1_000_000, "claude-opus-4-6")
	if opusModel.metricsCard.TotalCostUSD != wantOpus {
		t.Errorf("Opus cost = %.4f, want %.4f", opusModel.metricsCard.TotalCostUSD, wantOpus)
	}

	// Sonnet cost must be less than Opus cost for the same token count
	if wantSonnet >= wantOpus {
		t.Errorf("expected Sonnet ($%.4f) < Opus ($%.4f)", wantSonnet, wantOpus)
	}
}

func TestUpdateTokens_EmptyModelFallsBackToDefault(t *testing.T) {
	m := NewModel("test")
	updated, _ := m.Update(updateTokensMsg{InputTokens: 100_000, OutputTokens: 50_000, TotalTokens: 150_000, Model: ""})
	model := updated.(Model)
	want := memory.EstimateCost(100_000, 50_000, memory.DefaultModel)
	if model.metricsCard.TotalCostUSD != want {
		t.Errorf("cost with empty model = %.6f, want %.6f (DefaultModel=%s)", model.metricsCard.TotalCostUSD, want, memory.DefaultModel)
	}
}

func newUpgradeModel(t *testing.T) Model {
	t.Helper()
	m := NewModel("v2.100.0")
	m.width = 120
	m.height = 40
	// Seed update info so renderUpdateNotification can reference version strings.
	updated, _ := m.Update(updateAvailableMsg{
		CurrentVersion: "v2.100.0",
		LatestVersion:  "v2.101.0",
	})
	return updated.(Model)
}

func TestUpgradeRender_InProgressShowsMessage(t *testing.T) {
	m := newUpgradeModel(t)

	// Transition to InProgress with a status message.
	updated, _ := m.Update(upgradeProgressMsg{Progress: 50, Message: "Downloading..."})
	model := updated.(Model)
	model.upgradeState = UpgradeStateInProgress

	out := model.renderUpdateNotification()
	if !strings.Contains(out, "Downloading...") {
		t.Errorf("InProgress render missing upgradeMessage; got:\n%s", out)
	}
}

func TestUpgradeRender_InProgress_NoMessageNoExtraLine(t *testing.T) {
	m := newUpgradeModel(t)

	updated, _ := m.Update(upgradeProgressMsg{Progress: 30, Message: ""})
	model := updated.(Model)
	model.upgradeState = UpgradeStateInProgress

	out := model.renderUpdateNotification()
	if strings.Contains(out, "\n  \n") {
		t.Errorf("InProgress render should not add blank line when upgradeMessage is empty; got:\n%s", out)
	}
}

func TestUpgradeRender_CompleteNoRestarting(t *testing.T) {
	m := newUpgradeModel(t)

	updated, _ := m.Update(upgradeCompleteMsg{Success: true})
	model := updated.(Model)

	out := model.renderUpdateNotification()
	if strings.Contains(out, "Restarting") {
		t.Errorf("Complete render must not claim a restart happened; got:\n%s", out)
	}
	if !strings.Contains(out, "restart Pilot manually") {
		t.Errorf("Complete render should instruct manual restart; got:\n%s", out)
	}
	if !strings.Contains(out, "v2.101.0") {
		t.Errorf("Complete render should include the new version; got:\n%s", out)
	}
}

func TestUpgradeRender_FailedShowsError(t *testing.T) {
	m := newUpgradeModel(t)

	errMsg := "Gatekeeper rejected the binary"
	updated, _ := m.Update(upgradeCompleteMsg{Success: false, Error: errMsg})
	model := updated.(Model)

	out := model.renderUpdateNotification()
	if !strings.Contains(out, errMsg) {
		t.Errorf("Failed render should show the Gatekeeper-aware error; got:\n%s", out)
	}
	if strings.Contains(out, "Restarting") {
		t.Errorf("Failed render must not contain 'Restarting'; got:\n%s", out)
	}
}
