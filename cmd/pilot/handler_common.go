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
	"github.com/qf-studio/pilot/internal/memory"
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
	// TerminalByDesign marks an execution that finished in a terminal-by-design
	// status (superseded or canceled, see executor.IsTerminalByDesignStatus) —
	// deliberately not carried out rather than genuinely failed (GH-4794).
	TerminalByDesign bool
}

// IsDispatchGated reports whether this result's Error is (or wraps)
// executor.ErrDispatchGated — a pre/post-dispatch admission-gate decline
// (already-active dedup, repick backoff, terminal-completion re-check,
// claim-lost drop), not a genuine execution failure.
//
// GH-4587: callers translating a HandlerResult into a vendored-SDK
// sdkcore.IssueResult must consult this before forwarding Success=false with
// no PR/MR — the SDK poller's "failed without PR/MR, unmarking for retry"
// branch has no other way to distinguish "genuinely failed" from "declined
// because another generation/channel already owns this task", and treating
// the latter as the former churns unmark+re-offer loops against a task
// that's actively being worked.
func (hr *HandlerResult) IsDispatchGated() bool {
	return hr != nil && errors.Is(hr.Error, executor.ErrDispatchGated)
}

// IsTerminalByDesign reports whether this execution finished in a
// terminal-by-design status (superseded or canceled) rather than a genuine
// completion or failure (GH-4794). Callers translating a HandlerResult into
// a vendored-SDK sdkcore.IssueResult must consult this alongside
// IsDispatchGated before forwarding Success=false with no PR/MR — and must
// not emit a failure report/alert for it — since the work was deliberately
// not carried out (issue closed, or operator-canceled), not botched.
func (hr *HandlerResult) IsTerminalByDesign() bool {
	return hr != nil && hr.TerminalByDesign
}

