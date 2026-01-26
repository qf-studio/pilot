package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/spf13/cobra"
)

func newUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "View usage metering and billing data",
		Long:  `View billable usage events, summaries, and export data for billing.`,
	}

	cmd.AddCommand(
		newUsageSummaryCmd(),
		newUsageDailyCmd(),
		newUsageProjectsCmd(),
		newUsageEventsCmd(),
		newUsageExportCmd(),
	)

	return cmd
}

func newUsageSummaryCmd() *cobra.Command {
	var (
		days      int
		userID    string
		projectID string
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show usage summary for billing",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.UsageQuery{
				UserID:    userID,
				ProjectID: projectID,
				Start:     start,
				End:       end,
			}

			// Get summary
			summary, err := store.GetUsageSummary(query)
			if err != nil {
				return fmt.Errorf("failed to get usage summary: %w", err)
			}

			// Display summary
			fmt.Println()
			fmt.Printf("💰 Usage Summary (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			// Task usage
			fmt.Println("📋 Tasks")
			fmt.Printf("   Count:    %d\n", summary.TaskCount)
			fmt.Printf("   Cost:     $%.2f\n", summary.TaskCost)
			fmt.Println()

			// Token usage
			fmt.Println("🔤 Tokens")
			fmt.Printf("   Input:    %s\n", formatTokens(summary.TokensInput))
			fmt.Printf("   Output:   %s\n", formatTokens(summary.TokensOutput))
			fmt.Printf("   Total:    %s\n", formatTokens(summary.TokensTotal))
			fmt.Printf("   Cost:     $%.2f\n", summary.TokenCost)
			fmt.Println()

			// Compute usage
			fmt.Println("⚡ Compute")
			fmt.Printf("   Minutes:  %d\n", summary.ComputeMinutes)
			fmt.Printf("   Cost:     $%.2f\n", summary.ComputeCost)
			fmt.Println()

			// Storage (if any)
			if summary.StorageBytes > 0 {
				fmt.Println("💾 Storage")
				fmt.Printf("   Bytes:    %s\n", formatBytes(summary.StorageBytes))
				fmt.Printf("   Cost:     $%.2f\n", summary.StorageCost)
				fmt.Println()
			}

			// API calls (if any)
			if summary.APICallCount > 0 {
				fmt.Println("🌐 API Calls")
				fmt.Printf("   Count:    %d\n", summary.APICallCount)
				fmt.Printf("   Cost:     $%.2f\n", summary.APICallCost)
				fmt.Println()
			}

			// Total
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("💵 TOTAL COST:  $%.2f\n", summary.TotalCost)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Number of days to include")
	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")
	cmd.Flags().StringVar(&projectID, "project", "", "Filter by project ID")

	return cmd
}

func newUsageDailyCmd() *cobra.Command {
	var (
		days      int
		userID    string
		projectID string
	)

	cmd := &cobra.Command{
		Use:   "daily",
		Short: "Show daily usage breakdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.UsageQuery{
				UserID:    userID,
				ProjectID: projectID,
				Start:     start,
				End:       end,
			}

			// Get daily usage
			usage, err := store.GetDailyUsage(query)
			if err != nil {
				return fmt.Errorf("failed to get daily usage: %w", err)
			}

			if len(usage) == 0 {
				fmt.Println("No usage data found in the specified period.")
				return nil
			}

			// Display daily breakdown
			fmt.Println()
			fmt.Printf("📅 Daily Usage (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("%-12s %8s %10s %12s %10s %10s\n", "Date", "Tasks", "Task $", "Tokens", "Token $", "Total $")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, u := range usage {
				fmt.Printf("%-12s %8d %10s %12s %10s %10s\n",
					u.Date.Format("2006-01-02"),
					u.TaskCount,
					fmt.Sprintf("$%.2f", u.TaskCost),
					formatTokensShort(u.TokenCount),
					fmt.Sprintf("$%.2f", u.TokenCost),
					fmt.Sprintf("$%.2f", u.TotalCost),
				)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Number of days to include")
	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")
	cmd.Flags().StringVar(&projectID, "project", "", "Filter by project ID")

	return cmd
}

func newUsageProjectsCmd() *cobra.Command {
	var (
		days   int
		userID string
	)

	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Show usage by project",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.UsageQuery{
				UserID: userID,
				Start:  start,
				End:    end,
			}

			// Get project usage
			usage, err := store.GetUsageByProject(query)
			if err != nil {
				return fmt.Errorf("failed to get project usage: %w", err)
			}

			if len(usage) == 0 {
				fmt.Println("No usage data found in the specified period.")
				return nil
			}

			// Display project breakdown
			fmt.Println()
			fmt.Printf("📁 Usage by Project (Last %d Days)\n", days)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("%-40s %8s %12s %10s %10s\n", "Project", "Tasks", "Tokens", "Compute", "Cost")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, u := range usage {
				projectDisplay := u.ProjectID
				if len(projectDisplay) > 40 {
					projectDisplay = "..." + projectDisplay[len(projectDisplay)-37:]
				}

				fmt.Printf("%-40s %8d %12s %10s %10s\n",
					projectDisplay,
					u.TaskCount,
					formatTokensShort(u.TokenCount),
					fmt.Sprintf("%dm", u.ComputeMinutes),
					fmt.Sprintf("$%.2f", u.TotalCost),
				)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Number of days to include")
	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")

	return cmd
}

func newUsageEventsCmd() *cobra.Command {
	var (
		days      int
		userID    string
		projectID string
		eventType string
		limit     int
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show raw usage events",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.UsageQuery{
				UserID:    userID,
				ProjectID: projectID,
				Start:     start,
				End:       end,
			}
			if eventType != "" {
				query.EventType = memory.UsageEventType(eventType)
			}

			// Get events
			events, err := store.GetUsageEvents(query, limit)
			if err != nil {
				return fmt.Errorf("failed to get usage events: %w", err)
			}

			if len(events) == 0 {
				fmt.Println("No usage events found in the specified period.")
				return nil
			}

			// Display events
			fmt.Println()
			fmt.Printf("📊 Usage Events (Last %d Days, showing %d)\n", days, len(events))
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			for _, e := range events {
				fmt.Printf("\n%s | %s | %s\n",
					e.Timestamp.Format("2006-01-02 15:04"),
					e.EventType,
					e.ID,
				)
				fmt.Printf("   Project:  %s\n", shortenPath(e.ProjectID))
				fmt.Printf("   Quantity: %d | Unit: $%.4f | Total: $%.4f\n",
					e.Quantity, e.UnitCost, e.TotalCost)
				if e.ExecutionID != "" {
					fmt.Printf("   Execution: %s\n", e.ExecutionID)
				}
			}

			fmt.Println()
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "Number of days to include")
	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")
	cmd.Flags().StringVar(&projectID, "project", "", "Filter by project ID")
	cmd.Flags().StringVar(&eventType, "type", "", "Filter by event type (task, token, compute)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum events to show")

	return cmd
}

func newUsageExportCmd() *cobra.Command {
	var (
		days      int
		userID    string
		projectID string
		format    string
		output    string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export usage data to JSON or CSV",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			// Build query
			end := time.Now()
			start := end.AddDate(0, 0, -days)
			query := memory.UsageQuery{
				UserID:    userID,
				ProjectID: projectID,
				Start:     start,
				End:       end,
			}

			// Get events
			events, err := store.GetUsageEvents(query, 10000) // High limit for export
			if err != nil {
				return fmt.Errorf("failed to get usage events: %w", err)
			}

			if len(events) == 0 {
				fmt.Println("No usage events found in the specified period.")
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
				if err := encoder.Encode(events); err != nil {
					return fmt.Errorf("failed to write JSON: %w", err)
				}

			case "csv":
				csvWriter := csv.NewWriter(writer)
				defer csvWriter.Flush()

				// Write header
				header := []string{
					"id", "timestamp", "user_id", "project_id", "event_type",
					"quantity", "unit_cost", "total_cost", "execution_id",
				}
				if err := csvWriter.Write(header); err != nil {
					return fmt.Errorf("failed to write CSV header: %w", err)
				}

				// Write rows
				for _, e := range events {
					row := []string{
						e.ID,
						e.Timestamp.Format(time.RFC3339),
						e.UserID,
						e.ProjectID,
						string(e.EventType),
						fmt.Sprintf("%d", e.Quantity),
						fmt.Sprintf("%.6f", e.UnitCost),
						fmt.Sprintf("%.6f", e.TotalCost),
						e.ExecutionID,
					}
					if err := csvWriter.Write(row); err != nil {
						return fmt.Errorf("failed to write CSV row: %w", err)
					}
				}

			default:
				return fmt.Errorf("unsupported format: %s (use 'json' or 'csv')", format)
			}

			if output != "" && output != "-" {
				fmt.Printf("Exported %d usage events to %s\n", len(events), output)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 30, "Number of days to include")
	cmd.Flags().StringVar(&userID, "user", "", "Filter by user ID")
	cmd.Flags().StringVar(&projectID, "project", "", "Filter by project ID")
	cmd.Flags().StringVar(&format, "format", "json", "Output format (json or csv)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (- for stdout)")

	return cmd
}

// openStore is a helper to open the memory store
func openStore() (*memory.Store, error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open memory store: %w", err)
	}

	return store, nil
}

// formatBytes formats bytes as human-readable string
func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}
