package executor

import (
	"strings"
	"testing"
)

// TestTaskDecomposer_SkipReason_TableDriven is the GH-4271 regression suite:
// every non-decomposition branch in DecomposeWithContext must report a
// machine-readable SkipReason plus the classified Complexity, so callers can
// tell a genuine complex/epic-tier skip (worth a log line + execution event)
// apart from an unremarkable one (task never met the tier in the first
// place). See TaskDecomposer.ReportableSkip for the reporting policy.
func TestTaskDecomposer_SkipReason_TableDriven(t *testing.T) {
	baseConfig := func() *DecomposeConfig {
		return &DecomposeConfig{
			Enabled:             true,
			MinComplexity:       "complex",
			MaxSubtasks:         5,
			MinDescriptionWords: 50,
		}
	}

	longNonStructuredDescription := strings.Repeat("word ", 60) +
		"this description has plenty of words but no numbered steps, bullets, checkboxes, or file paths."

	tests := []struct {
		name           string
		config         *DecomposeConfig
		task           *Task
		wantSkipReason SkipReason
		wantComplexity Complexity
		wantReportable bool
	}{
		{
			name: "disabled gate on an epic-classified task",
			config: func() *DecomposeConfig {
				c := baseConfig()
				c.Enabled = false
				return c
			}(),
			task: &Task{
				ID:          "GH-1",
				Title:       "[epic] roll out multi-service rollout",
				Description: "short",
			},
			wantSkipReason: SkipReasonDisabled,
			wantComplexity: ComplexityEpic,
			wantReportable: true,
		},
		{
			name:   "no-decompose label gate",
			config: baseConfig(),
			task: &Task{
				ID:          "GH-2",
				Title:       "[epic] roll out multi-service rollout",
				Description: "short",
				Labels:      []string{NoDecomposeLabel},
			},
			wantSkipReason: SkipReasonNoDecomposeLabel,
			wantComplexity: ComplexityComplex, // detectEpic downgrades to complex when the label is present
			wantReportable: true,
		},
		{
			name:   "no-decompose phrase gate",
			config: baseConfig(),
			task: &Task{
				ID:          "GH-3",
				Title:       "[epic] roll out multi-service rollout",
				Description: "This task must not be decomposed under any circumstances.",
			},
			wantSkipReason: SkipReasonNoDecomposePhrase,
			wantComplexity: ComplexityComplex, // detectEpic downgrades to complex when the phrase is present
			wantReportable: true,
		},
		{
			name:   "complexity below min_complexity threshold",
			config: baseConfig(),
			task: &Task{
				ID:          "GH-4",
				Title:       "Add a new dashboard widget",
				Description: "Add a new widget to the dashboard that shows the current queue depth and updates live via websocket polling.",
			},
			wantSkipReason: SkipReasonBelowMinComplexity,
			wantComplexity: ComplexityMedium,
			wantReportable: false, // by construction shouldDecompose(Medium) is false here — not a silent-epic scenario
		},
		{
			name:   "description too short (heuristic mode) on a complex-classified task",
			config: baseConfig(),
			task: &Task{
				ID:          "GH-5",
				Title:       "refactor the executor pipeline",
				Description: "small change",
			},
			wantSkipReason: SkipReasonDescriptionTooShort,
			wantComplexity: ComplexityComplex,
			wantReportable: true,
		},
		{
			name:   "no structural split points found",
			config: baseConfig(),
			task: &Task{
				ID:          "GH-6",
				Title:       "refactor the executor pipeline",
				Description: longNonStructuredDescription,
			},
			wantSkipReason: SkipReasonNoSplitPoints,
			wantComplexity: ComplexityComplex,
			wantReportable: true,
		},
		{
			name:   "successful decomposition reports no skip reason",
			config: baseConfig(),
			task: &Task{
				ID:    "GH-7",
				Title: "refactor the executor pipeline",
				// Checklist (not a plain numbered list) since GH-4395 restricted
				// analyzeAndSplit to explicit work-item structure.
				Description: "- [ ] Update decompose.go\n" +
					"- [ ] Update runner.go\n" +
					"- [ ] Update dispatcher.go\n" +
					"- [ ] Add tests\n" +
					strings.Repeat("word ", 50),
			},
			wantSkipReason: SkipReasonNone,
			wantComplexity: ComplexityComplex,
			wantReportable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decomposer := NewTaskDecomposer(tt.config)
			result := decomposer.Decompose(tt.task)

			if result.SkipReason != tt.wantSkipReason {
				t.Errorf("SkipReason = %q, want %q (Reason=%q)", result.SkipReason, tt.wantSkipReason, result.Reason)
			}
			if result.Complexity != tt.wantComplexity {
				t.Errorf("Complexity = %q, want %q", result.Complexity, tt.wantComplexity)
			}
			if got := decomposer.ReportableSkip(result); got != tt.wantReportable {
				t.Errorf("ReportableSkip() = %v, want %v", got, tt.wantReportable)
			}
			if tt.wantSkipReason == SkipReasonNone && !result.Decomposed {
				t.Error("expected Decomposed=true when SkipReason is none")
			}
			if tt.wantSkipReason != SkipReasonNone && result.Decomposed {
				t.Error("expected Decomposed=false when a SkipReason is set")
			}
		})
	}
}

// TestTaskDecomposer_ReportableSkip_NilAndEdgeCases verifies ReportableSkip's
// guard clauses so a caller can pass any DecomposeResult without a nil check.
func TestTaskDecomposer_ReportableSkip_NilAndEdgeCases(t *testing.T) {
	decomposer := NewTaskDecomposer(DefaultDecomposeConfig())

	if decomposer.ReportableSkip(nil) {
		t.Error("ReportableSkip(nil) = true, want false")
	}
	if decomposer.ReportableSkip(&DecomposeResult{Decomposed: true, Complexity: ComplexityEpic}) {
		t.Error("ReportableSkip on a successful decomposition = true, want false")
	}
	if decomposer.ReportableSkip(&DecomposeResult{Decomposed: false, SkipReason: SkipReasonNone, Complexity: ComplexityEpic}) {
		t.Error("ReportableSkip with SkipReasonNone = true, want false")
	}
}

// TestTaskDecomposer_SkipLogDetail_CarriesConcreteValues verifies the emitted
// message contains the reason code plus concrete threshold/observed numbers,
// matching the canary evidence format
// ("decomposition skipped: ... description_words=275 < min_description_words=300").
func TestTaskDecomposer_SkipLogDetail_CarriesConcreteValues(t *testing.T) {
	decomposer := NewTaskDecomposer(&DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 300,
	})

	result := &DecomposeResult{
		Decomposed:       false,
		SkipReason:       SkipReasonDescriptionTooShort,
		Complexity:       ComplexityEpic,
		DescriptionWords: 275,
	}

	detail := decomposer.SkipLogDetail(result)

	for _, want := range []string{
		"reason=description_too_short",
		"complexity=epic",
		"description_words=275",
		"min_description_words=300",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("SkipLogDetail() = %q, want it to contain %q", detail, want)
		}
	}
}
