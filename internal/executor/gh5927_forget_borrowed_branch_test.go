package executor

import (
	"testing"
)

// TestForgetBorrowedBranch is the GH-5927 registry-hygiene test: a fix task
// that was closed, declined or cancelled must stop owning its borrowed branch.
func TestForgetBorrowedBranch(t *testing.T) {
	tests := []struct {
		name       string
		forget     string
		wantBranch bool // "pilot/GH-5917" still resolves after the forget
	}{
		{name: "forgetting the recorded task drops the entry", forget: "GH-5922", wantBranch: false},
		{name: "forgetting another task leaves the entry", forget: "GH-9999", wantBranch: true},
		{name: "forgetting an unknown task is a no-op", forget: "", wantBranch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{}
			r.RecordBorrowedBranch("GH-5922", "pilot/GH-5917", 5921)

			r.ForgetBorrowedBranch(tt.forget)

			_, _, peek := r.BorrowedBranch("GH-5922")
			fix, consumed := r.FixIssueForBranch("pilot/GH-5917")
			if peek != tt.wantBranch || consumed != tt.wantBranch {
				t.Fatalf("after forgetting %q: BorrowedBranch=%v FixIssueForBranch=(%d,%v), want present=%v", tt.forget, peek, fix, consumed, tt.wantBranch)
			}
		})
	}
}

// Every way a fix task can end without opening its PR drops the borrowed-branch
// entry: Cancel, the DECLINED pre-flight outcome (finishDeclined) and a model
// refusal (Dispatcher.escalateRefusal). Each case runs the real code path on a
// real Runner registry.
func TestEndedFixTaskForgetsBorrowedBranch(t *testing.T) {
	const (
		taskID = "GH-5922"
		branch = "pilot/GH-5917"
	)
	tests := []struct {
		name string
		end  func(t *testing.T, r *Runner)
	}{
		{"Cancel", func(t *testing.T, r *Runner) {
			_ = r.Cancel(taskID) // not running: returns an error, but the entry must still go
		}},
		{"finishDeclined", func(t *testing.T, r *Runner) {
			task := &Task{ID: taskID}
			result := &ExecutionResult{TaskID: taskID}
			r.finishDeclined(task, result, nil, nil, &progressState{}, r.log, "DECLINED: nothing to change")
			if !result.Declined {
				t.Fatal("finishDeclined did not mark the result declined")
			}
		}},
		{"escalateRefusal", func(t *testing.T, r *Runner) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			task := &Task{ID: taskID, ProjectPath: "/tmp/project-gh5927", SourceAdapter: "linear"} // non-GitHub: no issue comment
			execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning, 0)
			if err != nil {
				t.Fatalf("setup: Begin: %v", err)
			}
			d := NewDispatcher(store, r, nil)

			d.escalateRefusal(task, "refusal: policy: declined")

			rec, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if rec.Status != string(ExecStatusDeclined) {
				t.Fatalf("execution status = %q, want %q", rec.Status, ExecStatusDeclined)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner()
			r.RecordBorrowedBranch(taskID, branch, 5921)

			tt.end(t, r)

			if fix, ok := r.FixIssueForBranch(branch); ok {
				t.Fatalf("borrowed-branch entry survived %s: FixIssueForBranch = %d", tt.name, fix)
			}
		})
	}
}
