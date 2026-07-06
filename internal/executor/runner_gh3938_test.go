package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// tokenFakeBackend is a minimal Backend that returns a configurable token
// count without spawning a real subprocess, used to simulate "Claude was
// actually invoked and consumed tokens" (GH-3938).
type tokenFakeBackend struct {
	tokensIn, tokensOut int64
}

func (b tokenFakeBackend) Name() string      { return "token-fake" }
func (b tokenFakeBackend) IsAvailable() bool { return true }
func (b tokenFakeBackend) Execute(_ context.Context, _ ExecuteOptions) (*BackendResult, error) {
	return &BackendResult{
		Success:      true,
		Output:       "fake output",
		TokensInput:  b.tokensIn,
		TokensOutput: b.tokensOut,
	}, nil
}

// TestDispatcher_SyntheticDispatch_TokenStream_FullEventSequence covers GH-3938
// acceptance (a): a fake Claude stream that actually reports token usage must
// (1) persist tokens_output/tokens_input > 0 on the executions row — the
// "37-minute run must not persist as tokens_output=0" token-harvester fix —
// and (2) produce the full execution_events lifecycle past spec_validated
// (claude_started, implementation_started, and the terminal completed event),
// closing the fail-silent regression where a trace dead-ended at spec_validated.
func TestDispatcher_SyntheticDispatch_TokenStream_FullEventSequence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunnerWithBackend(tokenFakeBackend{tokensIn: 5000, tokensOut: 3200})
	runner.skipPreflightChecks = true
	runner.SetLogStore(store)
	runner.SetRecordingEnabled(false)

	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-SYNTH-TOKENS",
		Title:       "Synthetic dispatch with real token usage",
		Description: "GH-3938 fake Claude stream coverage",
		ProjectPath: t.TempDir(),
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	exec := waitForTerminalStatus(t, store, execID, 10*time.Second)
	if exec.Status != "completed" {
		t.Fatalf("expected status completed, got %q (error: %s)", exec.Status, exec.Error)
	}

	// SaveExecutionMetrics writes tokens in a follow-up store call after the
	// status flip to "completed" — poll briefly instead of asserting
	// immediately to avoid a race against that write.
	deadline := time.Now().Add(5 * time.Second)
	for exec.TokensOutput == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		exec, err = store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution failed: %v", err)
		}
	}

	if exec.TokensOutput <= 0 {
		t.Fatalf("expected executions.tokens_output > 0 for a run where Claude was actually invoked, got %d", exec.TokensOutput)
	}
	if exec.TokensInput <= 0 {
		t.Fatalf("expected executions.tokens_input > 0, got %d", exec.TokensInput)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	wantStages := []memory.Stage{
		memory.StageRunning,
		memory.StageSpecValidated,
		memory.StageClaudeStarted,
		memory.StageImplementationStarted,
		memory.StageCompleted,
	}
	if len(events) != len(wantStages) {
		var gotStages []memory.Stage
		for _, e := range events {
			gotStages = append(gotStages, e.Stage)
		}
		t.Fatalf("got %d events %v, want %d %v", len(events), gotStages, len(wantStages), wantStages)
	}
	for i, want := range wantStages {
		if events[i].Stage != want {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, want)
		}
	}
}

