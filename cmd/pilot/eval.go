package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/memory"
)

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Evaluation and regression testing commands",
		Long:  `Commands for managing eval tasks and checking for regressions between eval runs.`,
	}

	cmd.AddCommand(
		newEvalRunCmd(),
		newEvalListCmd(),
		newEvalStatsCmd(),
		newEvalCheckCmd(),
	)

	return cmd
}

func newEvalRunCmd() *cobra.Command {
	var (
		repo  string
		model string
		limit int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run eval tasks for a repository",
		Long: `Load eval tasks from the store and re-execute them as benchmarks.
Selects tasks by repository, optionally overriding the model used.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo flag is required")
			}

			store, cleanup, err := openEvalStore()
			if err != nil {
				return err
			}
			defer cleanup()

			tasks, err := store.ListEvalTasks(memory.EvalTaskFilter{
				Repo:  repo,
				Limit: limit,
			})
			if err != nil {
				return fmt.Errorf("failed to load eval tasks: %w", err)
			}

			if len(tasks) == 0 {
				fmt.Printf("No eval tasks found for repo %q\n", repo)
				return nil
			}

			modelLabel := model
			if modelLabel == "" {
				modelLabel = "(default)"
			}

			fmt.Println("=== Eval Run Plan ===")
			fmt.Println()
			fmt.Printf("  Repo:   %s\n", repo)
			fmt.Printf("  Model:  %s\n", modelLabel)
			fmt.Printf("  Tasks:  %d\n", len(tasks))
			fmt.Println()

			passed := 0
			for i, t := range tasks {
				status := "FAIL"
				if t.Success {
					status = "PASS"
					passed++
				}
				fmt.Printf("  %3d. [%s] #%d %s (%s)\n",
					i+1, status, t.IssueNumber, t.IssueTitle, t.ID)
			}

			fmt.Println()
			fmt.Printf("  Historical pass@1: %.1f%% (%d/%d)\n",
				evalPassRate(tasks), passed, len(tasks))

			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Repository (owner/name) to evaluate")
	cmd.Flags().StringVar(&model, "model", "", "Model to use for evaluation (default: config model)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of tasks to run")

	return cmd
}

func newEvalListCmd() *cobra.Command {
	var (
		repo        string
		limit       int
		successOnly bool
		failedOnly  bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List eval tasks",
		Long:  `Display eval tasks from the store with optional filters.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, cleanup, err := openEvalStore()
			if err != nil {
				return err
			}
			defer cleanup()

			tasks, err := store.ListEvalTasks(memory.EvalTaskFilter{
				Repo:        repo,
				SuccessOnly: successOnly,
				FailedOnly:  failedOnly,
				Limit:       limit,
			})
			if err != nil {
				return fmt.Errorf("failed to list eval tasks: %w", err)
			}

			if len(tasks) == 0 {
				fmt.Println("No eval tasks found.")
				return nil
			}

			fmt.Println()
			fmt.Printf("%-18s %-6s %-8s %-40s %s\n",
				"ID", "Status", "Issue", "Title", "Repo")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, t := range tasks {
				status := "FAIL"
				if t.Success {
					status = "PASS"
				}
				title := t.IssueTitle
				if len(title) > 40 {
					title = title[:37] + "..."
				}
				fmt.Printf("%-18s %-6s #%-7d %-40s %s\n",
					t.ID, status, t.IssueNumber, title, t.Repo)
			}

			fmt.Println()
			fmt.Printf("Total: %d tasks\n", len(tasks))

			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repository (owner/name)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of tasks to show")
	cmd.Flags().BoolVar(&successOnly, "success", false, "Show only successful tasks")
	cmd.Flags().BoolVar(&failedOnly, "failed", false, "Show only failed tasks")

	return cmd
}

func newEvalStatsCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Print eval pass@1 metrics and model comparisons",
		Long: `Compute and display pass@1/pass@k metrics from stored eval tasks.
Shows per-repository breakdown and overall statistics.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, cleanup, err := openEvalStore()
			if err != nil {
				return err
			}
			defer cleanup()

			tasks, err := store.ListEvalTasks(memory.EvalTaskFilter{
				Repo:  repo,
				Limit: 10000,
			})
			if err != nil {
				return fmt.Errorf("failed to load eval tasks: %w", err)
			}

			if len(tasks) == 0 {
				fmt.Println("No eval tasks found.")
				return nil
			}

			printEvalStats(tasks, repo)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repository (owner/name)")

	return cmd
}

// openEvalStore loads config and opens the memory store. Returns the store,
// a cleanup function, and any error.
func openEvalStore() (*memory.Store, func(), error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open memory store: %w", err)
	}

	return store, func() { _ = store.Close() }, nil
}

// evalPassRate computes the pass@1 rate (percentage) from a set of eval tasks.
func evalPassRate(tasks []*memory.EvalTask) float64 {
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

// printEvalStats displays pass@1 metrics with per-repo breakdown.
func printEvalStats(tasks []*memory.EvalTask, filterRepo string) {
	// Group tasks by repo.
	byRepo := make(map[string][]*memory.EvalTask)
	for _, t := range tasks {
		byRepo[t.Repo] = append(byRepo[t.Repo], t)
	}

	fmt.Println("=== Eval Statistics ===")
	fmt.Println()

	// Overall stats.
	passed := 0
	failed := 0
	var totalDurationMs int64
	for _, t := range tasks {
		if t.Success {
			passed++
		} else {
			failed++
		}
		totalDurationMs += t.DurationMs
	}

	fmt.Printf("  Total tasks:  %d\n", len(tasks))
	fmt.Printf("  Passed:       %d\n", passed)
	fmt.Printf("  Failed:       %d\n", failed)
	fmt.Printf("  pass@1:       %.1f%%\n", evalPassRate(tasks))
	if len(tasks) > 0 {
		avgMs := totalDurationMs / int64(len(tasks))
		fmt.Printf("  Avg duration: %s\n", formatDuration(avgMs))
	}
	fmt.Println()

	// Per-repo breakdown (only when not filtered to a single repo).
	if filterRepo == "" && len(byRepo) > 1 {
		fmt.Println("  Per-repository breakdown:")
		fmt.Println()
		fmt.Printf("  %-35s %6s %6s %6s %8s\n", "Repository", "Total", "Pass", "Fail", "pass@1")
		fmt.Println("  " + "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		for repoName, repoTasks := range byRepo {
			rPassed := 0
			for _, t := range repoTasks {
				if t.Success {
					rPassed++
				}
			}
			rFailed := len(repoTasks) - rPassed
			fmt.Printf("  %-35s %6d %6d %6d %7.1f%%\n",
				repoName, len(repoTasks), rPassed, rFailed, evalPassRate(repoTasks))
		}
		fmt.Println()
	}

	// Pass criteria breakdown.
	criteriaStats := make(map[string][2]int) // [passed, total]
	for _, t := range tasks {
		for _, c := range t.PassCriteria {
			counts := criteriaStats[c.Type]
			if c.Passed {
				counts[0]++
			}
			counts[1]++
			criteriaStats[c.Type] = counts
		}
	}

	if len(criteriaStats) > 0 {
		fmt.Println("  Quality gate pass rates:")
		fmt.Println()
		for gate, counts := range criteriaStats {
			rate := float64(counts[0]) / float64(counts[1]) * 100
			fmt.Printf("    %-12s %.1f%% (%d/%d)\n", gate, rate, counts[0], counts[1])
		}
		fmt.Println()
	}
}

func newEvalCheckCmd() *cobra.Command {
	var (
		baseline  string
		current   string
		threshold float64
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for eval regressions between two runs",
		Long: `Compare pass@1 rates between baseline and current eval runs.
Exits with code 1 if a regression is detected (CI-friendly).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if baseline == "" || current == "" {
				return fmt.Errorf("both --baseline and --current flags are required")
			}

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			baselineTasks, err := store.ListEvalTasks(memory.EvalTaskFilter{
				ExecutionID: baseline,
				Limit:       1000,
			})
			if err != nil {
				return fmt.Errorf("failed to load baseline tasks: %w", err)
			}

			currentTasks, err := store.ListEvalTasks(memory.EvalTaskFilter{
				ExecutionID: current,
				Limit:       1000,
			})
			if err != nil {
				return fmt.Errorf("failed to load current tasks: %w", err)
			}

			if len(baselineTasks) == 0 {
				return fmt.Errorf("no eval tasks found for baseline run %q", baseline)
			}
			if len(currentTasks) == 0 {
				return fmt.Errorf("no eval tasks found for current run %q", current)
			}

			report := memory.CheckRegression(baselineTasks, currentTasks, threshold)

			printEvalReport(report, baseline, current, threshold)

			// Emit alert event if regression detected and alert engine is available
			if report.Regressed {
				emitEvalRegressionAlert(cfg, report)
				os.Exit(1)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&baseline, "baseline", "", "Baseline run ID (execution_id)")
	cmd.Flags().StringVar(&current, "current", "", "Current run ID (execution_id)")
	cmd.Flags().Float64Var(&threshold, "threshold", memory.DefaultRegressionThreshold, "Regression threshold in percentage points")

	return cmd
}

