package wiring

import (
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
)

// TestPollingGatewayParity verifies that both harness constructors produce
// identical wiring state for all 17 Runner Has* accessors across multiple
// config combinations. Since GH-1935 closed the learning loop parity gap,
// both paths must be fully identical.
func TestPollingGatewayParity(t *testing.T) {
	tests := []struct {
		name       string
		configFunc func() *config.Config
	}{
		{
			name:       "minimal config",
			configFunc: MinimalConfig,
		},
		{
			name: "with autopilot",
			configFunc: func() *config.Config {
				return WithAutopilot(MinimalConfig())
			},
		},
		{
			name: "with quality",
			configFunc: func() *config.Config {
				return WithQuality(MinimalConfig())
			},
		},
		{
			name: "with budget",
			configFunc: func() *config.Config {
				return WithBudget(MinimalConfig())
			},
		},
		{
			name: "with learning",
			configFunc: func() *config.Config {
				return WithLearning(MinimalConfig())
			},
		},
		{
			name: "with team",
			configFunc: func() *config.Config {
				return WithTeam(MinimalConfig())
			},
		},
		{
			name:       "full config",
			configFunc: FullConfig,
		},
	}

	// All 17 Has* accessors on Runner.
	type hasCheck struct {
		field string
		get   func(*Harness) bool
	}
	allChecks := []hasCheck{
		{"HasKnowledge", func(h *Harness) bool { return h.Runner.HasKnowledge() }},
		{"HasLogStore", func(h *Harness) bool { return h.Runner.HasLogStore() }},
		{"HasMonitor", func(h *Harness) bool { return h.Runner.HasMonitor() }},
		{"HasOnSubIssuePRCreated", func(h *Harness) bool { return h.Runner.HasOnSubIssuePRCreated() }},
		{"HasModelRouter", func(h *Harness) bool { return h.Runner.HasModelRouter() }},
		{"HasQualityCheckerFactory", func(h *Harness) bool { return h.Runner.HasQualityCheckerFactory() }},
		{"HasLearningLoop", func(h *Harness) bool { return h.Runner.HasLearningLoop() }},
		{"HasPatternContext", func(h *Harness) bool { return h.Runner.HasPatternContext() }},
		{"HasTokenLimitCheck", func(h *Harness) bool { return h.Runner.HasTokenLimitCheck() }},
		{"HasTeamChecker", func(h *Harness) bool { return h.Runner.HasTeamChecker() }},
		{"HasDecomposer", func(h *Harness) bool { return h.Runner.HasDecomposer() }},
		{"HasAlertProcessor", func(h *Harness) bool { return h.Runner.HasAlertProcessor() }},
		{"HasIntentJudge", func(h *Harness) bool { return h.Runner.HasIntentJudge() }},
		{"HasDriftDetector", func(h *Harness) bool { return h.Runner.HasDriftDetector() }},
		{"HasProfileManager", func(h *Harness) bool { return h.Runner.HasProfileManager() }},
		{"HasParallelRunner", func(h *Harness) bool { return h.Runner.HasParallelRunner() }},
		{"HasSubIssueCreator", func(h *Harness) bool { return h.Runner.HasSubIssueCreator() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.configFunc()
			polling := NewPollingHarness(t, cfg)

			cfg2 := tt.configFunc()
			gateway := NewGatewayHarness(t, cfg2)

			for _, check := range allChecks {
				pv := check.get(polling)
				gv := check.get(gateway)
				if pv != gv {
					t.Errorf("parity mismatch for %s: polling=%v, gateway=%v",
						check.field, pv, gv)
				}
			}

			// Verify both harnesses have non-nil core components
			if polling.Store == nil {
				t.Error("polling harness: Store is nil")
			}
			if gateway.Store == nil {
				t.Error("gateway harness: Store is nil")
			}
			if polling.Controller == nil {
				t.Error("polling harness: Controller is nil")
			}
			if gateway.Controller == nil {
				t.Error("gateway harness: Controller is nil")
			}
		})
	}
}

// TestHarnessFieldsWithMinimalConfig verifies that a minimal config produces
// the expected baseline wiring: core components present, optional ones absent.
func TestHarnessFieldsWithMinimalConfig(t *testing.T) {
	for _, mode := range []string{"polling", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			cfg := MinimalConfig()
			var h *Harness
			if mode == "polling" {
				h = NewPollingHarness(t, cfg)
			} else {
				h = NewGatewayHarness(t, cfg)
			}

			// Core: always wired
			if !h.Runner.HasKnowledge() {
				t.Error("HasKnowledge should be true")
			}
			if !h.Runner.HasLogStore() {
				t.Error("HasLogStore should be true")
			}
			if !h.Runner.HasMonitor() {
				t.Error("HasMonitor should be true")
			}
			if !h.Runner.HasOnSubIssuePRCreated() {
				t.Error("HasOnSubIssuePRCreated should be true")
			}
			if !h.Runner.HasModelRouter() {
				t.Error("HasModelRouter should be true")
			}

			// Optional: disabled in minimal config
			if h.Runner.HasQualityCheckerFactory() {
				t.Error("HasQualityCheckerFactory should be false with minimal config")
			}
			if h.Runner.HasLearningLoop() {
				t.Error("HasLearningLoop should be false with minimal config")
			}
			if h.Runner.HasPatternContext() {
				t.Error("HasPatternContext should be false with minimal config")
			}
			if h.Runner.HasTokenLimitCheck() {
				t.Error("HasTokenLimitCheck should be false with minimal config")
			}
			if h.Runner.HasTeamChecker() {
				t.Error("HasTeamChecker should be false with minimal config")
			}
		})
	}
}
