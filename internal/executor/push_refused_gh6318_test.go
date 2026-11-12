package executor

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
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

// The lease push counts as a push: the plain push ran and was rejected, the
// lease push then stopped at the gate, and the text says 2, not 1.
func TestPushTaskBranch_AttemptCountIncludesTheLeasePush(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit("a.go", "package a\n", "feat: a")
	f.run("push", "origin", "pilot/GH-9")
	wip := f.run("rev-parse", "HEAD")
	f.run("reset", "--hard", "HEAD^")
	f.commit("b.go", "package b\n", "feat: b") // the branch no longer fast-forwards the remote head

	var gateCalls atomic.Int32
	stubGateCheck(t, func(context.Context, string, string, secretpatterns.PushOptions) error {
		if gateCalls.Add(1) == 1 {
			return nil // the plain push passes the gate and is rejected by the remote
		}
		return gateBroken(context.Background(), "", "", secretpatterns.PushOptions{})
	})
	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-9", Success: true}
	task := &Task{ID: "GH-9", Branch: "pilot/GH-9", pushedWIPSHA: wip}

	if r.pushTaskBranch(context.Background(), task, f.git, result, r.log) {
		t.Fatal("pushTaskBranch continued although the lease push could not clear the gate")
	}
	if got := gateCalls.Load(); got != 2 {
		t.Fatalf("setup: the gate ran %d times, want 2 (plain push, then lease push)", got)
	}
	if want := "push failed after 2 attempt(s):"; !strings.HasPrefix(result.Error, want) {
		t.Errorf("Error = %q, want prefix %q", result.Error, want)
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

// The gate names the ref when its context ended before it could resolve the
// head. A ref name is no dedup key: it stands for different commits over time.
func TestSecretGate_RefNameIsNotADedupKey(t *testing.T) {
	ResetSecretGateConfigForTest()
	t.Cleanup(ResetSecretGateConfigForTest)

	const sha = "0123456789ab"
	for _, tc := range []struct {
		name, task, commit string
		want               []bool
	}{
		{"short sha dedups", "GH-1", sha, []bool{true, false}},
		{"full sha dedups", "GH-2", strings.Repeat("ab", 20), []bool{true, false}},
		{"ref name alerts every time", "GH-3", "pilot/GH-3", []bool{true, true}},
		{"short ref alerts every time", "GH-4", "main", []bool{true, true}},
		{"no task alerts every time", "", sha, []bool{true, true}},
	} {
		for i, want := range tc.want {
			if got := firstAlertFor(tc.task, tc.commit); got != want {
				t.Errorf("%s: call %d = %v, want %v", tc.name, i+1, got, want)
			}
		}
	}
}
