package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// IssueInfo holds adapter-agnostic issue metadata passed to handleIssueGeneric.
type IssueInfo struct {
	TaskID      string // e.g., "GH-123", "APP-456", "PLANE-abcd1234"
	Title       string
	Description string
	URL         string // issue URL for monitor registration
	Adapter     string // "github", "linear", "jira", "asana", "plane"
	LogMark     string // dashboard log mark; "▸" = task intake (design-system glyph)
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

	// Metrics records the GH-4376 repick-storm skip counter (and any other
	// poller skip/dispatch counters this chokepoint later grows) onto
	// pilot_poller_skipped_total. Nil is tolerated — the admission gate and
	// backoff still apply, just without the Prometheus counter bump.
	Metrics *autopilot.Metrics
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

	// GH-4240: stamp the canary marker from the registered project config
	// before dispatch, so it survives the queue round-trip into the
	// executions row (ExecutionLifecycle.Begin) and the runner's live
	// metrics guard. deps.Cfg.GetProject returns nil for an unregistered
	// path (e.g. ad-hoc CLI runs), which correctly resolves to non-canary.
	if deps.Cfg != nil {
		if proj := deps.Cfg.GetProject(projectPath); proj != nil {
			task.IsCanary = proj.Canary
		}
	}

	// GH-4008: pre-check whether this task is already queued or running,
	// before any monitor/alert side effects fire. Prevents the noisy
	// "Dispatching..." + ERROR "already queued or running" pair that
	// repeated every poll cycle while a task legitimately waited behind
	// other work — the dispatcher's own duplicate check (QueueTask) remains
	// the authoritative guard; this is a cheap pre-check to skip the attempt
	// entirely in the common case.
	if deps.Dispatcher != nil && deps.Dispatcher.IsActive(taskID, projectPath) {
		logging.WithComponent("dispatch").Debug("Task already queued or running, skipping dispatch",
			slog.String("task_id", taskID))
		return &HandlerResult{Success: false, BranchName: task.Branch, Error: executor.ErrDispatchGated}, nil
	}

	// GH-4376: per-issue backoff — a task that was recently dropped (claim
	// lost, or already terminal per the HasTerminalCompletion re-check right
	// below) gets a growing cooldown instead of repeating the full
	// monitor/alert/dashboard side-effect sequence on every ~30s poll tick.
	//
	// GH-4394: wire the durable backing store on every call (idempotent — the
	// same *Dispatcher for the process lifetime in production, a fresh one
	// per test) so the cooldown survives a daemon restart or a shadow-DB
	// split-brain instead of silently resetting to zero mid-storm.
	if deps.Dispatcher != nil {
		repickBackoff.setPersister(deps.Dispatcher)
	}
	backoffKey := repickBackoffKey(projectPath, taskID)
	if deps.Dispatcher != nil && !repickBackoff.allow(backoffKey) {
		logging.WithComponent("dispatch").Debug("task in repick backoff window, skipping dispatch",
			slog.String("task_id", taskID))
		return &HandlerResult{Success: false, BranchName: task.Branch, Error: executor.ErrDispatchGated}, nil
	}

	// GH-4376/GH-4350: independent terminal-completion re-check at the shared
	// dispatch chokepoint — defense in depth against the poller's own
	// label-removed retry heuristic (external studio-sdk dependency)
	// re-admitting an issue whose task already has terminal ledger evidence.
	// The poller's own ExecutionChecker gate is supposed to catch this first;
	// this is the backstop for whatever lets it slip through (GH-91 evidence:
	// COMPLETED terminal execution, open issue, no status labels, re-dispatched
	// every ~30s poll cycle regardless).
	if deps.Dispatcher != nil {
		if done, hcErr := deps.Dispatcher.HasTerminalCompletion(taskID, projectPath); hcErr == nil && done {
			consecutive := repickBackoff.recordDrop(backoffKey)
			logFields := []any{slog.String("task_id", taskID), slog.Int("consecutive_drops", consecutive)}
			if consecutive >= repickBackoffWarnThreshold {
				logging.WithComponent("dispatch").Warn("repick storm: completed-but-open issue re-admitted repeatedly", logFields...)
			} else {
				logging.WithComponent("dispatch").Debug("skipping dispatch — task already has terminal completion", logFields...)
			}
			if deps.Metrics != nil {
				deps.Metrics.RecordPollerSkipped(repickMetricsRepo(task), skipreason.ReasonRepickStormBackoff)
			}
			fireLoopBreakerAlert(deps, taskID, title, projectPath, consecutive)
			return &HandlerResult{Success: false, BranchName: task.Branch, Error: executor.ErrDispatchGated}, nil
		}
	}

	// 1. Register with monitor
	if deps.Monitor != nil {
		deps.Monitor.Register(taskID, title, info.URL)
		// GH-2167: Attach project path so dashboard git graph can follow focused task
		deps.Monitor.SetProjectInfo(taskID, projectPath, filepath.Base(projectPath))
	}

	// 2. Log to dashboard
	if deps.Program != nil {
		deps.Program.Send(dashboard.AddLog(fmt.Sprintf("%s %s: %s", info.LogMark, taskID, title))())
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

	// 5. Print to stdout (skip in dashboard mode to avoid corrupting the TUI alt-screen)
	if deps.Program == nil {
		fmt.Printf("\n%s %s: %s\n", info.LogMark, taskID, title)
	}

	// 6. Dispatch via dispatcher OR direct execute via runner
	var result *executor.ExecutionResult
	var execErr error
	// gatedDrop marks the execID=="" branch below (GH-4372 duplicate/terminal
	// drop) so the final HandlerResult can carry executor.ErrDispatchGated
	// (GH-4469) without disturbing the existing execErr-driven monitor/alert
	// side effects in steps 7-9, which intentionally treat this path as
	// "nothing to wait for" rather than a failure.
	var gatedDrop bool

	if deps.Dispatcher != nil {
		execID, qErr := deps.Dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			if errors.Is(qErr, executor.ErrTaskAlreadyActive) {
				// GH-4008: race between the pre-check above and QueueTask's own
				// guard — expected dedup, not a failure. Downgrade to Debug so
				// it never surfaces as an ERROR to callers (e.g. the SDK poller
				// logs "Failed to process issue" on any non-nil handler error).
				logging.WithComponent("dispatch").Debug("Task already queued or running (race), skipping dispatch",
					slog.String("task_id", taskID), slog.Any("error", qErr))
			} else {
				execErr = fmt.Errorf("failed to queue task: %w", qErr)
			}
		} else if execID == "" {
			// GH-4372: QueueTask returns a nil error AND an empty execID when
			// it drops a duplicate/terminal pickup silently (ErrClaimLost to a
			// live owner, or a dead owner whose task is already
			// HasTerminalCompletion-done and must not be re-armed, GH-4350) —
			// no executions row exists here to wait on. Previously this fell
			// into the branch below and called WaitForExecution(ctx, "", ...),
			// which hit sql.ErrNoRows on its very first poll (an empty execID
			// never matches a row) and surfaced as "failed to get execution:
			// sql: no rows in result set" — an ERROR the SDK poller logged on
			// every tick for a task that was never actually a failure.
			consecutive := repickBackoff.recordDrop(backoffKey)
			logFields := []any{slog.String("task_id", taskID), slog.Int("consecutive_drops", consecutive)}
			if consecutive >= repickBackoffWarnThreshold {
				logging.WithComponent("dispatch").Warn("repick storm: claim-lost/terminal drop recurring", logFields...)
			} else {
				logging.WithComponent("dispatch").Debug("dispatch dropped duplicate/terminal pickup, nothing to wait for", logFields...)
			}
			if deps.Metrics != nil {
				deps.Metrics.RecordPollerSkipped(repickMetricsRepo(task), skipreason.ReasonRepickStormBackoff)
			}
			fireLoopBreakerAlert(deps, taskID, title, projectPath, consecutive)
			gatedDrop = true
		} else {
			// GH-4394 subtask 2: a repick (Dispatcher.beginWithGenerationRetry
			// claiming execution_claims generation > 0 because the prior claim
			// was terminal but the task wasn't done) already extended this
			// key's backoff directly against the store from inside QueueTask.
			// Clearing it here unconditionally — as if every successful
			// QueueTask return were a brand-new generation-0 dispatch — is
			// exactly what let GH-85 re-pick 5x in ~15 min with no backoff
			// growth: this chokepoint couldn't tell a repick apart from a
			// fresh pickup. Only clear for a genuine first attempt; on a
			// generation lookup error, err toward NOT clearing (leaves any
			// backoff intact rather than risking silently undoing growth).
			if gen, genErr := deps.Dispatcher.ExecutionGeneration(taskID, projectPath); genErr == nil && gen == 0 {
				repickBackoff.recordSuccess(backoffKey)
			}
			if deps.Monitor != nil {
				deps.Monitor.Queue(taskID)
			}
			if deps.Program == nil {
				fmt.Printf("   Queued as execution %s\n", execID[:8])
			}
			exec, waitErr := deps.Dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", execFailureMsg(exec.Error))
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
	hrErr := execErr
	if hrErr == nil && gatedDrop {
		// GH-4469: distinguish "dropped a duplicate/terminal pickup" from a
		// genuine execution failure for anything that inspects HandlerResult.
		hrErr = executor.ErrDispatchGated
	}
	hr := &HandlerResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: task.Branch,
		Error:      hrErr,
		Result:     result,
	}
	if result != nil {
		if result.PRUrl != "" {
			hr.PRURL = result.PRUrl
			// GH-2293: Use adapter-specific PR/MR number extraction.
			// Each forge has a different URL format for pull/merge requests.
			switch info.Adapter {
			case "gitlab":
				if mrNum, err := gitlab.ExtractMRNumber(result.PRUrl); err == nil {
					hr.PRNumber = mrNum
				}
			case "azuredevops":
				if prNum, err := azuredevops.ExtractPRNumber(result.PRUrl); err == nil {
					hr.PRNumber = prNum
				}
			default:
				if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
					hr.PRNumber = prNum
				}
			}
		}
		hr.HeadSHA = result.CommitSHA
		hr.Duration = result.Duration
	}

	return hr, execErr
}

