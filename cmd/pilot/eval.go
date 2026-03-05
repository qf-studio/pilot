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

	cmd.AddCommand(newEvalCheckCmd())

	return cmd
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
