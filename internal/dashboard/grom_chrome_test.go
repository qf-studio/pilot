package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/qf-studio/pilot/internal/autopilot"
)

// TestFullDashboardRender_WidthInvariants renders the whole dashboard with
// representative data and asserts every bordered line is exactly
// panelTotalWidth wide — the invariant the grom restyle must preserve.
// Run with -v to eyeball the full render (t.Log output).
func TestFullDashboardRender_WidthInvariants(t *testing.T) {
	m := NewModel("2.233.0")
	m.width = 120
	m.height = 50
	m.SetBannerMeta("stage", "opus/sonnet", nil, time.Now().Add(-4*time.Minute))
	m.SetBannerAdapters([]AdapterStatus{
		{Name: "gh", Active: true},
		{Name: "tg", Active: true},
		{Name: "slack", Active: false},
	})
	m.metricsCard = MetricsCardData{
		TotalTokens: 104_700, InputTokens: 57_300, OutputTokens: 31_000,
		CacheReadTokens: 1_204_000, CacheWriteTokens: 88_000,
		TotalCostUSD: 154.23, CostPerTask: 0.42,
		TotalTasks: 231, Succeeded: 154, Failed: 12, NoOp: 3, Infra: 2,
		TokenHistory: []int64{120, 340, 260, 510, 480, 700, 620},
		CostHistory:  []float64{1.2, 3.4, 2.6, 5.1, 4.8, 7.0, 6.2},
		TaskHistory:  []int{2, 5, 3, 8, 6, 9, 7},
	}
	m.tasks = []TaskDisplay{
		{ID: "GH-3993", Title: "Release trains: scheduled cycles", Status: "running", Progress: 62},
		{ID: "GH-3994", Title: "Honor require_ci at hijack", Status: "queued"},
	}
	m.completedTasks = []CompletedTask{
		{ID: "GH-4018", Title: "Aggregated scope notes + LLM What's New", Status: "success", CompletedAt: time.Now().Add(-40 * time.Minute), PeakRSSMB: 2048, Stage: StageInfo{Reached: 7, Label: "released", Known: true}},
		{ID: "GH-4008", Title: "Poller noise reduction", Status: "failed", CompletedAt: time.Now().Add(-3 * time.Hour), Stage: StageInfo{Reached: 4, Label: "ci_failed", Failed: true, Known: true}},
	}
	m.autopilotPanel = &AutopilotPanel{controller: newFakeCtl([]*autopilot.PRState{
		{PRNumber: 4054, PRTitle: "fix(executor): skip decomposer for single-package epics", Stage: autopilot.StageWaitingCI, CIStatus: autopilot.CIRunning, CreatedAt: time.Now().Add(-2 * time.Minute)},
		{PRNumber: 4050, PRTitle: "fix(github/sdk-poller): handleGithubIssueEvent dedup", Stage: autopilot.StageMerging, CIStatus: autopilot.CIFailure, CreatedAt: time.Now().Add(-14 * time.Minute)},
	}, 3, map[int]int{4050: 1})}
	m.logs = []string{
		"[17:58] PR #4018 merged (v2.233.0)",
		"[18:02] GH-3993 executor started",
		"[18:04] CI green on pilot/GH-3993",
	}

	out := m.renderDashboard()
	t.Log("\n" + out)

	for i, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if w := lipgloss.Width(line); w != panelTotalWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, panelTotalWidth, stripANSI(line))
		}
	}

	// Grom chrome landmarks: lowercase titles, legend in the queue border.
	plain := stripANSI(out)
	for _, want := range []string{
		"╭─ tokens ", "╭─ cost ", "╭─ queue · outcomes ",
		"┤ ● 1 running  ● gh  ● tg  ○ 1 idle ├", "╭─ autopilot ", "╭─ history ", "╭─ logs ",
		" pilot ●",
		"✓ GH-4018", "■■■■■■■ released", "✗ GH-4008", "■■■■■■■ ci_failed",
		"┤ ● 2 prs ├", "● #4054", "⟲ #4050", "↳ ⟲ retry 1/3",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("dashboard missing grom landmark %q", want)
		}
	}
}