// fireLoopBreakerAlert emits AlertTypeDispatchLoopBreaker exactly once per
// storm — the tick where consecutive first reaches repickLoopBreakerThreshold
// (GH-4469). consecutive strictly increases while a task keeps dropping and
// is reset by repickBackoff.recordSuccess on the first genuine dispatch, so
// comparing for equality (rather than >=) fires the alert once without
// needing separate dedup state; a nil AlertsEngine (e.g. in tests without one
// wired) is a no-op.
func fireLoopBreakerAlert(deps HandlerDeps, taskID, title, projectPath string, consecutive int) {
	if consecutive != repickLoopBreakerThreshold {
		return
	}
	logging.WithComponent("dispatch").Warn(
		"dispatch loop breaker: task rejected 10+ consecutive times, stopping until operator action or backoff expiry",
		slog.String("task_id", taskID), slog.Int("consecutive_drops", consecutive))
	if deps.AlertsEngine == nil {
		return
	}
	deps.AlertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeDispatchLoopBreaker,
		TaskID:    taskID,
		TaskTitle: title,
		Project:   projectPath,
		Metadata: map[string]string{
			"consecutive_drops": fmt.Sprintf("%d", consecutive),
		},
		Timestamp: time.Now(),
	})
}

// repickMetricsRepo returns the repo label to record repick-storm skips
// under: task.SourceRepo when the adapter set it (github/gitlab/azuredevops),
// falling back to the project path for adapters that don't carry a repo
// identity (linear/jira/asana/plane).
func repickMetricsRepo(task *executor.Task) string {
	if task.SourceRepo != "" {
		return task.SourceRepo
	}
	return task.ProjectPath
}

// execFailureMsg returns the error detail for a failed dispatcher execution.
// When exec.Error is empty (the executor failed silently), a descriptive default
// is substituted so callers never build a bare "execution failed: " comment.
func execFailureMsg(execError string) string {
	if execError == "" {
		return "executor reported failure without providing an error message"
	}
	return execError
}
