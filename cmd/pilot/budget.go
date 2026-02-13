package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// Budget status TUI constants
const (
	budgetWidth    = 60
	budgetBarWidth = 40
	budgetLabelCol = 14
)

// Budget status TUI styles
var (
	budgetHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("255"))

	budgetDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	budgetWarnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	budgetErrorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196"))

	budgetBarNormal = lipgloss.Color("255")
	budgetBarWarn   = lipgloss.Color("214")
	budgetBarError  = lipgloss.Color("196")
)

func budgetDivider() string {
	return budgetDimStyle.Render(strings.Repeat("─", budgetWidth))
}

func newBudgetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "View and manage cost controls",
		Long:  `View budget status, limits, and manage cost controls.`,
	}

	cmd.AddCommand(
		newBudgetStatusCmd(),
		newBudgetConfigCmd(),
		newBudgetSetCmd(),
		newBudgetResetCmd(),
		newBudgetAlertCmd(),
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

			// Render and print
			output := renderBudgetStatus(budgetCfg, status)
			fmt.Print(output)

			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")

	return cmd
}

// renderBudgetStatus renders the complete budget status display
func renderBudgetStatus(cfg *budget.Config, status *budget.Status) string {
	var b strings.Builder

	// Header
	b.WriteString(budgetHeaderStyle.Render("BUDGET STATUS"))
	b.WriteString("\n")
	b.WriteString(budgetDivider())
	b.WriteString("\n\n")

	// Status indicator line
	if !cfg.Enabled {
		b.WriteString("  ")
		b.WriteString(budgetDimStyle.Render("[DISABLED]"))
		b.WriteString(" Enable with: budget.enabled: true\n\n")
	} else if status.DailyPercent >= 100 || status.MonthlyPercent >= 100 {
		b.WriteString("  ")
		b.WriteString(budgetErrorStyle.Render("[X] Budget limit exceeded"))
		b.WriteString("\n\n")
	} else if status.DailyPercent >= 80 || status.MonthlyPercent >= 80 {
		b.WriteString("  ")
		if status.DailyPercent >= 80 && status.DailyPercent < 100 {
			b.WriteString(budgetWarnStyle.Render("[!] Approaching daily limit"))
		} else {
			b.WriteString(budgetWarnStyle.Render("[!] Approaching monthly limit"))
		}
		b.WriteString("\n\n")
	}

	// Paused/blocked warnings
	if status.IsPaused {
		b.WriteString("  ")
		b.WriteString(budgetErrorStyle.Render(fmt.Sprintf("[PAUSED] %s", status.PauseReason)))
		b.WriteString("\n\n")
	}
	if status.BlockedTasks > 0 {
		b.WriteString("  ")
		b.WriteString(budgetWarnStyle.Render(fmt.Sprintf("[!] %d task(s) blocked", status.BlockedTasks)))
		b.WriteString("\n\n")
	}

	// Daily budget
	b.WriteString(formatBudgetLine("Daily", status.DailySpent, status.DailyLimit, status.DailyPercent))
	b.WriteString("\n")

	// Monthly budget
	b.WriteString(formatBudgetLine("Monthly", status.MonthlySpent, status.MonthlyLimit, status.MonthlyPercent))

	// Footer section
	b.WriteString(budgetDivider())
	b.WriteString("\n")

	// Limits line
	tokensStr := formatTokensCompact(cfg.PerTask.MaxTokens)
	durationStr := formatDurationCompact(cfg.PerTask.MaxDuration)
	b.WriteString(fmt.Sprintf("  %-14s%s tokens    %s duration\n", "Limits", tokensStr, durationStr))

	// Enforcement line
	b.WriteString(fmt.Sprintf("  %-14sdaily:%-7s monthly:%-7s task:%s\n",
		"Enforcement",
		cfg.OnExceed.Daily,
		cfg.OnExceed.Monthly,
		cfg.OnExceed.PerTask,
	))

	b.WriteString(budgetDivider())
	b.WriteString("\n")

	// Timestamp (right-aligned)
	timestamp := formatRelativeTimestamp(status.LastUpdated)
	padding := budgetWidth - len(timestamp)
	if padding < 0 {
		padding = 0
	}
	b.WriteString(budgetDimStyle.Render(fmt.Sprintf("%s%s", strings.Repeat(" ", padding), timestamp)))
	b.WriteString("\n")

	return b.String()
}