// TestApplyZeroArtifactNoOpGuard covers GH-3938 acceptance (b): a run that
// claims success but shows zero tokens, zero files, and no commit/PR must be
// reclassified as no_op instead of completed — mirroring the GH-3224 ghost-SHA
// guard shape (bug_pilot_ghost_closes.md) — while a run with any real artifact
// (tokens, files, commit, or PR) must be left untouched.
func TestApplyZeroArtifactNoOpGuard(t *testing.T) {
	tests := []struct {
		name        string
		result      *ExecutionResult
		wantSuccess bool
		wantOutcome string
	}{
		{
			name:        "zero tokens, zero files, no commit, no PR — reclassified as no_op",
			result:      &ExecutionResult{Success: true},
			wantSuccess: false,
			wantOutcome: "no_op",
		},
		{
			name:        "real output tokens — left untouched",
			result:      &ExecutionResult{Success: true, TokensInput: 100, TokensOutput: 50},
			wantSuccess: true,
			wantOutcome: "",
		},
		{
			name:        "files changed — left untouched",
			result:      &ExecutionResult{Success: true, FilesChanged: 2},
			wantSuccess: true,
			wantOutcome: "",
		},
		{
			name:        "commit present — left untouched",
			result:      &ExecutionResult{Success: true, CommitSHA: "abc1234"},
			wantSuccess: true,
			wantOutcome: "",
		},
		{
			name:        "PR present — left untouched",
			result:      &ExecutionResult{Success: true, PRUrl: "https://github.com/org/repo/pull/1"},
			wantSuccess: true,
			wantOutcome: "",
		},
		{
			name:        "already failed — left untouched (guard is success-only)",
			result:      &ExecutionResult{Success: false, Error: "some other failure"},
			wantSuccess: false,
			wantOutcome: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyZeroArtifactNoOpGuard(tt.result, "test task")
			if tt.result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", tt.result.Success, tt.wantSuccess)
			}
			if tt.result.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", tt.result.Outcome, tt.wantOutcome)
			}
			if tt.wantOutcome == "no_op" {
				if !strings.Contains(tt.result.Error, "no new commit produced") {
					t.Errorf("Error = %q, want it to contain the shared no-op marker %q", tt.result.Error, "no new commit produced")
				}
				if status := TerminalStatus(tt.result); status != "no_op" {
					t.Errorf("TerminalStatus = %q, want no_op", status)
				}
			}
		})
	}
}

// TestExecuteWithOptions_ZeroArtifactNoOpGuard drives GH-3938 acceptance (b)
// through the real Runner.Execute path: a task that expects a deliverable
// (CreatePR) but whose backend produces no tokens, no files, and no commit/PR
// must come back Success=false/Outcome=no_op instead of completed — and a
// task that never expected a deliverable (plain read-only/LocalMode-style
// run) must be left as a legitimate success. The DirectCommit branch of the
// same guard (task.CreatePR || task.DirectCommit scoping in runner.go) is
// covered at the unit level by TestApplyZeroArtifactNoOpGuard instead of here,
// since exercising DirectCommit's git-push path needs a real remote.
func TestExecuteWithOptions_ZeroArtifactNoOpGuard(t *testing.T) {
	tests := []struct {
		name        string
		createPR    bool
		wantSuccess bool
		wantOutcome string
	}{
		{
			name:        "CreatePR expected a deliverable — zero output trips no_op",
			createPR:    true,
			wantSuccess: false,
			wantOutcome: "no_op",
		},
		{
			name:        "no deliverable expected — zero output is a legitimate success",
			createPR:    false,
			wantSuccess: true,
			wantOutcome: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunnerWithBackend(tokenFakeBackend{})
			runner.skipPreflightChecks = true

			task := &Task{
				ID:          "GH-ZERO-OUTPUT",
				Title:       "Task that produces nothing",
				ProjectPath: t.TempDir(),
				CreatePR:    tt.createPR,
			}

			result, err := runner.Execute(context.Background(), task)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v (error: %s)", result.Success, tt.wantSuccess, result.Error)
			}
			if result.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", result.Outcome, tt.wantOutcome)
			}
			if tt.wantOutcome == "no_op" {
				if !strings.Contains(result.Error, "no new commit produced") {
					t.Errorf("Error = %q, want it to contain the shared no-op marker", result.Error)
				}
				if status := TerminalStatus(result); status != "no_op" {
					t.Errorf("TerminalStatus = %q, want no_op", status)
				}
			}
		})
	}
}

