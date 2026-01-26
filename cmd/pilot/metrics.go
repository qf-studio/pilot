package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/spf13/cobra"
)

func newMetricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "View execution metrics and analytics",
		Long:  `View aggregated metrics, daily breakdowns, and export data for analysis.`,
	}

	cmd.AddCommand(
		newMetricsSummaryCmd(),
		newMetricsDailyCmd(),
		newMetricsProjectsCmd(),
		newMetricsExportCmd(),
	)

	return cmd
}

func newMetricsSummaryCmd() *cobra.Command {
	var (
		days     int
		projects []string
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show metrics summary for the last N days",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.MetricsQuery{
				Start:    start,
				End:      end,
				Projects: projects,
			}

			// Get summary
			summary, err := store.GetMetricsSummary(query)
			if err != nil {
				return fmt.Errorf("failed to get metrics: %w", err)
			}

			// Display summary
			fmt.Println()
			fmt.Printf("📊 Pilot Metrics Summary (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			// Execution stats
			fmt.Println("📈 Executions")
			fmt.Printf("   Total:    %d\n", summary.TotalExecutions)
			fmt.Printf("   Success:  %d (%.1f%%)\n", summary.SuccessCount, summary.SuccessRate*100)
			fmt.Printf("   Failed:   %d\n", summary.FailedCount)
			fmt.Printf("   PRs:      %d\n", summary.PRsCreated)
			fmt.Println()

			// Duration stats
			fmt.Println("⏱️  Duration")
			fmt.Printf("   Total:    %s\n", formatDuration(summary.TotalDurationMs))
			fmt.Printf("   Average:  %s\n", formatDuration(summary.AvgDurationMs))
			if summary.MinDurationMs > 0 {
				fmt.Printf("   Fastest:  %s\n", formatDuration(summary.MinDurationMs))
			}
			if summary.MaxDurationMs > 0 {
				fmt.Printf("   Slowest:  %s\n", formatDuration(summary.MaxDurationMs))
			}
			fmt.Println()

			// Token stats
			fmt.Println("🔤 Tokens")
			fmt.Printf("   Total:    %s\n", formatTokens(summary.TotalTokens))
			fmt.Printf("   Input:    %s\n", formatTokens(summary.TotalTokensInput))
			fmt.Printf("   Output:   %s\n", formatTokens(summary.TotalTokensOutput))
			if summary.TotalExecutions > 0 {
				fmt.Printf("   Avg/Task: %s\n", formatTokens(summary.AvgTokensPerTask))
			}
			fmt.Println()

			// Cost stats
			fmt.Println("💰 Estimated Cost")
			fmt.Printf("   Total:    $%.2f\n", summary.TotalCostUSD)
			if summary.TotalExecutions > 0 {
				fmt.Printf("   Avg/Task: $%.4f\n", summary.AvgCostUSD)
			}
			fmt.Println()

			// Code changes
			fmt.Println("📝 Code Changes")
			fmt.Printf("   Files:    %d\n", summary.TotalFilesChanged)
			fmt.Printf("   Added:    +%d lines\n", summary.TotalLinesAdded)
			fmt.Printf("   Removed:  -%d lines\n", summary.TotalLinesRemoved)
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Number of days to include")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Filter by project paths")

	return cmd
}

func newMetricsDailyCmd() *cobra.Command {
	var (
		days     int
		projects []string
	)

	cmd := &cobra.Command{
		Use:   "daily",
		Short: "Show daily metrics breakdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.MetricsQuery{
				Start:    start,
				End:      end,
				Projects: projects,
			}

			// Get daily metrics
			metrics, err := store.GetDailyMetrics(query)
			if err != nil {
				return fmt.Errorf("failed to get daily metrics: %w", err)
			}

			if len(metrics) == 0 {
				fmt.Println("No executions found in the specified period.")
				return nil
			}

			// Display daily breakdown
			fmt.Println()
			fmt.Printf("📅 Daily Metrics (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("%-12s %6s %6s %6s %10s %12s %10s\n", "Date", "Total", "Pass", "Fail", "Duration", "Tokens", "Cost")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, m := range metrics {
				successRate := ""
				if m.ExecutionCount > 0 {
					rate := float64(m.SuccessCount) / float64(m.ExecutionCount) * 100
					successRate = fmt.Sprintf("%.0f%%", rate)
				}
				_ = successRate // unused for now, keeping format simple

				fmt.Printf("%-12s %6d %6d %6d %10s %12s %10s\n",
					m.Date.Format("2006-01-02"),
					m.ExecutionCount,
					m.SuccessCount,
					m.FailedCount,
					formatDurationShort(m.TotalDurationMs),
					formatTokensShort(m.TotalTokens),
					fmt.Sprintf("$%.2f", m.TotalCostUSD),
				)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Number of days to include")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Filter by project paths")

	return cmd
}

