package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDispatcher_AdoptedPreservedRun_NoBackoffNoBlockedLabel is the GH-5709
// dispatcher-level acceptance test. Both runs end the session with
// uncommitted work that Pilot auto-preserves. When the preserved commit's
// gates are green it is adopted: the execution is "completed", the next
// dispatch pass does not re-pick it, and neither the repick backoff nor the
// pilot-blocked label is armed. The red-gates control run keeps today's
// terminal failure and does escalate (pilot-blocked) — proving the
// assertions are not vacuous.
func TestDispatcher_AdoptedPreservedRun_NoBackoffNoBlockedLabel(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end: real git/gh, skipped under -short")
	}
	tests := []struct {
		name        string
		outcome     *QualityOutcome
		wantStatus  string
		wantBlocked bool
		wantBackoff bool
	}{
		{name: "gates green: adopted", outcome: &QualityOutcome{Passed: true}, wantStatus: "completed"},
		{
			name: "gates red: control keeps today's escalation",
			outcome: &QualityOutcome{GateDetails: []QualityGateDetail{
				{Name: "test", Command: "go test ./...", ExitCode: 1, Output: "--- FAIL: TestX"},
			}},
			wantStatus:  "failed",
			wantBlocked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitleFile, capturedLabelEditsFile := setUpFakeGhPRCreateAndLabelPATH(t)

			const branch = "pilot/GH-5709-dispatch"
			dir, _ := setupFreshnessRepo(t)
			runGit(t, dir, "checkout", "-b", branch)

			backend := &mockGH4964Backend{
				perCall: func(_ int, _ ExecuteOptions) *BackendResult {
					writeUncommittedFile(t, dir, "feature.go")
					return &BackendResult{Success: true, Output: "once it reports, I'll commit"}
				},
			}
			runner := newGH4964Runner(backend)
			outcome := tt.outcome
			runner.qualityCheckerFactory = func(string, string, string) QualityChecker {
				return &stubQualityChecker{outcome: outcome}
			}

			store, cleanup := setupTestStore(t)
			defer cleanup()
			dispatcher := NewDispatcher(store, runner, nil)
			if err := dispatcher.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer dispatcher.Stop()

			task := newGH4964Task("GH-5709", branch, dir)
			execID, err := dispatcher.QueueTask(context.Background(), task)
			if err != nil {
				t.Fatalf("QueueTask: %v", err)
			}

			waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			exec, err := dispatcher.WaitForExecution(waitCtx, execID, 50*time.Millisecond)
			if err != nil {
				t.Fatalf("WaitForExecution: %v", err)
			}
			if exec.Status != tt.wantStatus {
				t.Fatalf("execution status = %q (error=%q), want %q", exec.Status, exec.Error, tt.wantStatus)
			}

			// The next dispatch pass for the same task.
			freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
			retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
			if err != nil {
				t.Fatalf("beginWithGenerationRetry: %v", err)
			}
			if retryExecID != "" {
				t.Errorf("a finished run must not be re-picked, got fresh execID %q", retryExecID)
			}

			_, _, backoffFound, err := dispatcher.RepickBackoffState(repickBackoffKey(task.ProjectPath, task.ID))
			if err != nil {
				t.Fatalf("RepickBackoffState: %v", err)
			}
			if backoffFound != tt.wantBackoff {
				t.Errorf("repick backoff armed = %v, want %v", backoffFound, tt.wantBackoff)
			}

			labelEdits := ""
			if b, readErr := os.ReadFile(capturedLabelEditsFile); readErr == nil {
				labelEdits = string(b)
			}
			if blocked := strings.Contains(labelEdits, "pilot-blocked"); blocked != tt.wantBlocked {
				t.Errorf("pilot-blocked label applied = %v, want %v (gh issue edit calls: %q)", blocked, tt.wantBlocked, labelEdits)
			}

			_, titleErr := os.Stat(capturedTitleFile)
			if (titleErr == nil) != (tt.wantStatus == "completed") {
				t.Errorf("gh pr create invoked = %v, want %v", titleErr == nil, tt.wantStatus == "completed")
			}
		})
	}
}
