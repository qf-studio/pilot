package memory

import "testing"

func TestCheckRegression(t *testing.T) {
	task := func(id string, success bool) *EvalTask {
		return &EvalTask{ID: id, Success: success}
	}

	tests := []struct {
		name              string
		baseline          []*EvalTask
		current           []*EvalTask
		threshold         float64
		wantRegressed     bool
		wantBaselineRate  float64
		wantCurrentRate   float64
		wantRegressedIDs  int
		wantImprovedIDs   int
		wantDeltaPositive bool
	}{
		{
			name:             "no regression - identical results",
			baseline:         []*EvalTask{task("a", true), task("b", true), task("c", false)},
			current:          []*EvalTask{task("a", true), task("b", true), task("c", false)},
			threshold:        DefaultRegressionThreshold,
			wantRegressed:    false,
			wantBaselineRate: 66.66,
			wantCurrentRate:  66.66,
		},
		{
			name:             "minor drop below threshold",
			baseline:         []*EvalTask{task("a", true), task("b", true), task("c", true), task("d", true), task("e", true), task("f", true), task("g", true), task("h", true), task("i", true), task("j", true), task("k", true), task("l", true), task("m", true), task("n", true), task("o", true), task("p", true), task("q", true), task("r", true), task("s", true), task("t", true)},
			current:          []*EvalTask{task("a", false), task("b", true), task("c", true), task("d", true), task("e", true), task("f", true), task("g", true), task("h", true), task("i", true), task("j", true), task("k", true), task("l", true), task("m", true), task("n", true), task("o", true), task("p", true), task("q", true), task("r", true), task("s", true), task("t", true)},
			threshold:        DefaultRegressionThreshold,
			wantRegressed:    false,
			wantBaselineRate: 100,
			wantCurrentRate:  95,
			wantRegressedIDs: 1,
		},
		{
			name:             "major drop above threshold",
			baseline:         []*EvalTask{task("a", true), task("b", true), task("c", true), task("d", true)},
			current:          []*EvalTask{task("a", false), task("b", false), task("c", true), task("d", true)},
			threshold:        DefaultRegressionThreshold,
			wantRegressed:    true,
			wantBaselineRate: 100,
			wantCurrentRate:  50,
			wantRegressedIDs: 2,
		},
		{
			name:              "improvement",
			baseline:          []*EvalTask{task("a", false), task("b", false), task("c", true)},
			current:           []*EvalTask{task("a", true), task("b", true), task("c", true)},
			threshold:         DefaultRegressionThreshold,
			wantRegressed:     false,
			wantBaselineRate:  33.33,
			wantCurrentRate:   100,
			wantImprovedIDs:   2,
			wantDeltaPositive: true,
		},
		{
			name:          "empty baseline and current",
			baseline:      nil,
			current:       nil,
			threshold:     DefaultRegressionThreshold,
			wantRegressed: false,
		},
		{
			name:          "empty baseline only",
			baseline:      nil,
			current:       []*EvalTask{task("a", true)},
			threshold:     DefaultRegressionThreshold,
			wantRegressed: false,
		},
		{
			name:             "identical inputs",
			baseline:         []*EvalTask{task("x", true), task("y", false)},
			current:          []*EvalTask{task("x", true), task("y", false)},
			threshold:        DefaultRegressionThreshold,
			wantRegressed:    false,
			wantBaselineRate: 50,
			wantCurrentRate:  50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := CheckRegression(tt.baseline, tt.current, tt.threshold)

			if report.Regressed != tt.wantRegressed {
				t.Errorf("Regressed = %v, want %v", report.Regressed, tt.wantRegressed)
			}

			if tt.wantBaselineRate > 0 {
				if diff := report.BaselinePassRate - tt.wantBaselineRate; diff > 1 || diff < -1 {
					t.Errorf("BaselinePassRate = %.2f, want ~%.2f", report.BaselinePassRate, tt.wantBaselineRate)
				}
			}

			if tt.wantCurrentRate > 0 {
				if diff := report.CurrentPassRate - tt.wantCurrentRate; diff > 1 || diff < -1 {
					t.Errorf("CurrentPassRate = %.2f, want ~%.2f", report.CurrentPassRate, tt.wantCurrentRate)
				}
			}

			if len(report.RegressedTaskIDs) != tt.wantRegressedIDs {
				t.Errorf("RegressedTaskIDs count = %d, want %d", len(report.RegressedTaskIDs), tt.wantRegressedIDs)
			}

			if len(report.ImprovedTaskIDs) != tt.wantImprovedIDs {
				t.Errorf("ImprovedTaskIDs count = %d, want %d", len(report.ImprovedTaskIDs), tt.wantImprovedIDs)
			}

			if tt.wantDeltaPositive && report.Delta <= 0 {
				t.Errorf("Delta = %.2f, want positive", report.Delta)
			}

			if report.Recommendation == "" {
				t.Error("Recommendation should not be empty")
			}
		})
	}
}
