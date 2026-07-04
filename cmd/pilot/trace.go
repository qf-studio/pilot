package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/memory"
)

// newTraceCmd implements `pilot trace <task-id>` (TASK-379 C4 / GH-3848): it
// renders the execution_events stage timeline for every execution recorded
// for a task, so retries and stage-level durations are visible without
// grepping raw logs.
func newTraceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trace <task-id>",
		Short: "Render the stage-transition timeline for a task's executions",
		Long: `Render the stage-transition timeline recorded in execution_events for
every execution of a task, newest execution first. Retries are rendered as
separate blocks; each stage line shows a UTC timestamp and the duration since
the previous stage.

Examples:
  pilot trace GH-42       # Show the stage timeline for task GH-42
  pilot trace TASK-379    # Show the stage timeline for a Navigator task ID`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			return runTrace(cmd.OutOrStdout(), store, args[0])
		},
	}

	return cmd
}

// runTrace writes the stage timeline for every execution of taskID to w,
// newest execution first (matching store.ListExecutionsForTask). Returns an
// error naming taskID when no executions are recorded, so the CLI exits
// non-zero with a clear message instead of printing an empty report.
func runTrace(w io.Writer, store *memory.Store, taskID string) error {
	executions, err := store.ListExecutionsForTask(taskID)
	if err != nil {
		return fmt.Errorf("failed to look up task %s: %w", taskID, err)
	}
	if len(executions) == 0 {
		return fmt.Errorf("no executions found for task %s", taskID)
	}

	for i, exec := range executions {
		events, err := store.ListExecutionEvents(exec.ID)
		if err != nil {
			return fmt.Errorf("failed to load stage events for execution %s: %w", exec.ID, err)
		}

		_, _ = fmt.Fprintf(w, "Execution %d/%d: %s (status: %s)\n", i+1, len(executions), exec.ID, exec.Status)
		_, _ = fmt.Fprintln(w, strings.Repeat("-", 60))
		writeEventTimeline(w, events)

		if i < len(executions)-1 {
			_, _ = fmt.Fprintln(w)
		}
	}

	return nil
}

// writeEventTimeline renders one execution's stage events in chronological
// order (the order ListExecutionEvents already returns them in): a UTC
// timestamp, the stage name, the duration since the previous stage (blank
// for the first event), and any detail text.
func writeEventTimeline(w io.Writer, events []*memory.Event) {
	if len(events) == 0 {
		_, _ = fmt.Fprintln(w, "  (no stage events recorded)")
		return
	}

	var prev time.Time
	for i, e := range events {
		ts := e.OccurredAt.UTC().Format("2006-01-02 15:04:05 UTC")

		duration := ""
		if i > 0 {
			duration = "+" + e.OccurredAt.Sub(prev).Round(time.Second).String()
		}

		line := fmt.Sprintf("  %s  %-18s %-10s", ts, e.Stage, duration)
		if e.Detail != "" {
			line += "  " + e.Detail
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))

		prev = e.OccurredAt
	}
}
