package main

import (
	"context"
	"fmt"
	"time"

	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/spf13/cobra"
)

func newBudgetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "View and manage cost controls",
		Long:  `View budget status, limits, and manage cost controls.`,
	}

	cmd.AddCommand(
		newBudgetStatusCmd(),
		newBudgetConfigCmd(),
		newBudgetResetCmd(),
	)

	return cmd
}

func newBudgetStatusCmd() *cobra.Command {
	var userID string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current budget status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Create enforcer
			budgetCfg := cfg.Budget
			if budgetCfg == nil {
				budgetCfg = budget.DefaultConfig()
			}

			enforcer := budget.NewEnforcer(budgetCfg, store)

			// Get status
			ctx := context.Background()
			status, err := enforcer.GetStatus(ctx, "", userID)
			if err != nil {
				return fmt.Errorf("failed to get budget status: %w", err)
			}

			// Display status
			fmt.Println()
			fmt.Println("💰 Budget Status")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			if !budgetCfg.Enabled {
				fmt.Println("   ⚠️  Budget controls are DISABLED")
				fmt.Println()
				fmt.Println("   Enable in config.yaml:")
				fmt.Println("   budget:")
				fmt.Println("     enabled: true")
				fmt.Println()
			}

			// Daily status
			dailyBar := renderProgressBar(status.DailyPercent, 25)
			dailyIcon := "✅"
			if status.DailyPercent >= 100 {
				dailyIcon = "🚫"
			} else if status.DailyPercent >= 80 {
				dailyIcon = "⚠️"
			}
			fmt.Printf("Daily:   %s $%.2f / $%.2f (%s %.0f%%)\n",
				dailyIcon,
				status.DailySpent,
				status.DailyLimit,
				dailyBar,
				status.DailyPercent,
			)

			// Monthly status
			monthlyBar := renderProgressBar(status.MonthlyPercent, 25)
			monthlyIcon := "✅"
			if status.MonthlyPercent >= 100 {
				monthlyIcon = "🚫"
			} else if status.MonthlyPercent >= 80 {
				monthlyIcon = "⚠️"
			}
			fmt.Printf("Monthly: %s $%.2f / $%.2f (%s %.0f%%)\n",
				monthlyIcon,
				status.MonthlySpent,
				status.MonthlyLimit,
				monthlyBar,
				status.MonthlyPercent,
			)

			fmt.Println()

			// Show paused/blocked status
			if status.IsPaused {
				fmt.Printf("🛑 New tasks are PAUSED: %s\n", status.PauseReason)
				fmt.Println()
			}

			if status.BlockedTasks > 0 {
				fmt.Printf("⚠️  %d task(s) blocked due to budget limits\n", status.BlockedTasks)
				fmt.Println()
			}

			// Per-task limits
			if budgetCfg.Enabled {
				fmt.Println("Per-Task Limits:")
				if budgetCfg.PerTask.MaxTokens > 0 {
					fmt.Printf("   Max Tokens:   %s\n", formatTokens(budgetCfg.PerTask.MaxTokens))
				} else {
					fmt.Println("   Max Tokens:   (unlimited)")
				}
				if budgetCfg.PerTask.MaxDuration > 0 {
					fmt.Printf("   Max Duration: %s\n", budgetCfg.PerTask.MaxDuration)
				} else {
					fmt.Println("   Max Duration: (unlimited)")
				}
				fmt.Println()

				fmt.Println("Enforcement Actions:")
				fmt.Printf("   On daily limit:   %s\n", budgetCfg.OnExceed.Daily)
				fmt.Printf("   On monthly limit: %s\n", budgetCfg.OnExceed.Monthly)
				fmt.Printf("   On per-task limit: %s\n", budgetCfg.OnExceed.PerTask)
			}

			fmt.Println()
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("Last updated: %s\n", status.LastUpdated.Format(time.RFC3339))

			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")

	return cmd
}

func newBudgetConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show budget configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			budgetCfg := cfg.Budget
			if budgetCfg == nil {
				budgetCfg = budget.DefaultConfig()
			}

			fmt.Println()
			fmt.Println("⚙️  Budget Configuration")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()
			fmt.Printf("Enabled:       %v\n", budgetCfg.Enabled)
			fmt.Printf("Daily Limit:   $%.2f\n", budgetCfg.DailyLimit)
			fmt.Printf("Monthly Limit: $%.2f\n", budgetCfg.MonthlyLimit)
			fmt.Println()
			fmt.Println("Per-Task Limits:")
			fmt.Printf("   Max Tokens:   %d\n", budgetCfg.PerTask.MaxTokens)
			fmt.Printf("   Max Duration: %s\n", budgetCfg.PerTask.MaxDuration)
			fmt.Println()
			fmt.Println("On Exceed Actions:")
			fmt.Printf("   Daily:   %s\n", budgetCfg.OnExceed.Daily)
			fmt.Printf("   Monthly: %s\n", budgetCfg.OnExceed.Monthly)
			fmt.Printf("   PerTask: %s\n", budgetCfg.OnExceed.PerTask)
			fmt.Println()
			fmt.Println("Thresholds:")
			fmt.Printf("   Warning at: %.0f%%\n", budgetCfg.Thresholds.WarnPercent)
			fmt.Println()

			fmt.Println("YAML configuration example:")
			fmt.Println("─────────────────────────────────────")
			fmt.Println("budget:")
			fmt.Println("  enabled: true")
			fmt.Println("  daily_limit: 50.00")
			fmt.Println("  monthly_limit: 500.00")
			fmt.Println("  per_task:")
			fmt.Println("    max_tokens: 100000")
			fmt.Println("    max_duration: 30m")
			fmt.Println("  on_exceed:")
			fmt.Println("    daily: pause")
			fmt.Println("    monthly: stop")
			fmt.Println("    per_task: stop")
			fmt.Println("  thresholds:")
			fmt.Println("    warn_percent: 80")
			fmt.Println("─────────────────────────────────────")

			return nil
		},
	}

	return cmd
}

func newBudgetResetCmd() *cobra.Command {
	var confirm bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset blocked tasks counter",
		Long:  `Reset the blocked tasks counter and resume task execution if paused due to daily limits.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				fmt.Println("⚠️  This will reset the blocked tasks counter and may resume paused execution.")
				fmt.Println("   Use --confirm to proceed.")
				return nil
			}

			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Create enforcer and reset
			budgetCfg := cfg.Budget
			if budgetCfg == nil {
				budgetCfg = budget.DefaultConfig()
			}

			enforcer := budget.NewEnforcer(budgetCfg, store)
			enforcer.ResetDaily()

			fmt.Println("✅ Budget counters reset")
			fmt.Println("   Blocked tasks counter: 0")
			fmt.Println("   Daily pause status: cleared")

			return nil
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm the reset operation")

	return cmd
}

// loadConfig loads the application configuration
func loadConfig() (*config.Config, error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// renderProgressBar renders a text progress bar
func renderProgressBar(percent float64, width int) string {
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int(float64(width) * percent / 100)
	empty := width - filled

	bar := ""
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}

	return bar
}

// Note: formatTokens is defined in metrics.go and shared across CLI commands
