package executor

import (
	"time"

	"github.com/google/uuid"
	"github.com/qf-studio/pilot/internal/memory"
)

// Status is the typed execution-row status vocabulary. Same string values as
// the free-text statuses every call site previously hand-wrote (TestTerminalStatus
// and dashboard/adapter read paths are unaffected) — typing it means a call site
// can no longer misspell a status, only fail to compile.
//
// Decision: constants are prefixed ExecStatus, not Status — the issue's
// suggested names (StatusQueued, StatusRunning, ...) collide with monitor.go's
// pre-existing TaskStatus vocabulary, a different concept (the in-memory task
// monitor's state) that already claims those identifiers in this package.
// GH-4243.
type Status string

const (
	ExecStatusQueued      Status = "queued"
	ExecStatusRunning     Status = "running"
	ExecStatusCompleted   Status = "completed"
	ExecStatusFailed      Status = "failed"
	ExecStatusNoOp        Status = "no_op"
	ExecStatusStalled     Status = "stalled"
	ExecStatusRateLimited Status = "rate_limited"
	ExecStatusInfra       Status = "infra"
	ExecStatusSkipped     Status = "skipped"
	ExecStatusDeclined    Status = "declined"
	ExecStatusDecomposed  Status = "decomposed"
)

// ExecutionLifecycle is the single chokepoint for creating and transitioning
// memory.Execution rows. Before GH-4243, every production path (dispatcher
// queueing, epic sub-issue dispatch, the `pilot task` CLI) hand-rolled
// &memory.Execution{} + store.SaveExecution and had to remember, independently,
// to thread the generated ID into Task.ExecutionID — forgetting that step is
// the FK-787 defect class (TASK-394's epic path, GH-4205's CLI path): without
// it, Task.LogExecutionID() falls back to the human-readable task ID, which has
// no matching executions.id row, and every execution_events insert for that
// run trips a foreign key violation. Begin/Transition/Finish move that
// invariant into the type system instead of another guard at another call site.
type ExecutionLifecycle struct {
	store *memory.Store
}

// NewExecutionLifecycle wraps store for lifecycle calls. Deliberately thin
// (one field) — safe to construct per call site rather than threading a
// shared field through every constructor that touches executions.
func NewExecutionLifecycle(store *memory.Store) *ExecutionLifecycle {
	return &ExecutionLifecycle{store: store}
}

// Begin creates task's execution row at the given initial status and threads
// the generated ID into task.ExecutionID, so Task.LogExecutionID() (and every
// runner-side execution_events/log write keyed off it) resolves to a real
// executions.id instead of falling back to the human-readable task ID.
//
// task.ExecutionID is stamped before the save is attempted, and execID is
// returned even on error (mem-026): a ledger write must never block the
// caller, and downstream event recording should still get a real UUID to
// join against rather than falling back to the human-readable task ID.
// Callers that must abort on error (e.g. the dispatcher's queueing path)
// still see it via the returned error and can discard the ID themselves.
func (l *ExecutionLifecycle) Begin(task *Task, initial Status) (string, error) {
	execID := uuid.New().String()
	task.ExecutionID = execID
	err := l.store.SaveExecution(&memory.Execution{
		ID:                execID,
		TaskID:            task.ID,
		ProjectPath:       task.ProjectPath,
		Status:            string(initial),
		TaskTitle:         task.Title,
		TaskDescription:   task.Description,
		TaskBranch:        task.Branch,
		TaskBaseBranch:    task.BaseBranch,
		TaskCreatePR:      task.CreatePR,
		TaskVerbose:       task.Verbose,
		TaskSourceAdapter: task.SourceAdapter,
		TaskSourceIssueID: task.SourceIssueID,
		TaskLabels:        task.Labels,
	})
	return execID, err
}

// Transition moves execID to a non-terminal status (e.g. queued -> running).
// Terminal moves belong to Finish, which also persists result/duration/metrics.
func (l *ExecutionLifecycle) Transition(execID string, s Status, errMsg ...string) error {
	return l.store.UpdateExecutionStatus(execID, string(s), errMsg...)
}

// Classify reduces a run's outcome to the terminal Status and error message
// Finish should persist: an invocation-level error (the backend itself failed
// to run, no result to classify) collapses to StatusFailed with execErr's
// text; otherwise it wraps TerminalStatus (runner.go) against result.
func Classify(execErr error, result *ExecutionResult) (Status, string) {
	if execErr != nil {
		return ExecStatusFailed, execErr.Error()
	}
	if result == nil {
		return ExecStatusFailed, "no execution result"
	}
	return Status(TerminalStatus(result)), result.Error
}

// Finish marks execID terminal at status with errMsg (empty on success) and
// persists duration plus, when result is non-nil, PR/commit and
// token/cost/RSS metrics in one place — generalizing the TASK-394
// finalizeSubIssueExecution pattern (and the dispatcher/CLI paths' equivalent
// hand-rolled post-execute logic) to every execution path. Pass Classify's
// output for status/errMsg in the common case; callers that must override the
// classification (e.g. the epic work-loss guard forcing "failed" on an
// otherwise-successful result whose PR never landed) pass an explicit status.
func (l *ExecutionLifecycle) Finish(execID string, status Status, errMsg string, result *ExecutionResult, duration time.Duration) error {
	if status == ExecStatusCompleted && result != nil {
		if err := l.store.MarkExecutionCompleted(execID, result.PRUrl, result.CommitSHA, duration.Milliseconds()); err != nil {
			return err
		}
	} else if err := l.store.UpdateExecutionStatus(execID, string(status), errMsg); err != nil {
		return err
	}

	if result == nil {
		return nil
	}
	return l.store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID:      execID,
		TokensInput:      result.TokensInput,
		TokensOutput:     result.TokensOutput,
		TokensTotal:      result.TokensTotal,
		TokensCacheRead:  result.CacheReadInputTokens,
		TokensCacheWrite: result.CacheCreationInputTokens,
		EstimatedCostUSD: result.EstimatedCostUSD,
		FilesChanged:     result.FilesChanged,
		LinesAdded:       result.LinesAdded,
		LinesRemoved:     result.LinesRemoved,
		ModelName:        result.ModelName,
		PeakRSSMB:        result.PeakRSSMB,
		FinalRSSMB:       result.FinalRSSMB,
	})
}
