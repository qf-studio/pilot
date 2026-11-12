package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// GH-6318: residuals from the review of the push secret gate follow-ups.

// A gate that cannot run stops the push after one attempt; the failure text
// reports that one attempt, not the retry limit.
func TestPushTaskBranch_GateUnavailableReportsTheRealAttemptCount(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit("main.go", "package main\n", "feat: code")
	stubGateCheck(t, gateBroken)
	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-9", Success: true}

	if r.pushTaskBranch(context.Background(), &Task{ID: "GH-9", Branch: "pilot/GH-9"}, f.git, result, r.log) {
		t.Fatal("pushTaskBranch continued although the gate could not run")
	}
	if want := "push failed after 1 attempt(s):"; !strings.HasPrefix(result.Error, want) {
		t.Errorf("Error = %q, want prefix %q", result.Error, want)
	}
	if strings.Contains(result.Error, fmt.Sprintf("after %d attempt(s)", gitPushRetryAttempts)) {
		t.Errorf("Error = %q claims the retry limit, the gate stopped the first attempt", result.Error)
	}
	if strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Error("the branch reached the remote although the gate could not run")
	}
}

// The direct-commit path names the task, so a second refusal of the same commit
// of the same task raises no second alert.
func TestPushRefused_DirectCommitDedupsTheAlertByTask(t *testing.T) {
	f := newPushRefusedFixture(t)
	f.run("checkout", "main")
	f.run("merge", "--ff-only", "pilot/GH-9")
	r := newSilentRunnerTask359()
	task := f.task()
	task.DirectCommit = true

	for i := 0; i < 2; i++ {
		result := &ExecutionResult{TaskID: task.ID, Success: true}
		if r.pushDirectCommit(context.Background(), task, f.git, result, r.log) {
			t.Fatalf("run %d: pushDirectCommit continued after a refusal", i)
		}
	}
	if len(f.sink.events) != 1 {
		t.Fatalf("%d alerts for one task and one commit, want 1", len(f.sink.events))
	}
	if got := f.sink.events[0].TaskID; got != task.ID {
		t.Errorf("alert TaskID = %q, want %q", got, task.ID)
	}
}