func printEvalReport(report *memory.RegressionReport, baseline, current string, threshold float64) {
	fmt.Println("=== Eval Regression Report ===")
	fmt.Println()
	fmt.Printf("  Baseline run:  %s\n", baseline)
	fmt.Printf("  Current run:   %s\n", current)
	fmt.Printf("  Threshold:     %.1fpp\n", threshold)
	fmt.Println()
	fmt.Printf("  Baseline pass@1: %.1f%%\n", report.BaselinePassRate)
	fmt.Printf("  Current pass@1:  %.1f%%\n", report.CurrentPassRate)
	fmt.Printf("  Delta:           %+.1fpp\n", report.Delta)
	fmt.Println()

	if len(report.RegressedTaskIDs) > 0 {
		fmt.Printf("  Regressed tasks (%d):\n", len(report.RegressedTaskIDs))
		for _, id := range report.RegressedTaskIDs {
			fmt.Printf("    - %s\n", id)
		}
		fmt.Println()
	}

	if len(report.ImprovedTaskIDs) > 0 {
		fmt.Printf("  Improved tasks (%d):\n", len(report.ImprovedTaskIDs))
		for _, id := range report.ImprovedTaskIDs {
			fmt.Printf("    - %s\n", id)
		}
		fmt.Println()
	}

	if report.Regressed {
		fmt.Println("  Result: REGRESSION DETECTED")
	} else {
		fmt.Println("  Result: OK")
	}
	fmt.Println()
	fmt.Printf("  Recommendation: %s\n", report.Recommendation)
}

func emitEvalRegressionAlert(cfg *config.Config, report *memory.RegressionReport) {
	alertsCfg := getAlertsConfig(cfg)
	if alertsCfg == nil {
		return
	}
	alertsCfg.Enabled = true

	dispatcher := alerts.NewDispatcher(alertsCfg)
	engine := alerts.NewEngine(alertsCfg, alerts.WithDispatcher(dispatcher))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		return
	}
	defer engine.Stop()

	engine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeEvalRegression,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"baseline_pass1":  fmt.Sprintf("%.1f", report.BaselinePassRate),
			"current_pass1":   fmt.Sprintf("%.1f", report.CurrentPassRate),
			"delta":           fmt.Sprintf("%.1f", report.Delta),
			"regressed_count": strconv.Itoa(len(report.RegressedTaskIDs)),
			"recommendation":  report.Recommendation,
		},
	})
}
