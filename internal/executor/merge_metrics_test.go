package executor

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// fakeMergeMetricsRecorder is a test double for MergeMetricsRecorder that
// captures every recorded merge so tests can assert exactly what (and how
// many times) was recorded, without pulling in the autopilot package.
type fakeMergeMetricsRecorder struct {
	mu    sync.Mutex
	calls []fakeMergeCall
}

type fakeMergeCall struct {
	ProjectPath string
	PRNumber    int
}

func (f *fakeMergeMetricsRecorder) RecordExternalMerge(projectPath string, prNumber int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeMergeCall{ProjectPath: projectPath, PRNumber: prNumber})
}

func (f *fakeMergeMetricsRecorder) snapshot() []fakeMergeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMergeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestRunner_RecordExternalMerge covers Runner.recordExternalMerge's own
// contract in isolation: forwards a well-formed PR URL as (projectPath,
// prNumber) to the configured recorder, skips URLs that don't parse to a PR
// number, and never panics on a nil Runner or an unconfigured recorder
// (GH-4390).
func TestRunner_RecordExternalMerge(t *testing.T) {
	t.Run("forwards parsed PR number to configured recorder", func(t *testing.T) {
		runner := NewRunner()
		recorder := &fakeMergeMetricsRecorder{}
		runner.SetMergeMetricsRecorder(recorder)

		runner.recordExternalMerge("/project-a", "https://github.com/qf-studio/pilot/pull/4390")

		got := recorder.snapshot()
		if len(got) != 1 {
			t.Fatalf("expected 1 recorded call, got %d: %+v", len(got), got)
		}
		if got[0].ProjectPath != "/project-a" || got[0].PRNumber != 4390 {
			t.Errorf("expected {/project-a 4390}, got %+v", got[0])
		}
	})

	t.Run("unparseable PR URL is skipped, not recorded as PR 0", func(t *testing.T) {
		runner := NewRunner()
		recorder := &fakeMergeMetricsRecorder{}
		runner.SetMergeMetricsRecorder(recorder)

		runner.recordExternalMerge("/project-a", "not-a-valid-pr-url")

		if got := recorder.snapshot(); len(got) != 0 {
			t.Errorf("expected no recorded calls for an unparseable URL, got %+v", got)
		}
	})

	t.Run("no recorder configured does not panic", func(t *testing.T) {
		runner := NewRunner()
		runner.recordExternalMerge("/project-a", "https://github.com/qf-studio/pilot/pull/1")
	})

	t.Run("nil Runner does not panic", func(t *testing.T) {
		var runner *Runner
		runner.recordExternalMerge("/project-a", "https://github.com/qf-studio/pilot/pull/1")
	})
}

// TestMergeMetricsRecorded_AcrossSelfHealPaths is the GH-4390 table-driven
// acceptance test: every path that discovers a task's branch was already
// merged on GitHub outside the autopilot controller's own merge flow must
// route through Runner.recordExternalMerge exactly once. Covers the three
// executor-side call sites — boot orphan heal (GH-4392), stale-running heal
// (GH-4092), and the pre-execute merged-PR short-circuit (GH-4141 Phase 3) —
// each exercised through its real production code path (not the bare
// recordExternalMerge unit above), so the wiring at each call site is what's
// actually under test.
func TestMergeMetricsRecorded_AcrossSelfHealPaths(t *testing.T) {
	const mergedPRURL = "https://github.com/qf-studio/pilot/pull/9500"
	const wantPRNumber = 9500

	tests := []struct {
		name        string
		projectPath string
		run         func(t *testing.T, store *memory.Store, recorder *fakeMergeMetricsRecorder, projectPath string)
	}{
		{
			name:        "boot orphan heal",
			projectPath: "/project-merge-metrics-orphan",
			run: func(t *testing.T, store *memory.Store, recorder *fakeMergeMetricsRecorder, projectPath string) {
				task := &Task{ID: "GH-9500", ProjectPath: projectPath}
				if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning); err != nil {
					t.Fatalf("setup Begin: %v", err)
				}

				origCheck := staleRunningMergedPRCheck
				staleRunningMergedPRCheck = func(_ context.Context, gotProjectPath, _ string) (string, error) {
					if gotProjectPath == projectPath {
						return mergedPRURL, nil
					}
					return "", nil
				}
				defer func() { staleRunningMergedPRCheck = origCheck }()

				runner := NewRunner()
				runner.SetMergeMetricsRecorder(recorder)
				dispatcher := NewDispatcher(store, runner, nil)
				dispatcher.reconcileOrphanedExecutions()
			},
		},
		{
			name:        "stale-running heal",
			projectPath: "/project-merge-metrics-stale",
			run: func(t *testing.T, store *memory.Store, recorder *fakeMergeMetricsRecorder, projectPath string) {
				exec := &memory.Execution{ID: "exec-merge-metrics-stale", TaskID: "GH-9500", ProjectPath: projectPath, Status: "running"}
				if err := store.SaveExecution(exec); err != nil {
					t.Fatalf("SaveExecution: %v", err)
				}

				origCheck := staleRunningMergedPRCheck
				staleRunningMergedPRCheck = func(_ context.Context, gotProjectPath, _ string) (string, error) {
					if gotProjectPath == projectPath {
						return mergedPRURL, nil
					}
					return "", nil
				}
				defer func() { staleRunningMergedPRCheck = origCheck }()

				runner := NewRunner()
				runner.SetMergeMetricsRecorder(recorder)
				dispatcher := NewDispatcher(store, runner, &DispatcherConfig{
					StaleRunningThreshold: 0,
					StaleQueuedThreshold:  0,
					StaleRecoveryInterval: time.Hour,
				})
				dispatcher.recoverStaleRunningTasks()
			},
		},
		{
			name:        "pre-execute merged-PR short-circuit",
			projectPath: "/project-merge-metrics-preflight",
			run: func(t *testing.T, store *memory.Store, recorder *fakeMergeMetricsRecorder, projectPath string) {
				exec := &memory.Execution{
					ID: "exec-merge-metrics-preflight", TaskID: "GH-9500", ProjectPath: projectPath,
					Status: "queued", TaskBranch: "pilot/GH-9500", TaskCreatePR: true,
				}
				if err := store.SaveExecution(exec); err != nil {
					t.Fatalf("SaveExecution: %v", err)
				}

				origCheck := mergedPRPreflightCheck
				mergedPRPreflightCheck = func(_ context.Context, gotProjectPath, _ string) (string, error) {
					if gotProjectPath == projectPath {
						return mergedPRURL, nil
					}
					return "", nil
				}
				defer func() { mergedPRPreflightCheck = origCheck }()

				backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
				runner := NewRunnerWithBackend(backend)
				runner.SetMergeMetricsRecorder(recorder)
				worker := NewProjectWorker(projectPath, store, runner, slog.Default())
				worker.processQueue(context.Background())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			recorder := &fakeMergeMetricsRecorder{}
			tc.run(t, store, recorder, tc.projectPath)

			got := recorder.snapshot()
			if len(got) != 1 {
				t.Fatalf("expected exactly 1 recorded merge, got %d: %+v", len(got), got)
			}
			if got[0].ProjectPath != tc.projectPath || got[0].PRNumber != wantPRNumber {
				t.Errorf("expected {%s %d}, got %+v", tc.projectPath, wantPRNumber, got[0])
			}
		})
	}
}
