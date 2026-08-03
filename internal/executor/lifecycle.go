package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// ErrClaimLost is returned by Begin when another caller already won the
// execution_claims race for (task.ID, task.ProjectPath, generation)
// (TASK-407/GH-4349). It is not an error state: it means a different
// dispatch channel (poller, epic sub-issue loop, CLI, ...) already owns this
// execution. Callers must abort BEFORE invoking the backend and must not
// treat this as a failure to log or retry — see each call site's own
// handling for how it accounts for the externally-owned execution.
var ErrClaimLost = errors.New("execution claim lost to another dispatch channel")

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
	// ExecStatusSuperseded marks an execution finalized without running (or
	// without opening a PR) because its GitHub issue was found closed by the
	// GH-4656 pickup-time / PR-creation preflight guards — the issue's scope
	// was already delivered by another run (sibling/parent) before this one
	// reached the gate. Distinct from ExecStatusSkipped (which covers other
	// pre-dispatch admission declines): superseded specifically means "another
	// run already shipped this."
	ExecStatusSuperseded Status = "superseded"
	// ExecStatusCanceled marks an execution an operator deliberately
	// terminated via `pilot task cancel` (GH-4678) — a real cancel verb,
	// distinct from ExecStatusStalled (a "dead owner, please retry" recovery
	// signal that the dispatcher deliberately re-picks) and from the
	// unrelated, confirmed-dead "cancelled" (double-L) value store.go's
	// terminalExecutionStatuses keeps only for historical defensiveness.
	// Spelled with a single L specifically to avoid ever colliding with that
	// dead value: "canceled" is the ONLY spelling any current code writes.
	// Terminal forever — the dispatcher's nextRetryGeneration never grants a
	// fresh generation for a row in this status, and it counts as done for
	// HasTerminalCompletion, so the poller never re-dispatches the task_id
	// (GH-4655 was cancel-by-abusing-"stalled", which does the opposite:
	// grants fresh generations exempt from the repick hard cap forever).
	ExecStatusCanceled Status = "canceled"
)

// ErrExecutionNotFound is returned by Cancel when taskID has no execution
// row at all in projectPath — nothing to cancel. Distinct from
// memory.ErrExecutionNotFound (RecordExecutionEvent's missing-parent-row
// signal); this one is scoped to Cancel's own lookup.
var ErrExecutionNotFound = errors.New("no execution found for task")

// ErrExecutionAlreadyTerminal is returned by Cancel when the task's latest
// execution row is already in a terminal status (including a prior cancel)
// — cancelling it again would be a no-op write racing the CAS guard for no
// benefit, so Cancel reports this instead of attempting the write.
var ErrExecutionAlreadyTerminal = errors.New("execution already in a terminal state")

// ErrExecutionRunning is returned by Cancel when the task's latest execution
// row is currently "running". GH-4678 v1 refuses rather than killing the
// backend process: no PID (or equivalent handle) is tracked anywhere in the
// executions table or in-memory Monitor state for a running row, so there is
// no safe handle here to signal/kill. The error carries the execution ID so
// the caller can act (e.g. locate and kill the process manually, or wait for
// it to finish) — "stop it cleanly" is left for future work once a PID/
// cancellation-context is threaded through the runner.
var ErrExecutionRunning = errors.New("execution is currently running")

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