// formatBudgetLine formats a single budget line (label + values + bar)
func formatBudgetLine(label string, spent, limit, percent float64) string {
	var b strings.Builder

	// Format values
	values := fmt.Sprintf("$%.2f / $%.2f", spent, limit)

	// Calculate padding for right-aligned percentage
	// Layout: "  {label:14}{values}{padding}{percent:4}%"
	percentStr := fmt.Sprintf("%.0f%%", percent)
	contentLen := 2 + budgetLabelCol + len(values) + len(percentStr)
	padding := budgetWidth - contentLen
	if padding < 1 {
		padding = 1
	}

	// First line: label, values, percent
	b.WriteString(fmt.Sprintf("  %-*s%s%s%s\n",
		budgetLabelCol, label,
		values,
		strings.Repeat(" ", padding),
		percentStr,
	))

	// Second line: progress bar (indented to align with values)
	b.WriteString(fmt.Sprintf("  %s%s\n",
		strings.Repeat(" ", budgetLabelCol),
		renderColoredBar(percent, budgetBarWidth),
	))

	return b.String()
}

// renderColoredBar renders a progress bar with color based on percentage
func renderColoredBar(percent float64, width int) string {
	// Determine color
	color := budgetBarNormal
	if percent >= 100 {
		color = budgetBarError
	} else if percent >= 80 {
		color = budgetBarWarn
	}

	// Clamp percent
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int(float64(width) * percent / 100)
	empty := width - filled

	filledStyle := lipgloss.NewStyle().Foreground(color)
	emptyStyle := budgetDimStyle

	return filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty))
}

// formatRelativeTimestamp returns a human-friendly timestamp
func formatRelativeTimestamp(t time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	timeStr := t.Format("15:04")

	if t.After(today) {
		return fmt.Sprintf("Updated %s today", timeStr)
	}
	if t.After(yesterday) {
		return fmt.Sprintf("Updated %s yesterday", timeStr)
	}
	return fmt.Sprintf("Updated %s", t.Format("2006-01-02 15:04"))
}

