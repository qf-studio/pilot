package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
)

// IssueInfo holds adapter-agnostic issue metadata passed to handleIssueGeneric.
type IssueInfo struct {
	TaskID      string // e.g., "GH-123", "APP-456", "PLANE-abcd1234"
	Title       string
	Description string
	URL         string // issue URL for monitor registration
	Adapter     string // "github", "linear", "jira", "asana", "plane"
	LogEmoji    string // "📥", "📊", "📦" per adapter
}

// HandlerResult holds adapter-agnostic execution outcome returned by handleIssueGeneric.
type HandlerResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
	Duration   time.Duration
	// Result carries the raw execution result for adapters that need rich metrics
	// (e.g., GitHub uses it for the rich PR comment with token/cost/file stats).
	Result *executor.ExecutionResult
}

// HandlerDeps groups the shared infrastructure parameters every handler requires.
type HandlerDeps struct {
	Cfg          *config.Config
	Dispatcher   *executor.Dispatcher
	Runner       *executor.Runner
	Monitor      *executor.Monitor
	Program      *tea.Program
	AlertsEngine *alerts.Engine
	Enforcer     *budget.Enforcer
	ProjectPath  string
}

// handleIssueGeneric executes the common ~120-line flow shared by all adapter handlers:
//  1. Register with monitor
//  2. Log to dashboard
//  3. Emit task started alert
//  4. Budget check (with budget exceeded alert + early return)
//  5. Print to stdout
//  6. Dispatch via dispatcher OR direct execute via runner
//  7. Update monitor (fail/complete)
//  8. Emit task completed/failed alert
//  9. Add to dashboard history
//  10. Build and return HandlerResult
func handleIssueGeneric(ctx context.Context, deps HandlerDeps, info IssueInfo, task *executor.Task) (*HandlerResult, error) {
	taskID := info.TaskID
	title := info.Title
	projectPath := deps.ProjectPath

	// 1. Register with monitor
	if deps.Monitor != nil {
		deps.Monitor.Register(taskID, title, info.URL)
	}

	// 2. Log to dashboard
	if deps.Program != nil {
		deps.Program.Send(dashboard.AddLog(fmt.Sprintf("%s %s: %s", info.LogEmoji, taskID, title))())
	}

	// 3. Emit task started alert
	if deps.AlertsEngine != nil {
		deps.AlertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: title,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// 4. Budget check — block task if daily/monthly limits exceeded
	if deps.Enforcer != nil {
		checkResult, budgetErr := deps.Enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
				slog.String("action", string(checkResult.Action)),
			)
			if deps.AlertsEngine != nil {
				deps.AlertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: title,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &HandlerResult{
				Success:    false,
				BranchName: task.Branch,
				Error:      budgetExceededErr,
			}, budgetExceededErr
		}
	}

	// 5. Print to stdout
	fmt.Printf("\n%s %s: %s\n", info.LogEmoji, taskID, title)

	// 6. Dispatch via dispatcher OR direct execute via runner
	var result *executor.ExecutionResult
	var execErr error

	if deps.Dispatcher != nil {
		execID, qErr := deps.Dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			if deps.Monitor != nil {
				deps.Monitor.Queue(taskID)
			}
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := deps.Dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = deps.Runner.Execute(ctx, task)
	}

	// 7. Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if deps.Monitor != nil {
		if execErr != nil {
			deps.Monitor.Fail(taskID, execErr.Error())
		} else {
			deps.Monitor.Complete(taskID, prURL)
		}
	}

	// 8. Emit task completed/failed alert
	if deps.AlertsEngine != nil {
		if execErr != nil {
			deps.AlertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: title,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			deps.AlertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: title,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			deps.AlertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: title,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// 9. Add completed task to dashboard history
	if deps.Program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		}
		if result != nil {
			duration = result.Duration.String()
		}
		deps.Program.Send(dashboard.AddCompletedTask(taskID, title, status, duration, "", false)())
	}

	// 10. Build and return HandlerResult
	hr := &HandlerResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: task.Branch,
		Error:      execErr,
		Result:     result,
	}
	if result != nil {
		if result.PRUrl != "" {
			hr.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				hr.PRNumber = prNum
			}
		}
		hr.HeadSHA = result.CommitSHA
		hr.Duration = result.Duration
	}

	return hr, execErr
}