// Begin claims (task.ID, task.ProjectPath, generation) and, on winning,
// creates the executions row at the given initial status — unconditionally
// stamping the generated ID into task.ExecutionID once the claim is won,
// including when the subsequent save itself fails. This generalizes the epic
// sub-issue path's existing behavior (mem-026: "the UUID is threaded through
// below regardless, so downstream event recording always has a real UUID
// rather than falling back to the task ID") to every caller, so runner-side
// execution_events/log writes (keyed on Task.LogExecutionID()) reference a
// stable identifier even on a ledger-write failure.
//
// generation defaults to 0 when omitted, so every pre-existing call site
// (dispatcher.go, cmd/pilot/commands.go) keeps compiling and claiming
// generation 0 — the initial attempt at a task. A caller deciding a
// legitimate retry (retry-after-terminal-failure, conflict
// close-and-reexecute, shouldRetryFailedIssue) passes generation+1 so the
// retry claims a fresh row instead of deadlocking on its own prior claim.
//
// On losing the claim, Begin returns ("", ErrClaimLost) and does NOT stamp
// task.ExecutionID — unlike the save-failure case, the caller owns no
// execution row at all here (another channel does), so stamping a fresh UUID
// would produce a dangling ID with no matching executions row, reintroducing
// the FK-787 class this chokepoint exists to prevent. Callers must check
// errors.Is(err, ErrClaimLost) and treat it as "already claimed — abort
// before backend invocation, log, no error state" (TASK-407/GH-4349).
func (l *ExecutionLifecycle) Begin(task *Task, initial Status, generation ...int) (string, error) {
	gen := 0
	if len(generation) > 0 {
		gen = generation[0]
	}

	execID := uuid.New().String()

	if l.store == nil {
		task.ExecutionID = execID
		return execID, nil
	}

	claimed, err := l.store.ClaimExecution(task.ID, task.ProjectPath, gen, execID)
	if err != nil {
		return "", fmt.Errorf("failed to claim execution: %w", err)
	}
	if !claimed {
		return "", ErrClaimLost
	}

	task.ExecutionID = execID

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
//
// GH-4423: routed through the CAS-guarded store write so a Transition racing
// in after Finish already recorded a terminal status (e.g. a duplicate
// dispatch resuming a worker for a row that already completed) is rejected
// instead of silently resurrecting the row to a non-terminal status. A
// rejection is not surfaced as an error here — the store already logs the
// both-states evidence, and the caller has no terminal-vs-not distinction to
// act on beyond what already happened to the row.
func (l *ExecutionLifecycle) Transition(execID string, s Status) error {
	if l.store == nil || execID == "" {
		return nil
	}
	_, err := l.store.UpdateExecutionStatusIfNotTerminal(execID, string(s))
	return err
}

// FinishOutcome is what Finish computed and persisted, so callers can drive
// their own side effects (execution_events stage mapping, progress
// callbacks) off the same classification instead of re-deriving it.
type FinishOutcome struct {
	Status Status
	Error  string
}

// Classify computes Finish's terminal outcome without persisting anything
// (GH-4259). Split out so a caller that needs to record execution_events
// rows for the terminal stage can do so before the status write Persist
// performs — see Persist's doc comment for why the ordering matters.
//
// execErr, when non-nil, short-circuits classification straight to
// StatusFailed, mirroring every existing call site's "err != nil" branch
// running before TerminalStatus is even consulted.
//
// override, when given, replaces the classified status outright — for a
// caller that has evidence the classifier can't see. epic.go's work-loss
// guard is the motivating case: a sub-issue can report Success with real
// commits but no PR (its work is stranded), which TerminalStatus would call
// "completed" but the epic must record as "failed" so the issue stays open
// for recovery. errMsg is still sourced from execErr/result as usual; only
// the status itself is overridden.
func (l *ExecutionLifecycle) Classify(result *ExecutionResult, execErr error, override ...Status) FinishOutcome {
	var outcome FinishOutcome
	if execErr != nil {
		outcome = FinishOutcome{Status: ExecStatusFailed, Error: execErr.Error()}
	} else {
		outcome = FinishOutcome{Status: Status(TerminalStatus(result))}
		if result != nil {
			outcome.Error = result.Error
		}
	}

	// GH-4404: a PR is ground truth that work was delivered. Something
	// downstream of PR creation — an intent-judge veto arriving after
	// delivery (#4407's truncated-diff false-veto is the incident that
	// exposed this), or any other terminal-but-not-completed signal — can
	// still classify the attempt as failed/declined/etc. Left uncorrected,
	// that write makes HasTerminalCompletion disagree with GitHub reality:
	// the row reads "not done" while the PR sits open, so the poller
	// re-picks the task and re-executes it from scratch — the duplicate-PR
	// class TASK-407 was built to prevent, reached by a different door
	// (pointer GH-16/GH-15). Promote to completed whenever a PR exists,
	// unless the caller supplied an explicit override — epic.go's
	// stranded-work override is the mirror case (commits with NO PR,
	// deliberately kept non-completed so the issue stays open for
	// recovery) and never collides with this: it never has a PRUrl to
	// promote on.
	if outcome.Status != ExecStatusCompleted && len(override) == 0 && result != nil && result.PRUrl != "" {
		outcome = FinishOutcome{Status: ExecStatusCompleted}
	}

	if len(override) > 0 {
		outcome.Status = override[0]
	}
	return outcome
}

// Persist writes a pre-classified outcome for execID: MarkExecutionCompleted
// on success so pr_url/commit_sha/duration land atomically,
// UpdateExecutionStatus otherwise, then saves execution metrics whenever
// result is non-nil — generalizing finalizeSubIssueExecution (the TASK-394
// sub-issue chokepoint) to every caller.
//
// GH-4259: this is the write that makes execID's terminal status visible to
// anything polling GetExecution. Callers that also record an
// execution_events row for the same terminal transition (e.g. the
// dispatcher's StageFailed/StageCompleted writes) MUST call recordExecutionEvent
// before Persist, not after — otherwise a poller can observe the terminal
// status and read the event ledger before the matching event row exists,
// intermittently losing the race (the synthetic dispatch event-sequence
// tests caught this once RecordExecutionEvent's validate-first GetExecution
// round trip, GH-4244, made the event write slow enough to lose more often).
func (l *ExecutionLifecycle) Persist(execID string, outcome FinishOutcome, result *ExecutionResult, duration time.Duration) error {
	if l.store == nil || execID == "" {
		return nil
	}

	// GH-4423: both branches route through the CAS-guarded store writes — a
	// duplicate Finish call (the same execID finished twice, e.g. a retried
	// callback) can no longer silently overwrite a terminal status this row
	// already reached. A rejected write returns err=nil here (the store
	// already logged the both-states evidence); metrics are still saved
	// below regardless, since they're informational and idempotent.
	var statusErr error
	if outcome.Status == ExecStatusCompleted {
		var prURL, commitSHA string
		if result != nil {
			prURL, commitSHA = result.PRUrl, result.CommitSHA
		}
		_, statusErr = l.store.MarkExecutionCompletedIfNotTerminal(execID, prURL, commitSHA, duration.Milliseconds())
	} else {
		_, statusErr = l.store.UpdateExecutionStatusIfNotTerminal(execID, string(outcome.Status), outcome.Error)
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

	return statusErr
}

// Finish terminates execID in one call: Classify followed by Persist.
// Callers that need to record execution_events rows for the terminal stage
// without racing a status poller should call Classify and Persist directly
// instead, recording events between the two (GH-4259) — see Persist's doc
// comment.
func (l *ExecutionLifecycle) Finish(execID string, result *ExecutionResult, execErr error, duration time.Duration, override ...Status) (FinishOutcome, error) {
	if l.store == nil || execID == "" {
		return FinishOutcome{}, nil
	}
	outcome := l.Classify(result, execErr, override...)
	err := l.Persist(execID, outcome, result, duration)
	return outcome, err
}

// LatestExecution returns the most recent execution row for taskID, scoped to
// projectPath the same way memory.Store.GetLatestExecutionByTaskID is (empty
// projectPath skips the filter). It exists so callers outside the executor
// package (GH-4562: alerts.Engine's stuck-task evictor) can resolve a taskID
// to the execID Finish needs without importing internal/memory and reaching
// past this chokepoint to call Store methods directly. A nil store or a
// lookup miss (including sql.ErrNoRows) both return (nil, nil) — "nothing
// found to transition" — mirroring epic.go's sweepStalledEpicChildren, which
// treats any GetLatestExecutionByTaskID error the same way.
func (l *ExecutionLifecycle) LatestExecution(taskID, projectPath string) (*memory.Execution, error) {
	if l.store == nil {
		return nil, nil
	}
	exec, err := l.store.GetLatestExecutionByTaskID(taskID, projectPath)
	if err != nil {
		return nil, nil
	}
	return exec, nil
}

// Cancel implements `pilot task cancel` (GH-4678): the real terminal cancel
// verb that GH-4655 exposed the lack of. Before this, the only documented
// workaround was writing status='stalled' directly — but "stalled" means
// "dead owner, retry me" to nextRetryGeneration, so that workaround kept
// granting the task fresh generations exempt from the repick hard cap
// forever instead of stopping it.
//
// Cancel looks up taskID's latest execution row in projectPath and:
//   - returns ErrExecutionNotFound if there is no row at all,
//   - returns ErrExecutionRunning (naming the execution ID) if the row is
//     currently "running" — v1 refuses rather than killing the backend
//     process, since no PID/handle is tracked anywhere for a running row
//     (see ErrExecutionRunning's doc comment),
//   - returns ErrExecutionAlreadyTerminal if the row is already terminal
//     (completed, failed, canceled, ...) — nothing left to cancel,
//   - otherwise (queued, or any other non-terminal, non-running status)
//     writes ExecStatusCanceled via the same CAS-guarded store path every
//     other terminal transition in this file uses, records the reason in
//     the error column, and journals a StageCanceled execution_events row so
//     `pilot trace` shows the cancel just like any other terminal outcome.
//
// This makes the cancel terminal in every place that matters without any
// call site needing to know the mechanism:
//   - the dispatcher's nextRetryGeneration carve-out refuses generation+1
//     for a canceled row (AC2),
//   - memory.HasTerminalCompletion now counts a canceled row as done, so the
//     SDK poller's pre-dispatch gate and the dispatcher's own
//     hasTerminalSuccessLedger guard both suppress re-dispatch (AC3),
//   - the CAS guard (store.go's terminalExecutionStatuses) now includes
//     "canceled", so nothing can silently resurrect the row afterward.
func (l *ExecutionLifecycle) Cancel(taskID, projectPath, reason string) (*memory.Execution, error) {
	if l.store == nil {
		return nil, ErrExecutionNotFound
	}

	exec, err := l.store.GetLatestExecutionByTaskID(taskID, projectPath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrExecutionNotFound
		}
		return nil, fmt.Errorf("failed to look up execution for %s: %w", taskID, err)
	}

	switch {
	case exec.Status == string(ExecStatusRunning):
		return exec, fmt.Errorf("%w: execution %s", ErrExecutionRunning, exec.ID)
	case IsTerminalStatus(exec.Status):
		return exec, fmt.Errorf("%w: execution %s is already %q", ErrExecutionAlreadyTerminal, exec.ID, exec.Status)
	}

	reasonMsg := reason
	if reasonMsg == "" {
		reasonMsg = "canceled via pilot task cancel"
	}

	if applied, err := l.store.UpdateExecutionStatusIfNotTerminal(exec.ID, string(ExecStatusCanceled), reasonMsg); err != nil {
		return nil, fmt.Errorf("failed to cancel execution %s: %w", exec.ID, err)
	} else if !applied {
		// Lost a race against another terminal write landing between the
		// lookup above and this write (e.g. the run finished concurrently) —
		// the row has its own true terminal status now; report that instead
		// of claiming the cancel took effect.
		return exec, fmt.Errorf("%w: execution %s reached a terminal state concurrently", ErrExecutionAlreadyTerminal, exec.ID)
	}

	if err := l.store.RecordExecutionEvent(exec.ID, memory.StageCanceled, reasonMsg); err != nil {
		logging.WithComponent("executor.lifecycle").Warn("failed to record cancel execution event",
			"execution_id", exec.ID, "task_id", taskID, "error", err)
	}

	exec.Status = string(ExecStatusCanceled)
	exec.Error = reasonMsg
	return exec, nil
}

// IsTerminalStatus reports whether status is one of the executions-table
// terminal statuses (dispatcher.go's terminalExecutionStatuses). Exposed
// alongside LatestExecution so a caller outside the executor package can
// guard a Finish call on current status without duplicating the terminal
// vocabulary (GH-4562) — see terminal_status_inventory_test.go's guard
// against a second copy of this classification.
func IsTerminalStatus(status string) bool {
	return isTerminalExecutionStatus(status)
}