// formatTokensCompact formats token count compactly (e.g., "100k")
func formatTokensCompact(tokens int64) string {
	if tokens <= 0 {
		return "unlimited"
	}
	if tokens >= 1000000 {
		return fmt.Sprintf("%.0fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.0fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

// formatDurationCompact formats duration compactly (e.g., "30m")
func formatDurationCompact(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	if d >= time.Hour {
		h := d.Hours()
		if h == float64(int(h)) {
			return fmt.Sprintf("%.0fh", h)
		}
		return fmt.Sprintf("%.1fh", h)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
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
			fmt.Println(budgetHeaderStyle.Render("BUDGET CONFIGURATION"))
			fmt.Println(budgetDivider())
			fmt.Println()
			fmt.Printf("  Enabled:       %v\n", budgetCfg.Enabled)
			fmt.Printf("  Daily Limit:   $%.2f\n", budgetCfg.DailyLimit)
			fmt.Printf("  Monthly Limit: $%.2f\n", budgetCfg.MonthlyLimit)
			fmt.Println()
			fmt.Println("  Per-Task Limits:")
			fmt.Printf("    Max Tokens:   %d\n", budgetCfg.PerTask.MaxTokens)
			fmt.Printf("    Max Duration: %s\n", budgetCfg.PerTask.MaxDuration)
			fmt.Println()
			fmt.Println("  On Exceed Actions:")
			fmt.Printf("    Daily:   %s\n", budgetCfg.OnExceed.Daily)
			fmt.Printf("    Monthly: %s\n", budgetCfg.OnExceed.Monthly)
			fmt.Printf("    PerTask: %s\n", budgetCfg.OnExceed.PerTask)
			fmt.Println()
			fmt.Println("  Thresholds:")
			fmt.Printf("    Warning at: %.0f%%\n", budgetCfg.Thresholds.WarnPercent)
			fmt.Println()
			fmt.Println(budgetDivider())
			fmt.Println()
			fmt.Println("  YAML configuration example:")
			fmt.Println(budgetDimStyle.Render("  " + strings.Repeat("─", 37)))
			fmt.Println("  budget:")
			fmt.Println("    enabled: true")
			fmt.Println("    daily_limit: 50.00")
			fmt.Println("    monthly_limit: 500.00")
			fmt.Println("    per_task:")
			fmt.Println("      max_tokens: 100000")
			fmt.Println("      max_duration: 30m")
			fmt.Println("    on_exceed:")
			fmt.Println("      daily: pause")
			fmt.Println("      monthly: stop")
			fmt.Println("      per_task: stop")
			fmt.Println("    thresholds:")
			fmt.Println("      warn_percent: 80")
			fmt.Println(budgetDimStyle.Render("  " + strings.Repeat("─", 37)))
			fmt.Println()

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
				fmt.Println()
				fmt.Println(budgetWarnStyle.Render("[!] This will reset the blocked tasks counter and may resume paused execution."))
				fmt.Println("    Use --confirm to proceed.")
				fmt.Println()
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

			fmt.Println()
			fmt.Println("[OK] Budget counters reset")
			fmt.Println("     Blocked tasks counter: 0")
			fmt.Println("     Daily pause status: cleared")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm the reset operation")

	return cmd
}

func newBudgetSetCmd() *cobra.Command {
	var (
		daily   float64
		monthly float64
		enabled bool
	)

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set budget limits",
		Long: `Update budget limits in the configuration.

Examples:
  pilot budget set --daily 50       # Set daily limit to $50
  pilot budget set --monthly 500    # Set monthly limit to $500
  pilot budget set --enabled        # Enable budget enforcement
  pilot budget set --daily 100 --monthly 1000 --enabled`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Initialize budget config if nil
			if cfg.Budget == nil {
				cfg.Budget = budget.DefaultConfig()
			}

			// Track what changed
			changes := []string{}

			// Apply changes
			if cmd.Flags().Changed("daily") {
				cfg.Budget.DailyLimit = daily
				changes = append(changes, fmt.Sprintf("daily_limit: $%.2f", daily))
			}
			if cmd.Flags().Changed("monthly") {
				cfg.Budget.MonthlyLimit = monthly
				changes = append(changes, fmt.Sprintf("monthly_limit: $%.2f", monthly))
			}
			if cmd.Flags().Changed("enabled") {
				cfg.Budget.Enabled = enabled
				changes = append(changes, fmt.Sprintf("enabled: %v", enabled))
			}

			if len(changes) == 0 {
				fmt.Println("No changes specified. Use flags to set values:")
				fmt.Println("  --daily <amount>     Set daily limit (USD)")
				fmt.Println("  --monthly <amount>   Set monthly limit (USD)")
				fmt.Println("  --enabled            Enable budget enforcement")
				return nil
			}

			// Save updated config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Println()
			fmt.Println(budgetHeaderStyle.Render("BUDGET UPDATED"))
			fmt.Println(budgetDivider())
			for _, change := range changes {
				fmt.Printf("  ✓ %s\n", change)
			}
			fmt.Println()
			fmt.Printf("  Config saved to: %s\n", configPath)
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().Float64Var(&daily, "daily", 0, "Daily budget limit (USD)")
	cmd.Flags().Float64Var(&monthly, "monthly", 0, "Monthly budget limit (USD)")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "Enable budget enforcement")

	return cmd
}

func newBudgetAlertCmd() *cobra.Command {
	var (
		warnPercent float64
		onDaily     string
		onMonthly   string
	)

	cmd := &cobra.Command{
		Use:   "alert",
		Short: "Configure budget alerts",
		Long: `Configure alert thresholds and actions when budget limits are approached or exceeded.

Actions:
  warn   - Log warning but continue processing
  pause  - Stop accepting new tasks, finish current
  stop   - Terminate immediately

Examples:
  pilot budget alert --warn-at 80                    # Warn at 80% usage
  pilot budget alert --on-daily pause               # Pause on daily limit
  pilot budget alert --on-monthly stop              # Stop on monthly limit
  pilot budget alert --warn-at 90 --on-daily warn   # Late warning, warn only`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Initialize budget config if nil
			if cfg.Budget == nil {
				cfg.Budget = budget.DefaultConfig()
			}

			// Track what changed
			changes := []string{}

			// Apply changes
			if cmd.Flags().Changed("warn-at") {
				cfg.Budget.Thresholds.WarnPercent = warnPercent
				changes = append(changes, fmt.Sprintf("warn_percent: %.0f%%", warnPercent))
			}
			if cmd.Flags().Changed("on-daily") {
				action := budget.Action(onDaily)
				if action != budget.ActionWarn && action != budget.ActionPause && action != budget.ActionStop {
					return fmt.Errorf("invalid action: %s (use warn, pause, or stop)", onDaily)
				}
				cfg.Budget.OnExceed.Daily = action
				changes = append(changes, fmt.Sprintf("on_exceed.daily: %s", onDaily))
			}
			if cmd.Flags().Changed("on-monthly") {
				action := budget.Action(onMonthly)
				if action != budget.ActionWarn && action != budget.ActionPause && action != budget.ActionStop {
					return fmt.Errorf("invalid action: %s (use warn, pause, or stop)", onMonthly)
				}
				cfg.Budget.OnExceed.Monthly = action
				changes = append(changes, fmt.Sprintf("on_exceed.monthly: %s", onMonthly))
			}

			if len(changes) == 0 {
				// Show current alert config
				fmt.Println()
				fmt.Println(budgetHeaderStyle.Render("BUDGET ALERT CONFIGURATION"))
				fmt.Println(budgetDivider())
				fmt.Println()
				fmt.Printf("  Warning threshold:  %.0f%%\n", cfg.Budget.Thresholds.WarnPercent)
				fmt.Println()
				fmt.Println("  On limit exceeded:")
				fmt.Printf("    Daily:   %s\n", cfg.Budget.OnExceed.Daily)
				fmt.Printf("    Monthly: %s\n", cfg.Budget.OnExceed.Monthly)
				fmt.Printf("    PerTask: %s\n", cfg.Budget.OnExceed.PerTask)
				fmt.Println()
				fmt.Println(budgetDivider())
				fmt.Println()
				fmt.Println("  Use flags to update:")
				fmt.Println("    --warn-at <percent>   Warning threshold percentage")
				fmt.Println("    --on-daily <action>   Action when daily limit exceeded")
				fmt.Println("    --on-monthly <action> Action when monthly limit exceeded")
				fmt.Println()
				return nil
			}

			// Save updated config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Println()
			fmt.Println(budgetHeaderStyle.Render("ALERT CONFIG UPDATED"))
			fmt.Println(budgetDivider())
			for _, change := range changes {
				fmt.Printf("  ✓ %s\n", change)
			}
			fmt.Println()
			fmt.Printf("  Config saved to: %s\n", configPath)
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().Float64Var(&warnPercent, "warn-at", 0, "Warning threshold percentage (e.g., 80)")
	cmd.Flags().StringVar(&onDaily, "on-daily", "", "Action when daily limit exceeded (warn, pause, stop)")
	cmd.Flags().StringVar(&onMonthly, "on-monthly", "", "Action when monthly limit exceeded (warn, pause, stop)")

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

// Note: formatTokens is defined in metrics.go and shared across CLI commands
