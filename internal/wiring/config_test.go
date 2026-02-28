package wiring

import (
	"testing"
)

// TestConfigFlagToComponentMapping verifies that config flags correctly
// control component instantiation: enabled = non-nil, disabled = nil.
func TestConfigFlagToComponentMapping(t *testing.T) {
	tests := []struct {
		name      string
		setup     func()
		field     string
		checkFunc func(*Harness) bool
		enabled   bool
	}{
		{
			name:      "quality disabled → no factory",
			field:     "QualityCheckerFactory",
			checkFunc: func(h *Harness) bool { return h.Runner.HasQualityCheckerFactory() },
			enabled:   false,
		},
		{
			name:      "quality enabled → factory wired",
			field:     "QualityCheckerFactory",
			checkFunc: func(h *Harness) bool { return h.Runner.HasQualityCheckerFactory() },
			enabled:   true,
		},
		{
			name:      "learning disabled → no learning loop",
			field:     "LearningLoop",
			checkFunc: func(h *Harness) bool { return h.Runner.HasLearningLoop() },
			enabled:   false,
		},
		{
			name:      "learning enabled → learning loop wired (polling)",
			field:     "LearningLoop",
			checkFunc: func(h *Harness) bool { return h.Runner.HasLearningLoop() },
			enabled:   true,
		},
		{
			name:      "learning disabled → no pattern context",
			field:     "PatternContext",
			checkFunc: func(h *Harness) bool { return h.Runner.HasPatternContext() },
			enabled:   false,
		},
		{
			name:      "learning enabled → pattern context wired (polling)",
			field:     "PatternContext",
			checkFunc: func(h *Harness) bool { return h.Runner.HasPatternContext() },
			enabled:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := MinimalConfig()

			// Enable the relevant feature
			if tt.enabled {
				switch tt.field {
				case "QualityCheckerFactory":
					WithQuality(cfg)
				case "LearningLoop", "PatternContext":
					WithLearning(cfg)
				}
			}

			// Use polling harness (has full wiring)
			h := NewPollingHarness(t, cfg)

			got := tt.checkFunc(h)
			if got != tt.enabled {
				t.Errorf("%s: expected %v, got %v", tt.field, tt.enabled, got)
			}
		})
	}
}

// TestCoreComponentsAlwaysWired verifies that core components are always
// present regardless of config, for both harness modes.
func TestCoreComponentsAlwaysWired(t *testing.T) {
	coreChecks := []struct {
		name  string
		check func(*Harness) bool
	}{
		{"Knowledge", func(h *Harness) bool { return h.Runner.HasKnowledge() }},
		{"LogStore", func(h *Harness) bool { return h.Runner.HasLogStore() }},
		{"Monitor", func(h *Harness) bool { return h.Runner.HasMonitor() }},
		{"OnSubIssuePRCreated", func(h *Harness) bool { return h.Runner.HasOnSubIssuePRCreated() }},
		{"ModelRouter", func(h *Harness) bool { return h.Runner.HasModelRouter() }},
	}

	for _, mode := range []string{"polling", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			cfg := MinimalConfig()
			var h *Harness
			if mode == "polling" {
				h = NewPollingHarness(t, cfg)
			} else {
				h = NewGatewayHarness(t, cfg)
			}

			for _, cc := range coreChecks {
				if !cc.check(h) {
					t.Errorf("%s should always be wired in %s mode", cc.name, mode)
				}
			}
		})
	}
}

// TestMinimalConfigDefaults verifies that MinimalConfig disables all optional features.
func TestMinimalConfigDefaults(t *testing.T) {
	cfg := MinimalConfig()

	if cfg.Orchestrator.Autopilot.Enabled {
		t.Error("autopilot should be disabled in minimal config")
	}
	if cfg.Quality.Enabled {
		t.Error("quality should be disabled in minimal config")
	}
	if cfg.Budget.Enabled {
		t.Error("budget should be disabled in minimal config")
	}
	if cfg.Memory.Learning.Enabled {
		t.Error("learning should be disabled in minimal config")
	}
}

// TestFullConfigEnablesAll verifies that FullConfig enables all optional features.
func TestFullConfigEnablesAll(t *testing.T) {
	cfg := FullConfig()

	if !cfg.Orchestrator.Autopilot.Enabled {
		t.Error("autopilot should be enabled in full config")
	}
	if !cfg.Quality.Enabled {
		t.Error("quality should be enabled in full config")
	}
	if !cfg.Budget.Enabled {
		t.Error("budget should be enabled in full config")
	}
	if !cfg.Memory.Learning.Enabled {
		t.Error("learning should be enabled in full config")
	}
}

// TestConfigBuildersAreAdditive verifies that config builders can be composed.
func TestConfigBuildersAreAdditive(t *testing.T) {
	cfg := MinimalConfig()
	WithAutopilot(cfg)
	WithQuality(cfg)

	if !cfg.Orchestrator.Autopilot.Enabled {
		t.Error("autopilot should be enabled after WithAutopilot")
	}
	if !cfg.Quality.Enabled {
		t.Error("quality should be enabled after WithQuality")
	}
	if cfg.Budget.Enabled {
		t.Error("budget should remain disabled when not explicitly enabled")
	}
}
