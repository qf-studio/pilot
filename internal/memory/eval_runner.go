package memory

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// EvalExecutor abstracts the task execution interface for eval runs.
// This avoids importing the executor package, preventing import cycles.
type EvalExecutor interface {
	// RunEvalTask executes a single eval task in a worktree checkout and returns
	// whether it passed, along with execution metrics.
	RunEvalTask(ctx context.Context, task *EvalTask, model string) (*EvalResult, error)
}

// EvalRunConfig configures a single eval run.
type EvalRunConfig struct {
	RunID    string       // Unique identifier for this eval run
	Tasks    []*EvalTask  // Tasks to evaluate
	Model    string       // Model to use for execution
	Limit    int          // Max tasks to run (0 = all)
	Store    *Store       // Persistence layer
	Executor EvalExecutor // Task executor
}

// EvalRunSummary summarises the outcome of a single eval run.
type EvalRunSummary struct {
	RunID       string        `json:"run_id"`
	Model       string        `json:"model"`
	TotalTasks  int           `json:"total_tasks"`
	Passed      int           `json:"passed"`
	Failed      int           `json:"failed"`
	PassRate    float64       `json:"pass_rate"`
	TotalTimeMs int64         `json:"total_time_ms"`
	Results     []*EvalResult `json:"results"`
}

// RunEval executes eval tasks using the configured executor, persists results,
// and returns a summary. It applies the limit from config, validates pass criteria
// for each task, and cleans up worktrees on completion.
func RunEval(ctx context.Context, cfg EvalRunConfig) (*EvalRunSummary, error) {
	if cfg.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if cfg.Executor == nil {
		return nil, fmt.Errorf("executor is required")
	}

	tasks := cfg.Tasks
	if cfg.Limit > 0 && cfg.Limit < len(tasks) {
		tasks = tasks[:cfg.Limit]
	}

	summary := &EvalRunSummary{
		RunID:      cfg.RunID,
		Model:      cfg.Model,
		TotalTasks: len(tasks),
	}

	start := time.Now()

	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}

		result, err := cfg.Executor.RunEvalTask(ctx, task, cfg.Model)
		if err != nil {
			slog.Warn("Eval task execution failed",
				slog.String("task_id", task.ID),
				slog.String("error", err.Error()),
			)
			result = &EvalResult{
				RunID:    cfg.RunID,
				TaskID:   task.ID,
				Model:    cfg.Model,
				Passed:   false,
				ErrorMsg: err.Error(),
			}
		}

		// Ensure run metadata is set
		result.RunID = cfg.RunID
		result.Model = cfg.Model

		// Validate pass criteria if the executor reported success
		if result.Passed && len(task.PassCriteria) > 0 {
			result.Passed = validatePassCriteria(task.PassCriteria)
		}

		if cfg.Store != nil {
			if saveErr := cfg.Store.SaveEvalResult(result); saveErr != nil {
				slog.Error("Failed to save eval result",
					slog.String("task_id", task.ID),
					slog.String("error", saveErr.Error()),
				)
			}
		}

		summary.Results = append(summary.Results, result)
		if result.Passed {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}

	summary.TotalTimeMs = time.Since(start).Milliseconds()
	if summary.TotalTasks > 0 {
		summary.PassRate = float64(summary.Passed) / float64(summary.TotalTasks) * 100
	}

	return summary, nil
}

// validatePassCriteria returns true only if all criteria passed.
func validatePassCriteria(criteria []PassCriteria) bool {
	for _, c := range criteria {
		if !c.Passed {
			return false
		}
	}
	return true
}

// Pass1 computes the pass@1 metric: the fraction of tasks that passed on the first attempt.
// Results should be from a single run. Returns a percentage (0–100).
func Pass1(results []*EvalResult) float64 {
	if len(results) == 0 {
		return 0
	}
	passed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
	}
	return float64(passed) / float64(len(results)) * 100
}

