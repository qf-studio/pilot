package executor

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qf-studio/pilot/internal/memory"
)

// Status is the typed vocabulary for the executions-table status column
// (GH-4243). Values are unchanged from the free-text strings every call site
// already wrote — this only makes the vocabulary compile-time checkable, it
// does not touch persisted data, DB schema, or TerminalStatus's classifier
// precedence. Constants are prefixed Exec (rather than the bare "Status..."
// the issue sketched) because monitor.go's dashboard-facing TaskStatus
// already owns StatusQueued/StatusRunning/StatusCompleted/StatusFailed/
// StatusStalled for the in-memory task-state enum — a distinct concept from
// the persisted executions-row status this type models.
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
// executions-table rows (GH-4243). Before this type, every production path
// hand-rolled a memory.Execution{} literal (dispatcher.go's two queue sites,
// epic.go's sub-issue site) or called MarkExecutionCompleted/
// UpdateExecutionStatus directly and scattered the ExecutionID-threading
// step. A path that forgot a step produced the FK-787 defect class
// (execution_events insert against a task_id with no matching executions
// row), invisible metrics, or a false dashboard status. ExecutionLifecycle
// is a thin wrapper over *memory.Store — no new persistence, just one place
// that can't skip a step.
type ExecutionLifecycle struct {
	store *memory.Store
}

// NewExecutionLifecycle wraps store. A nil store makes every method a no-op
// (Begin still stamps task.ExecutionID with a generated UUID so downstream
// LogExecutionID() callers behave the same as with a real store whose write
// failed), mirroring the "logStore == nil" guards already scattered across
// dispatcher/epic/CLI call sites — callers don't need their own nil check.
func NewExecutionLifecycle(store *memory.Store) *ExecutionLifecycle {
	return &ExecutionLifecycle{store: store}
}

// Begin creates the executions row for task at the given initial status and
// unconditionally stamps the generated ID into task.ExecutionID — including
// when the save itself fails. This generalizes the epic sub-issue path's
// existing behavior (mem-026: "the UUID is threaded through below
// regardless, so downstream event recording always has a real UUID rather
// than falling back to the task ID") to every caller, so runner-side
// execution_events/log writes (keyed on Task.LogExecutionID()) reference a
// stable identifier even on a ledger-write failure. Callers that must abort
// when the row isn't durably persisted (e.g. a queued task with no execution
// row would be lost) inspect the returned error.
func (l *ExecutionLifecycle) Begin(task *Task, initial Status) (string, error) {
	execID := uuid.New().String()
	task.ExecutionID = execID

	if l.store == nil {
		return execID, nil
	}

	exec := &memory.Execution{
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
		IsCanary:          task.IsCanary,
	}
	if err := l.store.SaveExecution(exec); err != nil {
		return execID, fmt.Errorf("failed to save execution: %w", err)
	}
	return execID, nil
}

// Transition moves execID to a non-terminal status (e.g. queued -> running).
// Terminal moves go through Finish instead, which also persists metrics.
func (l *ExecutionLifecycle) Transition(execID string, s Status) error {
	if l.store == nil || execID == "" {
		return nil
	}
	return l.store.UpdateExecutionStatus(execID, string(s))
}

// FinishOutcome is what Finish computed and persisted, so callers can drive
// their own side effects (execution_events stage mapping, progress
// callbacks) off the same classification instead of re-deriving it.
type FinishOutcome struct {
	Status Status
	Error  string
}

// Finish terminates execID: classifies result via TerminalStatus, persists
// the terminal status (MarkExecutionCompleted on success so pr_url/
// commit_sha/duration land atomically, UpdateExecutionStatus otherwise), and
// saves execution metrics whenever result is non-nil — generalizing
// finalizeSubIssueExecution (the TASK-394 sub-issue chokepoint) to every
// caller. execErr, when non-nil, short-circuits classification straight to
// StatusFailed, mirroring every existing call site's "err != nil" branch
// running before TerminalStatus is even consulted; metrics are still saved
// in that case if result is non-nil (the execution ran and produced partial
// results before erroring), matching the dispatcher's existing behavior.
//
// override, when given, replaces the classified status outright — for a
// caller that has evidence the classifier can't see. epic.go's work-loss
// guard is the motivating case: a sub-issue can report Success with real
// commits but no PR (its work is stranded), which TerminalStatus would call
// "completed" but the epic must record as "failed" so the issue stays open
// for recovery. errMsg is still sourced from execErr/result as usual; only
// the status itself is overridden.
func (l *ExecutionLifecycle) Finish(execID string, result *ExecutionResult, execErr error, duration time.Duration, override ...Status) (FinishOutcome, error) {
	if l.store == nil || execID == "" {
		return FinishOutcome{}, nil
	}

	var outcome FinishOutcome
	var statusErr error

	if execErr != nil {
		outcome = FinishOutcome{Status: ExecStatusFailed, Error: execErr.Error()}
	} else {
		outcome = FinishOutcome{Status: Status(TerminalStatus(result))}
		if result != nil {
			outcome.Error = result.Error
		}
	}
	if len(override) > 0 {
		outcome.Status = override[0]
	}

	if outcome.Status == ExecStatusCompleted {
		var prURL, commitSHA string
		if result != nil {
			prURL, commitSHA = result.PRUrl, result.CommitSHA
		}
		statusErr = l.store.MarkExecutionCompleted(execID, prURL, commitSHA, duration.Milliseconds())
	} else {
		statusErr = l.store.UpdateExecutionStatus(execID, string(outcome.Status), outcome.Error)
	}

	if result != nil {
		metricsErr := l.store.SaveExecutionMetrics(&memory.ExecutionMetrics{
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
		if statusErr == nil {
			statusErr = metricsErr
		}
	}

	return outcome, statusErr
}
