package executor

import (
	"context"
	"errors"
	"testing"
)

// guardRecordingBackend records whether Execute was invoked and with which
// ProjectPath, so tests can assert that a rejected call never reaches the
// backend at all (a loud error, not a silent skip that still executes).
type guardRecordingBackend struct {
	called         bool
	gotProjectPath string
}

func (b *guardRecordingBackend) Name() string      { return "guard-recording" }
func (b *guardRecordingBackend) IsAvailable() bool { return true }

func (b *guardRecordingBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.called = true
	b.gotProjectPath = opts.ProjectPath
	return &BackendResult{Success: true}, nil
}

// TestRunner_BackendExecute_PathIdentityGuard is the GH-4703 regression
// guard for the backendExecute chokepoint. It covers the recurrence class
// from TASK-323, GH-3577/PR#3580, and #4702: a call site whose resolved
// execution path collapses back to task.ProjectPath (the daemon's shared
// repo root) while worktree isolation was expected for the task.
func TestRunner_BackendExecute_PathIdentityGuard(t *testing.T) {
	const projectRoot = "/repo/root"
	const worktreePath = "/repo/.worktrees/GH-4703"

	tests := []struct {
		name          string
		config        *BackendConfig
		task          *Task
		executionPath string
		wantErr       bool
		wantCalled    bool
	}{
		{
			name:   "worktree expected but execution path collapses to project root: guarded error",
			config: &BackendConfig{UseWorktree: true},
			task: &Task{
				ID:           "GH-4703-a",
				ProjectPath:  projectRoot,
				Branch:       "pilot/GH-4703",
				DirectCommit: false,
			},
			executionPath: projectRoot, // bug shape: should have been worktreePath
			wantErr:       true,
			wantCalled:    false,
		},
		{
			name:   "LocalMode exception (task.Branch empty): root-scoped call allowed",
			config: &BackendConfig{UseWorktree: true},
			task: &Task{
				ID:          "GH-4703-b",
				ProjectPath: projectRoot,
				Branch:      "", // LocalMode/Q&A/CLI: legitimately root-scoped
			},
			executionPath: projectRoot,
			wantErr:       false,
			wantCalled:    true,
		},
		{
			name:   "DirectCommit exception: root-scoped call allowed",
			config: &BackendConfig{UseWorktree: true},
			task: &Task{
				ID:           "GH-4703-c",
				ProjectPath:  projectRoot,
				Branch:       "pilot/GH-4703",
				DirectCommit: true,
			},
			executionPath: projectRoot,
			wantErr:       false,
			wantCalled:    true,
		},
		{
			name:   "correct worktree path: allowed",
			config: &BackendConfig{UseWorktree: true},
			task: &Task{
				ID:          "GH-4703-d",
				ProjectPath: projectRoot,
				Branch:      "pilot/GH-4703",
			},
			executionPath: worktreePath,
			wantErr:       false,
			wantCalled:    true,
		},
		{
			name:   "UseWorktree disabled entirely: root-scoped call allowed",
			config: &BackendConfig{UseWorktree: false},
			task: &Task{
				ID:          "GH-4703-e",
				ProjectPath: projectRoot,
				Branch:      "pilot/GH-4703",
			},
			executionPath: projectRoot,
			wantErr:       false,
			wantCalled:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &guardRecordingBackend{}
			runner := NewRunnerWithBackend(backend)
			runner.config = tt.config
			runner.SetSkipPreflightChecks(true)
			runner.SetRecordingEnabled(false)

			_, err := runner.backendExecute(context.Background(), tt.task, tt.executionPath, ExecuteOptions{
				Prompt: "test prompt",
				TaskID: tt.task.ID,
			})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrProjectPathNotIsolated) {
					t.Errorf("expected ErrProjectPathNotIsolated, got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if backend.called != tt.wantCalled {
				t.Errorf("backend.called = %v, want %v", backend.called, tt.wantCalled)
			}

			if tt.wantCalled && backend.gotProjectPath != tt.executionPath {
				t.Errorf("backend received ProjectPath %q, want %q", backend.gotProjectPath, tt.executionPath)
			}
		})
	}
}

// TestRunner_BackendExecute_SetsProjectPathAuthoritatively verifies that
// backendExecute always assigns opts.ProjectPath from the executionPath
// argument, ignoring whatever the caller populated in the ExecuteOptions
// literal — the structural half of the GH-4703 fix. A call site can no
// longer pass a stale or incorrect ProjectPath in the literal and have it
// silently win.
func TestRunner_BackendExecute_SetsProjectPathAuthoritatively(t *testing.T) {
	backend := &guardRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{ID: "GH-4703-f", ProjectPath: "/repo/root"}

	_, err := runner.backendExecute(context.Background(), task, "/repo/.worktrees/expected", ExecuteOptions{
		Prompt:      "test prompt",
		TaskID:      task.ID,
		ProjectPath: "/some/stale/path-a-call-site-mistakenly-set",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if backend.gotProjectPath != "/repo/.worktrees/expected" {
		t.Errorf("backend received ProjectPath %q, want the executionPath argument to win", backend.gotProjectPath)
	}
}
