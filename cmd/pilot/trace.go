package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	var projectFlag string

	cmd := &cobra.Command{
		Use:   "trace <task-id>",
		Short: "Render the stage-transition timeline for a task's executions",
		Long: `Render the stage-transition timeline recorded in execution_events for
every execution of a task, newest execution first. Retries are rendered as
separate blocks; each stage line shows a UTC timestamp and the duration since
the previous stage.

task_id is not unique across projects (every freshly onboarded repo starts
issue numbering at #1), so the trace is scoped to a single project: --project
if given, otherwise the current directory if it matches one of the task's
projects, otherwise (when the task_id only ever ran in one project) that
project. If the task_id collides across multiple projects and neither of
those resolves it, the candidate projects are listed instead of merged.

Examples:
  pilot trace GH-42                         # Show the stage timeline for task GH-42
  pilot trace TASK-379                      # Show the stage timeline for a Navigator task ID
  pilot trace GH-1 --project /path/to/repo  # Scope to a specific project`,
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

			return runTrace(cmd.OutOrStdout(), store, args[0], projectFlag)
		},
	}

	cmd.Flags().StringVarP(&projectFlag, "project", "p", "", "Project path to scope the trace to (default: current directory, auto-resolved if unambiguous)")

	return cmd
}

// runTrace writes the stage timeline for every execution of taskID within a
// single, resolved project to w, newest execution first. Returns an error
// naming taskID when no executions are recorded at all, or listing candidate
// projects when taskID collides across projects and cwd/--project can't
// disambiguate (GH-4378) — either way, the CLI exits non-zero with no
// partial output rather than silently merging unrelated repos.
func runTrace(w io.Writer, store *memory.Store, taskID, projectFlag string) error {
	projects, err := store.ListProjectsForTask(taskID)
	if err != nil {
		return fmt.Errorf("failed to look up task %s: %w", taskID, err)
	}
	if len(projects) == 0 {
		return fmt.Errorf("no executions found for task %s", taskID)
	}

	project := resolveTraceProject(projectFlag, projects)
	if project == "" {
		return ambiguousTraceProjectError(taskID, projects)
	}

	executions, err := store.ListExecutionsForTask(taskID, project)
	if err != nil {
		return fmt.Errorf("failed to look up task %s: %w", taskID, err)
	}
	if len(executions) == 0 {
		return fmt.Errorf("no executions found for task %s in project %s", taskID, project)
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

// resolveTraceProject picks the single project a trace should be scoped to,
// given an optional --project override and the distinct projects that have
// recorded executions for the task (newest-first, from
// store.ListProjectsForTask). Returns "" when the task_id collides across
// multiple projects and neither the override nor the current directory
// resolves it — the caller must treat that as ambiguous rather than merging.
func resolveTraceProject(projectFlag string, projects []memory.TaskProjectSummary) string {
	if projectFlag != "" {
		return projectFlag
	}
	// A task_id that only ever ran in one project is unambiguous regardless
	// of where `pilot trace` is invoked from.
	if len(projects) == 1 {
		return projects[0].ProjectPath
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for _, p := range projects {
		if p.ProjectPath == cwd {
			return cwd
		}
	}
	return ""
}

// ambiguousTraceProjectError lists the projects a colliding task_id ran in,
// newest execution first, so the caller can pick one via --project instead
// of trace silently merging unrelated repos' executions (GH-4378).
func ambiguousTraceProjectError(taskID string, projects []memory.TaskProjectSummary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "task %s has executions in %d projects — pass --project to disambiguate:\n", taskID, len(projects))
	for _, p := range projects {
		fmt.Fprintf(&b, "  %s\t(latest %s)\n", p.ProjectPath, p.LatestAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}
