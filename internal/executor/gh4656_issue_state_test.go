package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-4656 regression suite: the 2026-07-31 incident window where a queued or
// in-flight run's GitHub issue was closed out from under it (delivered by a
// sibling/parent run) went unnoticed because every existing pickup-time
// re-check was task_id-local and the PR-creation preflight was branch-local.
// These tests exercise the two new GitHub-state guards this issue adds:
//
//   - the dispatcher's pickup-time revalidation (dispatcher.go, before
//     Execute()), driven end-to-end via ProjectWorker.processQueue — mirrors
//     TestProcessQueue_MergedPRPreflight_SkipsBackend's shape.
//   - the runner's PR-creation preflight, exercised directly against the
//     extracted checkIssueSupersededBeforePR method — mirrors
//     TestDirectPathExistingBranchPR's use of adoptOpenBranchPR as a unit
//     boundary instead of driving a full Execute() through git push.
//
// Both guards share the same swappable fetchIssueState var (issue_state.go),
// stubbed here exactly like the existing mergedPRPreflightCheck idiom —
// no real git remote or GitHub call is exercised.

// stubFetchIssueState overrides the package-level fetchIssueState var for the
// duration of the test and restores the original on cleanup.
func stubFetchIssueState(t *testing.T, fn func(ctx context.Context, runner *Runner, task *Task, projectPath string) (IssueState, error)) {
	t.Helper()
	orig := fetchIssueState
	fetchIssueState = fn
	t.Cleanup(func() { fetchIssueState = orig })
}

// (a) claim admitted -> issue closed before pickup -> no Execute, row
// finalized with the typed "superseded" status.
func TestProcessQueue_IssueClosedBeforePickup_SupersedesWithoutExecute(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const projectPath = "/project-gh4656-pickup-closed"
	const taskID = "GH-8101"

	exec := &memory.Execution{
		ID:           "exec-gh4656-pickup-closed",
		TaskID:       taskID,
		ProjectPath:  projectPath,
		Status:       "queued",
		TaskBranch:   "pilot/GH-8101",
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, task *Task, _ string) (IssueState, error) {
		if task.ID != taskID {
			t.Fatalf("fetchIssueState called with unexpected task ID %q", task.ID)
		}
		return IssueState{Closed: true, Labels: []string{"pilot-superseded", "pilot"}}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations (issue already closed at pickup), got %d", count)
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != string(ExecStatusSuperseded) {
		t.Errorf("expected status %q, got %q", ExecStatusSuperseded, got.Status)
	}

	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one execution event")
	}
	last := events[len(events)-1]
	if last.Stage != memory.StageSuperseded {
		t.Errorf("expected last event stage %q, got %q", memory.StageSuperseded, last.Stage)
	}
	if !strings.Contains(last.Detail, "closed before pickup") || !strings.Contains(last.Detail, "superseded_label=true") {
		t.Errorf("expected event detail to name the closed state and superseded label, got %q", last.Detail)
	}
}

