package quality

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ExecutorConfig configures quality gate execution in the pipeline
type ExecutorConfig struct {
	Config      *Config
	ProjectPath string
	TaskID      string
}

// Executor manages quality gate enforcement in task execution pipeline
type Executor struct {
	runner *Runner
	config *Config
	taskID string
	log    *slog.Logger
}

// NewExecutor creates a quality gate executor for a task
func NewExecutor(cfg *ExecutorConfig) *Executor {
	return &Executor{
		runner: NewRunner(cfg.Config, cfg.ProjectPath),
		config: cfg.Config,
		taskID: cfg.TaskID,
		log:    logging.WithComponent("quality"),
	}
}

// CheckResult represents the quality gate check outcome
type ExecutionOutcome struct {
	Passed        bool
	Results       *CheckResults
	ShouldRetry   bool
	RetryFeedback string // Error feedback to send to Claude for retry
	Attempt       int
}

// Check runs all quality gates and returns the outcome
func (e *Executor) Check(ctx context.Context) (*ExecutionOutcome, error) {
	return e.CheckWithAttempt(ctx, 0)
}

// CheckWithAttempt runs quality gates tracking retry attempts
func (e *Executor) CheckWithAttempt(ctx context.Context, attempt int) (*ExecutionOutcome, error) {
	if !e.config.Enabled {
		return &ExecutionOutcome{
			Passed:  true,
			Attempt: attempt,
		}, nil
	}

	e.log.Info("Running quality gates",
		slog.String("task_id", e.taskID),
		slog.Int("attempt", attempt+1),
	)

	results, err := e.runner.RunAll(ctx, e.taskID)
	if err != nil {
		return nil, fmt.Errorf("quality gate check failed: %w", err)
	}

	outcome := &ExecutionOutcome{
		Passed:  results.AllPassed,
		Results: results,
		Attempt: attempt,
	}

	if !results.AllPassed {
		outcome.ShouldRetry = ShouldRetry(e.config, results, attempt)
		if outcome.ShouldRetry {
			outcome.RetryFeedback = FormatErrorFeedback(results)
		}

		e.log.Warn("Quality gates failed",
			slog.String("task_id", e.taskID),
			slog.Bool("will_retry", outcome.ShouldRetry),
			slog.Int("attempt", attempt+1),
		)
	} else {
		e.log.Info("Quality gates passed",
			slog.String("task_id", e.taskID),
			slog.Duration("total_time", results.TotalTime),
		)
	}

	return outcome, nil
}

// OnProgress sets progress callback for gate execution
func (e *Executor) OnProgress(callback ProgressCallback) {
	e.runner.OnProgress(callback)
}

// GateReport generates a summary report of gate results
type GateReport struct {
	TaskID    string           `json:"task_id"`
	Passed    bool             `json:"passed"`
	Summary   string           `json:"summary"`
	Gates     []GateReportItem `json:"gates"`
	TotalTime time.Duration    `json:"total_time"`
	Attempt   int              `json:"attempt"`
}

// GateReportItem represents a single gate in the report
type GateReportItem struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
	Coverage float64       `json:"coverage,omitempty"`
}

// GenerateReport creates a human-readable report from results
func GenerateReport(taskID string, results *CheckResults, attempt int) *GateReport {
	report := &GateReport{
		TaskID:    taskID,
		Passed:    results.AllPassed,
		TotalTime: results.TotalTime,
		Attempt:   attempt,
		Gates:     make([]GateReportItem, len(results.Results)),
	}

	passed := 0
	failed := 0
	skipped := 0

	for i, r := range results.Results {
		report.Gates[i] = GateReportItem{
			Name:     r.GateName,
			Status:   string(r.Status),
			Duration: r.Duration,
			Error:    r.Error,
			Coverage: r.Coverage,
		}

		switch r.Status {
		case StatusPassed:
			passed++
		case StatusFailed:
			failed++
		case StatusSkipped:
			skipped++
		}
	}

	if results.AllPassed {
		report.Summary = fmt.Sprintf("All %d quality gates passed", passed)
	} else {
		report.Summary = fmt.Sprintf("%d passed, %d failed, %d skipped", passed, failed, skipped)
	}

	return report
}

// FormatReportForNotification formats the report for Slack/Telegram notification
func FormatReportForNotification(report *GateReport) string {
	var sb strings.Builder

	if report.Passed {
		sb.WriteString("✅ Quality Gates Passed\n\n")
	} else {
		sb.WriteString("❌ Quality Gates Failed\n\n")
	}

	sb.WriteString(fmt.Sprintf("Task: %s\n", report.TaskID))
	sb.WriteString(fmt.Sprintf("Summary: %s\n", report.Summary))
	sb.WriteString(fmt.Sprintf("Duration: %s\n\n", report.TotalTime.Round(time.Second)))

	sb.WriteString("Gates:\n")
	for _, gate := range report.Gates {
		var icon string
		switch GateStatus(gate.Status) {
		case StatusPassed:
			icon = "✅"
		case StatusFailed:
			icon = "❌"
		case StatusSkipped:
			icon = "⏭️"
		default:
			icon = "⏳"
		}

		sb.WriteString(fmt.Sprintf("  %s %s (%s)", icon, gate.Name, gate.Duration.Round(time.Millisecond)))
		if gate.Error != "" {
			sb.WriteString(fmt.Sprintf(" - %s", gate.Error))
		}
		if gate.Coverage > 0 {
			sb.WriteString(fmt.Sprintf(" [%.1f%%]", gate.Coverage))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