// EffectiveSuccess reports whether this result should be translated as
// Success=true on the vendored-SDK sdkcore.IssueResult every adapter handler
// builds from a HandlerResult. A genuine hr.Success obviously counts, but so
// does IsDispatchGated() (GH-4587: an admission-gate decline — already
// active, repick backoff, terminal re-check, claim-lost drop — is not a
// failed execution) and IsTerminalByDesign() (GH-4794: a superseded/canceled
// execution is the success path for "this work is no longer wanted"). All
// seven adapter handlers (github, gitlab, azuredevops, linear, jira, asana,
// plane) must use this single formula rather than inlining the three-term
// OR — GH-4801 found four of the seven still building Success: hr.Success
// bare, and azuredevops missing the GH-4587 term entirely, after PR#4800
// only fixed github/gitlab/azuredevops's IsTerminalByDesign gap.
func (hr *HandlerResult) EffectiveSuccess() bool {
	return hr.Success || hr.IsDispatchGated() || hr.IsTerminalByDesign()
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
			// GH-4540/TASK-421: a completed-but-open issue being re-admitted
			// is not a failed re-pick — the task already succeeded — so this
			// must not grow consecutive_drops (the counter
			// beginWithGenerationRetry gates dispatcherRepickHardCap on).
			// recordClaimLostDrop still grows the shared backoff cooldown
			// window, so the storm is still throttled the way TASK-413
			// intends; it just never counts toward the hard cap.
			claimLostDrops := repickBackoff.recordClaimLostDrop(backoffKey)
			logFields := []any{
				slog.String("task_id", taskID),
				slog.String("drop_reason", "already has terminal completion"),
				slog.Int("claim_lost_drops", claimLostDrops),
			}
			if claimLostDrops >= repickBackoffWarnThreshold {
				logging.WithComponent("dispatch").Warn("repick storm: completed-but-open issue re-admitted repeatedly — not counted toward repick hard cap", logFields...)
			} else {
				logging.WithComponent("dispatch").Debug("skipping dispatch — task already has terminal completion — not counted toward repick hard cap", logFields...)
			}
			if deps.Metrics != nil {
				deps.Metrics.RecordPollerSkipped(repickMetricsRepo(task), skipreason.ReasonRepickStormBackoff)
			}
			fireLoopBreakerAlert(deps, taskID, title, projectPath, claimLostDrops)
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
	// terminalByDesign marks an execution that finished superseded/canceled
	// (GH-4794) — set below once WaitForExecution returns a terminal row —
	// so steps 8/10 can suppress the false failure report/alert without
	// touching the genuine-failure path.
	var terminalByDesign bool

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
			// GH-4540/TASK-421: this branch fires whenever QueueTask silently
			// dropped the pickup — a live owner already has the task
			// queued/running, the task is already terminally done, or a
			// retry Begin() lost a race — none of which are a genuine failed
			// re-pick. Must not grow consecutive_drops. recordClaimLostDrop
			// still grows the shared backoff cooldown window so a
			// repeatedly-re-offered task is still throttled, just via a
			// counter the hard cap never reads. Re-check IsActive purely to
			// give the log a best-effort, human-readable reason — it does
			// not change what gets counted.
			dropReason := "already terminal or a retry race was lost"
			if deps.Dispatcher.IsActive(taskID, projectPath) {
				dropReason = "already queued or running"
			}
			claimLostDrops := repickBackoff.recordClaimLostDrop(backoffKey)
			logFields := []any{
				slog.String("task_id", taskID),
				slog.String("drop_reason", dropReason),
				slog.Int("claim_lost_drops", claimLostDrops),
			}
			if claimLostDrops >= repickBackoffWarnThreshold {
				logging.WithComponent("dispatch").Warn("repick storm: claim-lost/terminal drop recurring — not counted toward repick hard cap", logFields...)
			} else {
				logging.WithComponent("dispatch").Debug("dispatch dropped duplicate/terminal pickup, nothing to wait for — not counted toward repick hard cap", logFields...)
			}
			if deps.Metrics != nil {
				deps.Metrics.RecordPollerSkipped(repickMetricsRepo(task), skipreason.ReasonRepickStormBackoff)
			}
			fireLoopBreakerAlert(deps, taskID, title, projectPath, claimLostDrops)
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
			} else {
				result, execErr, terminalByDesign = classifyWaitedExecution(task.ID, exec)
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
		if ev := classifyResultAlert(taskID, title, projectPath, execErr, result, terminalByDesign); ev != nil {
			deps.AlertsEngine.ProcessEvent(*ev)
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
		Success:          execErr == nil && result != nil && result.Success,
		BranchName:       task.Branch,
		Error:            hrErr,
		Result:           result,
		TerminalByDesign: terminalByDesign,
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

// classifyWaitedExecution turns a terminal execution row (as returned by
// Dispatcher.WaitForExecution) into the (result, err, terminalByDesign)
// triple handleIssueGeneric needs. GH-4794: a superseded or canceled
// execution is the success path for "this work is no longer wanted" — it
// must be reported as neither a completion nor a genuine failure, so callers
// can suppress the failure alert/report and the vendored-SDK "no PR,
// unmarking for retry" branch without touching the real-failure path
// (exec.Status == "failed" is untouched below).
func classifyWaitedExecution(taskID string, exec *memory.Execution) (result *executor.ExecutionResult, execErr error, terminalByDesign bool) {
	if exec.Status == "failed" {
		return nil, fmt.Errorf("execution failed: %s", execFailureMsg(exec.Error)), false
	}
	result = &executor.ExecutionResult{
		TaskID:    taskID,
		Success:   exec.Status == "completed",
		Output:    exec.Output,
		Error:     exec.Error,
		PRUrl:     exec.PRUrl,
		CommitSHA: exec.CommitSHA,
		Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
	}
	return result, nil, executor.IsTerminalByDesignStatus(exec.Status)
}

// classifyResultAlert decides which alerts.Event (if any) handleIssueGeneric's
// step 8 should emit for a finished dispatch/execute attempt, given the
// classification classifyWaitedExecution (or the direct-execute Runner path)
// produced. Returns nil when no alert should fire.
//
// GH-4794: a terminal-by-design execution (superseded or canceled) is the
// success path for "this work is no longer wanted" — it must not produce a
// TaskFailed alert (it isn't a failure) or a TaskCompleted one (no PR/commit
// exists), so the terminalByDesign case falls through to nil. A hard
// execErr (queue/wait failure) always pages regardless of terminalByDesign,
// since that's a pipeline failure, not a classified execution status.
func classifyResultAlert(taskID, title, projectPath string, execErr error, result *executor.ExecutionResult, terminalByDesign bool) *alerts.Event {
	now := time.Now()
	switch {
	case execErr != nil:
		return &alerts.Event{
			Type:      alerts.EventTypeTaskFailed,
			TaskID:    taskID,
			TaskTitle: title,
			Project:   projectPath,
			Error:     execErr.Error(),
			Timestamp: now,
		}
	case result != nil && result.Success:
		metadata := map[string]string{}
		if result.PRUrl != "" {
			metadata["pr_url"] = result.PRUrl
		}
		if result.Duration > 0 {
			metadata["duration"] = result.Duration.String()
		}
		return &alerts.Event{
			Type:      alerts.EventTypeTaskCompleted,
			TaskID:    taskID,
			TaskTitle: title,
			Project:   projectPath,
			Metadata:  metadata,
			Timestamp: now,
		}
	case result != nil && !terminalByDesign:
		// GH-4794: a superseded/canceled execution is the success path for
		// "this work is no longer wanted", not a failure — don't page an
		// operator for it.
		return &alerts.Event{
			Type:      alerts.EventTypeTaskFailed,
			TaskID:    taskID,
			TaskTitle: title,
			Project:   projectPath,
			Error:     result.Error,
			Timestamp: now,
		}
	default:
		return nil
	}
}