// TestDecomposedEventDetail covers the StageDecomposed detail string (GH-3938):
// "decomposed into N children: #a, #b, #c", sourced from the actual dispatched
// child issues rather than the planned subtask count.
func TestDecomposedEventDetail(t *testing.T) {
	tests := []struct {
		name   string
		issues []CreatedIssue
		want   string
	}{
		{
			name:   "github issues",
			issues: []CreatedIssue{{Number: 101}, {Number: 102}, {Number: 103}},
			want:   "decomposed into 3 children: #101, #102, #103",
		},
		{
			name:   "non-github identifiers",
			issues: []CreatedIssue{{Identifier: "APP-1"}, {Identifier: "APP-2"}},
			want:   "decomposed into 2 children: APP-1, APP-2",
		},
		{
			name:   "no children",
			issues: nil,
			want:   "decomposed into 0 children: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decomposedEventDetail(tt.issues)
			if got != tt.want {
				t.Errorf("decomposedEventDetail() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunner_Execute_EpicDecomposition_RecordsChildrenAndAggregatesTokens
// covers GH-3938 acceptance (c) plus the token-harvester fix together: an epic
// parent that dispatches real child sub-issue work must (1) record the
// StageDecomposed execution_event carrying the actual child issue list, (2)
// carry those same children on ExecutionResult.ChildIssues so the completion
// comment can report an honest "decomposed into N children: ..." summary
// instead of a misleading "no changes were made", and (3) aggregate each
// child's real token usage onto the epic-parent's own result instead of
// persisting as tokens_output=0.
func TestRunner_Execute_EpicDecomposition_RecordsChildrenAndAggregatesTokens(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	r := NewRunner()
	r.skipPreflightChecks = true
	r.dryRun = true
	r.SetLogStore(store)

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-epic-gh9002",
		TaskID:      "GH-9002",
		ProjectPath: "/project",
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// openSubIssueCheck returns true → CreateSubIssues returns ErrSubIssuesAlreadyExist,
	// so recoverSubIssuesFn supplies the child list without touching the real gh CLI.
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}

	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return []CreatedIssue{
			{Number: 21, Identifier: "21", URL: "https://github.com/o/r/issues/21", State: "open",
				Subtask: PlannedSubtask{Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"}},
		}, nil
	}

	// The child sub-issue "actually ran" and consumed real tokens.
	r.executeFunc = func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, TokensInput: 4000, TokensOutput: 2500}, nil
	}

	r.planEpicFn = func(_ context.Context, _ *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{
			ParentTask: &Task{ID: "GH-9002"},
			Subtasks: []PlannedSubtask{
				{Order: 1, Title: "feat(gateway): add websocket handler", Description: "Implement upgrade handler in internal/gateway/server.go"},
				{Order: 2, Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"},
			},
		}, nil
	}

	task := &Task{
		ID:          "GH-9002",
		Title:       "[epic] decomposition records children and aggregates tokens",
		ExecutionID: "exec-epic-gh9002",
	}

	result, err := r.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.IsEpic {
		t.Error("expected IsEpic=true")
	}
	if !result.Success {
		t.Errorf("expected Success=true (child delivered real tokens), got false — error: %s", result.Error)
	}
	if len(result.ChildIssues) != 1 || result.ChildIssues[0].Number != 21 {
		t.Errorf("expected ChildIssues=[#21], got %+v", result.ChildIssues)
	}
	if result.TokensOutput <= 0 {
		t.Errorf("expected epic result to aggregate the child's tokens_output, got %d", result.TokensOutput)
	}
	if result.TokensInput <= 0 {
		t.Errorf("expected epic result to aggregate the child's tokens_input, got %d", result.TokensInput)
	}

	events, err := store.ListExecutionEvents("exec-epic-gh9002")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	var decomposedDetail string
	found := false
	for _, e := range events {
		if e.Stage == memory.StageDecomposed {
			found = true
			decomposedDetail = e.Detail
		}
	}
	if !found {
		var stages []memory.Stage
		for _, e := range events {
			stages = append(stages, e.Stage)
		}
		t.Fatalf("expected a StageDecomposed event, got stages %v", stages)
	}
	if want := "decomposed into 1 children: #21"; decomposedDetail != want {
		t.Errorf("StageDecomposed detail = %q, want %q", decomposedDetail, want)
	}
}
