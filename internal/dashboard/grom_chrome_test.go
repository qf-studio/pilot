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
	m.SetBannerMeta("stage", "opus-5-5 / sonnet-5-5", nil, time.Now().Add(-4*time.Minute))
	next, _ := m.Update(BillingModeMsg{Mode: "subscription", Source: "config"})
	m = next.(Model)
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
		{ID: "GH-3993", Title: "Release trains: scheduled cycles", Status: "running", Progress: 62, Phase: "Implementing", ProjectName: "pilot", StartedAt: time.Now().Add(-3 * time.Minute)},
		{ID: "GH-3994", Title: "Honor require_ci at hijack", Status: "queued", ProjectName: "pilot"},
		{ID: "GH-3990", Title: "Done in another project", Status: "done", PRURL: "https://github.com/o/r/pull/3991", ProjectName: "console", StartedAt: time.Now().Add(-54 * time.Minute), CompletedAt: time.Now().Add(-40 * time.Minute)},
		{ID: "GH-3980", Title: "Failed in a third project", Status: "failed", Phase: "ci_failed", ProjectName: "docs", CompletedAt: time.Now().Add(-10 * time.Hour)},
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
		" pilot ●", " opus-5-5 / sonnet-5-5", "subscription ",
		"✓ GH-4018", "■■■■■■■ released", "✗ GH-4008", "■■■■■■■ ci_failed",
		"┤ ● 2 prs ├", "● #4054", "⟲ #4050", "↳ ⟲ retry 1/3",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("dashboard missing grom landmark %q", want)
		}
	}
}

// TestChromeHeader_TwoLineBanner pins the GH-6350 header: banner line 1 is
// the identity and clock, line 2 the model text and billing word, every line
// the panel width (69, or the terminal width when stacked), and no header line
// holds the word "billing".
func TestChromeHeader_TwoLineBanner(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		stacked bool
		msg     BillingModeMsg
		word    string
		want    int
	}{
		{"subscription at 69", 120, false, BillingModeMsg{Mode: "subscription", Source: "config"}, "subscription", panelTotalWidth},
		{"api tokens at 69", 120, false, BillingModeMsg{Mode: "api_key", Source: "config"}, "api tokens", panelTotalWidth},
		{"subscription stacked at 80", 80, true, BillingModeMsg{Mode: "subscription", Source: "config"}, "subscription", 80},
		{"override stacked at 80", 80, true, BillingModeMsg{Mode: "api_key", Source: "cli"}, "api tokens", 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel("2.320.1")
			m.width, m.height = tt.width, 50
			if tt.stacked {
				m.gitGraphMode = GitGraphVisible
			}
			m.SetBannerMeta("stage", "opus-5-5 / sonnet-5-5", nil, time.Now().Add(-10*time.Minute))
			next, _ := m.Update(tt.msg)
			m = next.(Model)

			header := m.renderChromeHeader()
			t.Log("\n" + stripANSI(header))
			lines := strings.Split(strings.TrimSuffix(header, "\n"), "\n")
			if len(lines) < 2 {
				t.Fatalf("header = %d lines:\n%s", len(lines), stripANSI(header))
			}
			l1, l2 := stripANSI(lines[0]), stripANSI(lines[1])
			if !strings.HasPrefix(l1, " pilot ● · v2.320.1 · stage") || !strings.HasSuffix(l1, " utc ") {
				t.Errorf("banner line 1 = %q", l1)
			}
			if !strings.HasPrefix(l2, " opus-5-5 / sonnet-5-5 ") || !strings.HasSuffix(l2, "  "+tt.word+" ") {
				t.Errorf("banner line 2 = %q, want the model text left and %q right", l2, tt.word)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w != tt.want {
					t.Errorf("header line %d visual width = %d, want %d: %q", i+1, w, tt.want, stripANSI(line))
				}
				if strings.Contains(stripANSI(line), "billing") {
					t.Errorf("header line %d holds \"billing\": %q", i+1, stripANSI(line))
				}
			}
		})
	}
}