func newMetricsProjectsCmd() *cobra.Command {
	var (
		days  int
		limit int
	)

	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Show metrics by project",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.MetricsQuery{
				Start: start,
				End:   end,
			}

			// Get project metrics
			metrics, err := store.GetProjectMetrics(query)
			if err != nil {
				return fmt.Errorf("failed to get project metrics: %w", err)
			}

			if len(metrics) == 0 {
				fmt.Println("No executions found in the specified period.")
				return nil
			}

			// Limit results
			if limit > 0 && len(metrics) > limit {
				metrics = metrics[:limit]
			}

			// Display project breakdown
			fmt.Println()
			fmt.Printf("📁 Project Metrics (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, m := range metrics {
				fmt.Printf("\n📦 %s\n", m.ProjectName)
				fmt.Printf("   Path:     %s\n", shortenPath(m.ProjectPath))
				fmt.Printf("   Tasks:    %d (%.1f%% success)\n", m.ExecutionCount, m.SuccessRate*100)
				fmt.Printf("   Duration: %s\n", formatDuration(m.TotalDurationMs))
				fmt.Printf("   Tokens:   %s\n", formatTokens(m.TotalTokens))
				fmt.Printf("   Cost:     $%.2f\n", m.TotalCostUSD)
				fmt.Printf("   Last:     %s\n", m.LastExecution.Format("2006-01-02 15:04"))
			}

			fmt.Println()
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Number of days to include")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum projects to show")

	return cmd
}

func newMetricsExportCmd() *cobra.Command {
	var (
		days     int
		projects []string
		format   string
		output   string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export metrics data to JSON or CSV",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.MetricsQuery{
				Start:    start,
				End:      end,
				Projects: projects,
			}

			// Export data
			data, err := store.ExportMetrics(query)
			if err != nil {
				return fmt.Errorf("failed to export metrics: %w", err)
			}

			if len(data) == 0 {
				fmt.Println("No executions found in the specified period.")
				return nil
			}

			// Determine output destination
			var writer *os.File
			if output == "" || output == "-" {
				writer = os.Stdout
			} else {
				writer, err = os.Create(output)
				if err != nil {
					return fmt.Errorf("failed to create output file: %w", err)
				}
				defer func() { _ = writer.Close() }()
			}

			// Export based on format
			switch format {
			case "json":
				encoder := json.NewEncoder(writer)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(data); err != nil {
					return fmt.Errorf("failed to write JSON: %w", err)
				}

			case "csv":
				csvWriter := csv.NewWriter(writer)
				defer csvWriter.Flush()

				// Write header
				header := []string{
					"id", "task_id", "project_path", "status", "duration_ms",
					"tokens_input", "tokens_output", "tokens_total", "estimated_cost_usd",
					"files_changed", "lines_added", "lines_removed", "model_name",
					"pr_url", "commit_sha", "created_at", "completed_at",
				}
				if err := csvWriter.Write(header); err != nil {
					return fmt.Errorf("failed to write CSV header: %w", err)
				}

				// Write rows
				for _, e := range data {
					completedAt := ""
					if e.CompletedAt != nil {
						completedAt = e.CompletedAt.Format(time.RFC3339)
					}

					row := []string{
						e.ID, e.TaskID, e.ProjectPath, e.Status,
						fmt.Sprintf("%d", e.DurationMs),
						fmt.Sprintf("%d", e.TokensInput),
						fmt.Sprintf("%d", e.TokensOutput),
						fmt.Sprintf("%d", e.TokensTotal),
						fmt.Sprintf("%.6f", e.EstimatedCostUSD),
						fmt.Sprintf("%d", e.FilesChanged),
						fmt.Sprintf("%d", e.LinesAdded),
						fmt.Sprintf("%d", e.LinesRemoved),
						e.ModelName, e.PRUrl, e.CommitSHA,
						e.CreatedAt.Format(time.RFC3339),
						completedAt,
					}
					if err := csvWriter.Write(row); err != nil {
						return fmt.Errorf("failed to write CSV row: %w", err)
					}
				}

			default:
				return fmt.Errorf("unsupported format: %s (use 'json' or 'csv')", format)
			}

			if output != "" && output != "-" {
				fmt.Printf("Exported %d records to %s\n", len(data), output)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Number of days to include")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Filter by project paths")
	cmd.Flags().StringVar(&format, "format", "json", "Output format (json or csv)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (- for stdout)")

	return cmd
}

// Helper functions

func formatDuration(ms int64) string {
	if ms == 0 {
		return "0s"
	}

	d := time.Duration(ms) * time.Millisecond

	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func formatDurationShort(ms int64) string {
	if ms == 0 {
		return "0s"
	}

	d := time.Duration(ms) * time.Millisecond

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func formatTokens(tokens int64) string {
	if tokens == 0 {
		return "0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(tokens)/1_000_000)
}

func formatTokensShort(tokens int64) string {
	if tokens == 0 {
		return "0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%dK", tokens/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
}

func shortenPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && len(path) > len(home) && path[:len(home)] == home {
		return "~" + path[len(home):]
	}
	// Just show the last 2 components
	parts := []string{}
	for path != "" && path != "/" {
		dir := filepath.Base(path)
		parts = append([]string{dir}, parts...)
		path = filepath.Dir(path)
		if len(parts) >= 2 {
			break
		}
	}
	if len(parts) > 0 {
		return ".../" + filepath.Join(parts...)
	}
	return path
}