// (b) run finishes -> issue closed mid-run -> PR refused, finalized
// superseded. Exercises checkIssueSupersededBeforePR directly, mirroring how
// TestDirectPathExistingBranchPR exercises adoptOpenBranchPR without driving
// a full Execute() through git push/PR creation.
func TestCheckIssueSupersededBeforePR(t *testing.T) {
	tests := []struct {
		name          string
		fetchResult   IssueState
		fetchErr      error
		wantHandled   bool
		wantOutcome   string
		wantLogWarn   bool
		wantEventKind bool
	}{
		{
			name:          "issue closed mid-run: PR refused, superseded",
			fetchResult:   IssueState{Closed: true, Labels: []string{"pilot-superseded"}},
			wantHandled:   true,
			wantOutcome:   "superseded",
			wantEventKind: true,
		},
		{
			name:        "issue still open: proceeds to create PR as today",
			fetchResult: IssueState{Closed: false},
			wantHandled: false,
		},
		{
			name:        "GitHub 5xx on state fetch: fails open, proceeds",
			fetchErr:    errors.New("GitHub API: 503 Service Unavailable"),
			wantHandled: false,
			wantLogWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
				return tt.fetchResult, tt.fetchErr
			})

			var logBuf bytes.Buffer
			r := &Runner{log: slog.New(slog.NewTextHandler(&logBuf, nil))}
			r.SetLogStore(store)

			task := &Task{ID: "GH-8102", Title: "fix: mid-run supersede test", Branch: "pilot/GH-8102"}
			if err := store.SaveExecution(&memory.Execution{ID: task.ID, TaskID: task.ID, Status: "running"}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}
			result := &ExecutionResult{TaskID: task.ID, Success: true}

			handled := r.checkIssueSupersededBeforePR(context.Background(), task, result)
			if handled != tt.wantHandled {
				t.Fatalf("checkIssueSupersededBeforePR handled = %v, want %v", handled, tt.wantHandled)
			}

			if tt.wantHandled {
				if result.Success {
					t.Error("expected result.Success = false when PR is refused")
				}
				if result.Outcome != tt.wantOutcome {
					t.Errorf("result.Outcome = %q, want %q", result.Outcome, tt.wantOutcome)
				}
			} else {
				if !result.Success {
					t.Errorf("expected result.Success to remain true when not handled, got false (error=%q)", result.Error)
				}
			}

			events, err := store.ListExecutionEvents(task.LogExecutionID())
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			if tt.wantEventKind {
				if len(events) == 0 {
					t.Fatal("expected a superseded execution event to be recorded")
				}
				if events[len(events)-1].Stage != memory.StageSuperseded {
					t.Errorf("expected last event stage %q, got %q", memory.StageSuperseded, events[len(events)-1].Stage)
				}
			} else if len(events) != 0 {
				t.Errorf("expected no execution events, got %d", len(events))
			}

			if tt.wantLogWarn && !strings.Contains(logBuf.String(), "Failed to revalidate issue state before PR creation") {
				t.Errorf("expected a fail-open warning to be logged, got log: %q", logBuf.String())
			}
		})
	}
}

// (c) open issue at pickup: identical behavior to today — the backend still
// runs, and no "superseded" status is spuriously written.
func TestProcessQueue_IssueOpenAtPickup_ProceedsNormally(t *testing.T) {
	const branch = "pilot/GH-8103"
	dir := setupPRGuardRepo(t, branch, false) // no additional commits

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:           "exec-gh4656-pickup-open",
		TaskID:       "GH-8103",
		ProjectPath:  dir,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origCheck })

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to be invoked (issue open) — pickup guard must not have short-circuited")
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status == string(ExecStatusSuperseded) {
		t.Errorf("did not expect status %q for an open issue", ExecStatusSuperseded)
	}
}

// (d) GitHub 5xx on the pickup-time state fetch: fails open — the backend
// still runs, with a logged warning naming the failure.
func TestProcessQueue_IssueStateFetchError_FailsOpenAtPickup(t *testing.T) {
	const branch = "pilot/GH-8104"
	dir := setupPRGuardRepo(t, branch, false) // no additional commits

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:           "exec-gh4656-pickup-error",
		TaskID:       "GH-8104",
		ProjectPath:  dir,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	fetchErr := fmt.Errorf("GitHub API: 503 Service Unavailable")
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{}, fetchErr
	})
	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origCheck })

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	worker := NewProjectWorker(dir, store, runner, log)

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to still be invoked despite the GitHub lookup error (fail-open)")
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status == string(ExecStatusSuperseded) {
		t.Errorf("did not expect status %q when the state fetch merely errored", ExecStatusSuperseded)
	}

	if !strings.Contains(logBuf.String(), "Failed to revalidate issue state before pickup") {
		t.Errorf("expected a fail-open warning naming the pickup-time lookup failure, got log: %q", logBuf.String())
	}
}
