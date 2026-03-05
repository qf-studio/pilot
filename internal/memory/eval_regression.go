package memory

import "fmt"

// DefaultRegressionThreshold is the pass@1 delta (in percentage points) above which
// a regression is flagged. For example, 5.0 means a 5pp drop triggers the flag.
const DefaultRegressionThreshold = 5.0

// RegressionReport summarises the difference between a baseline and current eval run.
type RegressionReport struct {
	BaselinePassRate float64  `json:"baseline_pass_rate"`
	CurrentPassRate  float64  `json:"current_pass_rate"`
	Delta            float64  `json:"delta"`
	Regressed        bool     `json:"regressed"`
	RegressedTaskIDs []string `json:"regressed_task_ids,omitempty"`
	ImprovedTaskIDs  []string `json:"improved_task_ids,omitempty"`
	Recommendation   string   `json:"recommendation"`
}

// CheckRegression compares baseline and current eval task lists and produces a
// RegressionReport. pass@1 rate is computed as the fraction of tasks with Success=true.
// The threshold is in percentage points: if the current pass rate drops by more than
// threshold compared to baseline, Regressed is set to true.
func CheckRegression(baseline, current []*EvalTask, threshold float64) *RegressionReport {
	baselineRate := passRate(baseline)
	currentRate := passRate(current)
	delta := currentRate - baselineRate

	// Build lookup of baseline outcomes by ID.
	baselineSuccess := make(map[string]bool, len(baseline))
	for _, t := range baseline {
		baselineSuccess[t.ID] = t.Success
	}

	var regressed, improved []string
	for _, t := range current {
		prev, existed := baselineSuccess[t.ID]
		if !existed {
			continue
		}
		if prev && !t.Success {
			regressed = append(regressed, t.ID)
		}
		if !prev && t.Success {
			improved = append(improved, t.ID)
		}
	}

	report := &RegressionReport{
		BaselinePassRate: baselineRate,
		CurrentPassRate:  currentRate,
		Delta:            delta,
		RegressedTaskIDs: regressed,
		ImprovedTaskIDs:  improved,
	}

	if -delta > threshold {
		report.Regressed = true
		report.Recommendation = fmt.Sprintf(
			"Pass rate dropped %.1fpp (%.1f%% → %.1f%%), exceeding %.1fpp threshold. Investigate %d regressed task(s).",
			-delta, baselineRate, currentRate, threshold, len(regressed),
		)
	} else if delta > 0 {
		report.Recommendation = fmt.Sprintf(
			"Pass rate improved %.1fpp (%.1f%% → %.1f%%). %d task(s) newly passing.",
			delta, baselineRate, currentRate, len(improved),
		)
	} else {
		report.Recommendation = "No significant change in pass rate."
	}

	return report
}

// passRate returns the percentage of tasks that succeeded (0–100).
// Returns 0 for empty slices.
func passRate(tasks []*EvalTask) float64 {
	if len(tasks) == 0 {
		return 0
	}
	passed := 0
	for _, t := range tasks {
		if t.Success {
			passed++
		}
	}
	return float64(passed) / float64(len(tasks)) * 100
}
