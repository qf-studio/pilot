package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5564: the pre-execute merged-PR short-circuit must tell a fix task that
// BORROWED its origin PR's branch (and finds that very PR merged) apart from a
// task whose own PR merged. The former delivered nothing and is superseded +
// closed; the latter keeps the completed path.

const gh5564MergedURL = "https://github.com/qf-studio/pilot/pull/77"

// gh5564FakeGH installs a fake `gh` on PATH that logs every invocation and
// reports the issue as OPEN to `issue view --jq` probes.
func gh5564FakeGH(t *testing.T) (logFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "gh-calls.log")
	script := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"` + "\n" +
		`if [ "$1" = "issue" ] && [ "$2" = "view" ]; then echo OPEN; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return logFile
}

func gh5564Setup(t *testing.T, execID, taskID string) (store *memory.Store, projectPath string, backend *mockFixedBackend, runner *Runner, recorder *fakeMergeMetricsRecorder) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	projectPath = t.TempDir()

	if err := store.SaveExecution(&memory.Execution{
		ID: execID, TaskID: taskID, ProjectPath: projectPath,
		Status: "queued", TaskBranch: "pilot/GH-5564-origin", TaskCreatePR: true,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return gh5564MergedURL, nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origCheck })

	origState := fetchIssueState
	fetchIssueState = func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{}, nil
	}
	t.Cleanup(func() { fetchIssueState = origState })

	backend = &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner = NewRunnerWithBackend(backend)
	recorder = &fakeMergeMetricsRecorder{}
	runner.SetMergeMetricsRecorder(recorder)
	return store, projectPath, backend, runner, recorder
}

func TestProcessQueue_MergedPRShortCircuit_BorrowedOriginPR_Supersedes(t *testing.T) {
	logFile := gh5564FakeGH(t)
	const execID, taskID = "exec-gh5564-borrowed", "GH-5565"
	store, projectPath, backend, runner, recorder := gh5564Setup(t, execID, taskID)

	runner.RecordBorrowedBranch(taskID, "pilot/GH-5564-origin", 77)

	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.processQueue(context.Background())

	got, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "superseded" {
		t.Errorf("status = %q, want superseded", got.Status)
	}
	if got.PRUrl != "" {
		t.Errorf("pr_url = %q, want empty (origin PR is not this task's delivery)", got.PRUrl)
	}
	if backend.execCount != 0 {
		t.Errorf("backend invoked %d times, want 0", backend.execCount)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Errorf("external merge recorded %+v, want none", calls)
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("gh was never invoked: %v", err)
	}
	calls := string(logBytes)
	if n := strings.Count(calls, "issue close 5565 --comment No work performed: this fix issue inherited the branch of PR #77"); n != 1 {
		t.Errorf("want exactly one close-with-comment, got %d; calls:\n%s", n, calls)
	}
	if n := strings.Count(calls, "issue comment"); n != 0 {
		t.Errorf("want no separate comment call, got %d; calls:\n%s", n, calls)
	}
	for _, want := range []string{"--add-label pilot-superseded", "--remove-label pilot-in-progress", "--remove-label pilot-failed"} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh calls missing %q; calls:\n%s", want, calls)
		}
	}
}

func TestProcessQueue_MergedPRShortCircuit_OwnPR_StillCompletes(t *testing.T) {
	logFile := gh5564FakeGH(t)
	const execID, taskID = "exec-gh5564-own", "GH-5566"
	store, projectPath, backend, runner, recorder := gh5564Setup(t, execID, taskID)

	// No RecordBorrowedBranch: the merged PR is this task's own.
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.processQueue(context.Background())

	got, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.PRUrl != gh5564MergedURL {
		t.Errorf("pr_url = %q, want %q", got.PRUrl, gh5564MergedURL)
	}
	if backend.execCount != 0 {
		t.Errorf("backend invoked %d times, want 0", backend.execCount)
	}
	if calls := recorder.snapshot(); len(calls) != 1 || calls[0].PRNumber != 77 {
		t.Errorf("want exactly one external merge for PR 77, got %+v", calls)
	}
	if b, err := os.ReadFile(logFile); err == nil && strings.Contains(string(b), "issue close") {
		t.Errorf("own-PR path must not close the issue; calls:\n%s", b)
	}
}

// A borrow record for a DIFFERENT PR than the one the preflight found merged
// is the GH-5400 case (another PR on the same branch merged) and must keep the
// completed path — the fromPR equality check is what separates it from the
// superseded path.
func TestProcessQueue_MergedPRShortCircuit_BorrowedDifferentPR_StillCompletes(t *testing.T) {
	logFile := gh5564FakeGH(t)
	const execID, taskID = "exec-gh5564-otherpr", "GH-5567"
	store, projectPath, _, runner, _ := gh5564Setup(t, execID, taskID)

	runner.RecordBorrowedBranch(taskID, "pilot/GH-5564-origin", 76)

	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.processQueue(context.Background())

	got, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "completed" || got.PRUrl != gh5564MergedURL {
		t.Errorf("status/pr_url = %q/%q, want completed/%q", got.Status, got.PRUrl, gh5564MergedURL)
	}
	if b, err := os.ReadFile(logFile); err == nil && strings.Contains(string(b), "issue close") {
		t.Errorf("different-PR path must not close the issue; calls:\n%s", b)
	}
}
