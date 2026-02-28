package wiring

import (
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
)

// TestPollingGatewayParity verifies that both harness constructors produce
// identical wiring state for all Runner fields — except the known learning
// loop parity gap (GH-1814).
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
			name:       "full config",
			configFunc: FullConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.configFunc()

			polling := NewPollingHarness(t, cfg)
			// Re-create config since harness may have consumed TempDir
			cfg2 := tt.configFunc()
			gateway := NewGatewayHarness(t, cfg2)

			// Fields that MUST be identical across both paths
			parityChecks := []struct {
				field   string
				polling bool
				gateway bool
			}{
				{"HasKnowledge", polling.Runner.HasKnowledge(), gateway.Runner.HasKnowledge()},
				{"HasLogStore", polling.Runner.HasLogStore(), gateway.Runner.HasLogStore()},
				{"HasMonitor", polling.Runner.HasMonitor(), gateway.Runner.HasMonitor()},
				{"HasOnSubIssuePRCreated", polling.Runner.HasOnSubIssuePRCreated(), gateway.Runner.HasOnSubIssuePRCreated()},
				{"HasModelRouter", polling.Runner.HasModelRouter(), gateway.Runner.HasModelRouter()},
				{"HasQualityCheckerFactory", polling.Runner.HasQualityCheckerFactory(), gateway.Runner.HasQualityCheckerFactory()},
			}

			for _, check := range parityChecks {
				if check.polling != check.gateway {
					t.Errorf("parity mismatch for %s: polling=%v, gateway=%v",
						check.field, check.polling, check.gateway)
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

// TestLearningLoopParityGap documents and verifies the known GH-1814 parity
// gap: learning loop is wired in polling mode but NOT in gateway mode.
func TestLearningLoopParityGap(t *testing.T) {
	cfg := WithLearning(MinimalConfig())
	polling := NewPollingHarness(t, cfg)

	cfg2 := WithLearning(MinimalConfig())
	gateway := NewGatewayHarness(t, cfg2)

	// Polling path wires learning loop
	if !polling.Runner.HasLearningLoop() {
		t.Error("polling harness should have LearningLoop wired")
	}
	if !polling.Runner.HasPatternContext() {
		t.Error("polling harness should have PatternContext wired")
	}
	if polling.LearningLoop == nil {
		t.Error("polling harness: LearningLoop field is nil")
	}
	if polling.PatternContext == nil {
		t.Error("polling harness: PatternContext field is nil")
	}

	// Gateway path does NOT wire learning loop (known gap)
	if gateway.Runner.HasLearningLoop() {
		t.Error("gateway harness should NOT have LearningLoop (GH-1814 parity gap)")
	}
	if gateway.Runner.HasPatternContext() {
		t.Error("gateway harness should NOT have PatternContext (GH-1814 parity gap)")
	}
	if gateway.LearningLoop != nil {
		t.Error("gateway harness: LearningLoop should be nil")
	}
	if gateway.PatternContext != nil {
		t.Error("gateway harness: PatternContext should be nil")
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
		})
	}
}