// PassK computes the pass@k metric using the unbiased estimator:
//
//	pass@k = 1 - C(n-c, k) / C(n, k)
//
// where n = total samples, c = correct samples, and C is the binomial coefficient.
// For each unique task, it counts total attempts and successes across results,
// then averages the per-task pass@k. Returns a percentage (0–100).
func PassK(results []*EvalResult, k int) float64 {
	if len(results) == 0 || k <= 0 {
		return 0
	}

	// Group results by task_id
	type taskStats struct {
		total   int
		correct int
	}
	byTask := make(map[string]*taskStats)
	for _, r := range results {
		st, ok := byTask[r.TaskID]
		if !ok {
			st = &taskStats{}
			byTask[r.TaskID] = st
		}
		st.total++
		if r.Passed {
			st.correct++
		}
	}

	if len(byTask) == 0 {
		return 0
	}

	var sum float64
	for _, st := range byTask {
		n := st.total
		c := st.correct
		if k > n {
			// Not enough samples — fall back to empirical rate
			if c > 0 {
				sum += 1
			}
			continue
		}
		// pass@k = 1 - C(n-c, k) / C(n, k)
		// Use log-space to avoid overflow
		sum += 1.0 - math.Exp(logComb(n-c, k)-logComb(n, k))
	}

	return sum / float64(len(byTask)) * 100
}

// logComb computes ln(C(n, k)) using the log-gamma function.
func logComb(n, k int) float64 {
	if k > n || k < 0 {
		return math.Inf(-1) // C(n,k) = 0 → log = -inf
	}
	if k == 0 || k == n {
		return 0 // C(n,0) = C(n,n) = 1 → log = 0
	}
	// ln(C(n,k)) = ln(n!) - ln(k!) - ln((n-k)!)
	nf, _ := math.Lgamma(float64(n + 1))
	kf, _ := math.Lgamma(float64(k + 1))
	nkf, _ := math.Lgamma(float64(n - k + 1))
	return nf - kf - nkf
}

// ModelComparison holds a side-by-side comparison between two models.
type ModelComparison struct {
	ModelA       string  `json:"model_a"`
	ModelB       string  `json:"model_b"`
	PassRateA    float64 `json:"pass_rate_a"`
	PassRateB    float64 `json:"pass_rate_b"`
	Delta        float64 `json:"delta"`
	AvgDurationA int64   `json:"avg_duration_a_ms"`
	AvgDurationB int64   `json:"avg_duration_b_ms"`
	AvgTokensA   int64   `json:"avg_tokens_a"`
	AvgTokensB   int64   `json:"avg_tokens_b"`
	TotalCostA   float64 `json:"total_cost_a_usd"`
	TotalCostB   float64 `json:"total_cost_b_usd"`
	Winner       string  `json:"winner"`
}

// CompareModels produces a comparison between two sets of eval results (one per model).
func CompareModels(resultsA, resultsB []*EvalResult) *ModelComparison {
	mc := &ModelComparison{}

	if len(resultsA) > 0 {
		mc.ModelA = resultsA[0].Model
	}
	if len(resultsB) > 0 {
		mc.ModelB = resultsB[0].Model
	}

	mc.PassRateA = Pass1(resultsA)
	mc.PassRateB = Pass1(resultsB)
	mc.Delta = mc.PassRateA - mc.PassRateB

	mc.AvgDurationA = avgDuration(resultsA)
	mc.AvgDurationB = avgDuration(resultsB)
	mc.AvgTokensA = avgTokens(resultsA)
	mc.AvgTokensB = avgTokens(resultsB)
	mc.TotalCostA = totalCost(resultsA)
	mc.TotalCostB = totalCost(resultsB)

	switch {
	case mc.PassRateA > mc.PassRateB:
		mc.Winner = mc.ModelA
	case mc.PassRateB > mc.PassRateA:
		mc.Winner = mc.ModelB
	default:
		mc.Winner = "tie"
	}

	return mc
}

func avgDuration(results []*EvalResult) int64 {
	if len(results) == 0 {
		return 0
	}
	var total int64
	for _, r := range results {
		total += r.DurationMs
	}
	return total / int64(len(results))
}

func avgTokens(results []*EvalResult) int64 {
	if len(results) == 0 {
		return 0
	}
	var total int64
	for _, r := range results {
		total += r.TokensUsed
	}
	return total / int64(len(results))
}

func totalCost(results []*EvalResult) float64 {
	var total float64
	for _, r := range results {
		total += r.CostUSD
	}
	return total
}
