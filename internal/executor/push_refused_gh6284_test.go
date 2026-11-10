package executor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6284: follow-ups from the review of the push secret gate gap fixes.

// stubGateCheck replaces the gate's scan for the test.
func stubGateCheck(t *testing.T, fn func(ctx context.Context, dir, head string, opts secretpatterns.PushOptions) error) {
	t.Helper()
	prev := checkPushForGate
	checkPushForGate = fn
	t.Cleanup(func() { checkPushForGate = prev })
}

func gateBroken(context.Context, string, string, secretpatterns.PushOptions) error {
	return errors.New("git exploded")
}

// A cancelled context ends the gate with the context error: not a refusal, no
// alert, and the failure is a *SecretGateError the callers retry or fail on.
func TestSecretGate_CancelledContextIsNotARefusal(t *testing.T) {
	f := newPushRefusedFixture(t) // HEAD holds a registered value: a live scan would refuse
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CheckSecretGate(ctx, f.local, "pilot/GH-9", "GH-9")
	if err == nil {
		t.Fatal("cancelled context: gate passed")
	}
	if IsPushRefused(err) {
		t.Fatalf("a cancelled context became a refusal: %v", err)
	}
	if !errors.Is(err, context.Canceled) || !IsSecretGateError(err) {
		t.Errorf("got %v, want a SecretGateError wrapping context.Canceled", err)
	}
	if len(f.sink.events) != 0 {
		t.Errorf("raised %d alerts for a cancelled context", len(f.sink.events))
	}
}

// A gate that could not run is not forgiven by a head already on the remote.
func TestPushRefused_GateErrorIsNotForgiven(t *testing.T) {
	f := newPushRefusedFixture(t)
	ctx := context.Background()
	gateErr := &SecretGateError{Err: errors.New("git exploded")}
	if pushFailureForgiven(ctx, f.git, "pilot/GH-9", gateErr) {
		t.Error("a gate error was forgiven because the branch exists on the remote")
	}
	if !pushFailureForgiven(ctx, f.git, "pilot/GH-9", errors.New("failed to push: exit status 128")) {
		t.Error("a real push transport error must still be forgiven (GH-1389)")
	}
}

// The hold, epic, decompose and timeout-salvage sites fail on a gate error
// although the branch exists on the remote (a stale head).
func TestPushRefused_GateErrorFailsTheTaskAtForgivingSites(t *testing.T) {
	t.Run("epic", func(t *testing.T) {
		f := newPushRefusedFixture(t)
		stubGateCheck(t, gateBroken)
		r := newSilentRunnerTask359()
		r.prCreator = f.creator
		result := &ExecutionResult{TaskID: "GH-9", Success: true, IsEpic: true}

		r.finalizeEpicBranchPR(context.Background(), f.task(), f.git, result, []string{"completed"})

		if result.Success || !strings.Contains(result.Error, "epic branch push failed") {
			t.Errorf("Success=%v Error=%q, want a failed push", result.Success, result.Error)
		}
		f.assertNoPR(t, result)
	})
	t.Run("decompose", func(t *testing.T) {
		f := newPushRefusedFixture(t)
		stubGateCheck(t, gateBroken)
		r := newSilentRunnerTask359()
		r.prCreator = f.creator
		result := &ExecutionResult{TaskID: "GH-9", Success: true, CommitSHA: "placeholder"}

		r.finalizeDecomposedParentPR(context.Background(), f.task(), f.git, result)

		if result.Success || result.Error == "" {
			t.Errorf("Success=%v Error=%q, want a failed push", result.Success, result.Error)
		}
		f.assertNoPR(t, result)
	})
	t.Run("hold", func(t *testing.T) {
		f := newPushRefusedFixture(t)
		stubGateCheck(t, gateBroken)
		r := newSilentRunnerTask359()
		result := &ExecutionResult{TaskID: "GH-9"}
		var preserve bool

		r.holdPushedBranch(context.Background(), f.task(), f.git, result, r.log, "backend timeout", "", &preserve)

		if result.BranchPushed == nil || *result.BranchPushed {
			t.Errorf("BranchPushed = %v, want false: the remote holds a stale head", result.BranchPushed)
		}
		if !preserve {
			t.Error("worktree not preserved after a failed hold push")
		}
	})
	t.Run("timeout salvage", func(t *testing.T) {
		f := newPushRefusedFixture(t)
		stubGateCheck(t, gateBroken)
		r := newSilentRunnerTask359()
		r.prCreator = f.creator
		result := &ExecutionResult{TaskID: "GH-9"}
		var preserve bool

		r.pushAndCreatePRAfterTimeout(context.Background(), f.task(), f.git, result, nil, r.log, "", &preserve)

		if result.PRUrl != "" {
			t.Errorf("PRUrl = %q on a stale remote head", result.PRUrl)
		}
		f.assertNoPR(t, result)
	})
}

// The lease push over an inherited head must surface the gate's refusal, not
// escalate it as a non-fast-forward.
func TestPushRefused_LeaseOverInheritedHeadSurfacesRefusal(t *testing.T) {
	f := newPushRefusedFixture(t)
	// Make the local branch diverge from the remote one: rewrite the first
	// commit, so a plain push is a real non-fast-forward rejection.
	f.run("reset", "--hard", "HEAD~1")
	f.run("commit", "--amend", "-m", "feat: first, rewritten")
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	remoteHead := f.run("--git-dir", f.remote, "rev-parse", "pilot/GH-9")

	// The first push passes the gate and is rejected by git; the lease push is
	// the call the gate refuses.
	var mu sync.Mutex
	calls := 0
	stubGateCheck(t, func(ctx context.Context, dir, head string, opts secretpatterns.PushOptions) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return nil
		}
		return &secretpatterns.PushRangeError{Commit: "abc123", File: "notes.md", Kind: "x"}
	})
	task := f.task()
	task.inheritedHeadSHA = remoteHead

	r := &Runner{}
	err := r.pushBranchWithRetry(context.Background(), f.git, task, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if !IsPushRefused(err) {
		t.Fatalf("got %v, want the lease push's refusal", err)
	}
	if got := f.run("--git-dir", f.remote, "rev-parse", "pilot/GH-9"); got != remoteHead {
		t.Errorf("remote head moved to %s", got)
	}
}

// The main PR path: a refusal of the branch push parks the task needs_human, no
// infra failure and no PR. Removing the IsPushRefused block of pushTaskBranch
// turns this into a "push failed after N attempt(s)" failure.
func TestPushRefused_MainPRPathParksTheTask(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	r.prCreator = f.creator
	result := &ExecutionResult{TaskID: "GH-9", Success: true}

	if r.pushTaskBranch(context.Background(), f.task(), f.git, result, r.log) {
		t.Fatal("pushTaskBranch continued after a refusal")
	}

	assertRefusedTerminal(t, result)
	f.assertNoPR(t, result)
	if !strings.Contains(f.ghCalls(t), "--add-label pilot-needs-human") {
		t.Errorf("issue did not get pilot-needs-human:\n%s", f.ghCalls(t))
	}
	if len(f.sink.events) != 1 {
		t.Errorf("gate ran %d times, want 1 (a refusal is not retried)", len(f.sink.events))
	}
}

// A clean push on the main PR path continues.
func TestPushRefused_MainPRPathCleanPushContinues(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit("main.go", "package main\n", "feat: code")
	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-9", Success: true}

	if !r.pushTaskBranch(context.Background(), &Task{ID: "GH-9", Branch: "pilot/GH-9"}, f.git, result, r.log) {
		t.Fatalf("clean push ended the run: %q", result.Error)
	}
	if !strings.Contains(f.remoteBranches(), "pilot/GH-9") {
		t.Error("branch did not reach the remote")
	}
}

// The PushToMain path: a refused default-branch push parks the task and leaves
// remote main alone. Removing the IsPushRefused block of pushDirectCommit turns
// this into a "push to main failed" failure.
func TestPushRefused_DirectCommitPathParksTheTask(t *testing.T) {
	f := newPushRefusedFixture(t)
	f.run("checkout", "main")
	f.run("merge", "--ff-only", "pilot/GH-9")
	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-9", Success: true}
	task := f.task()
	task.DirectCommit = true

	if r.pushDirectCommit(context.Background(), task, f.git, result, r.log) {
		t.Fatal("pushDirectCommit continued after a refusal")
	}

	assertRefusedTerminal(t, result)
	if strings.Contains(result.Error, "push to main failed") {
		t.Errorf("Error = %q reads as a plain push failure", result.Error)
	}
	if got := f.run("--git-dir", f.remote, "rev-list", "--count", "main"); got != "1" {
		t.Errorf("remote main has %s commits, want 1", got)
	}
}

// The comment follows the cause (GH-6284).
func TestPushRefused_CommentFollowsTheCause(t *testing.T) {
	secretErr := &secretpatterns.PushRangeError{Commit: "abc123", File: "notes.md", Kind: "x"}
	scanErr := &secretpatterns.PushScanError{Reason: "the push base could not be computed: no merge base"}
	newPR := &Task{ID: "GH-9", Branch: "pilot/GH-9"}
	continued := &Task{ID: "GH-9", Branch: "pilot/GH-9", FromPR: 12}

	body := refusalComment(newPR, secretErr, "refs/pilot-recovery/GH-9")
	for _, want := range []string{"abc123", "notes.md", "Remove the secret", "rotate", "no PR was opened", "refs/pilot-recovery/GH-9"} {
		if !strings.Contains(body, want) {
			t.Errorf("secret comment lacks %q:\n%s", want, body)
		}
	}

	body = refusalComment(newPR, scanErr, "")
	for _, want := range []string{"base could not be computed", "No secret was found", "no PR was opened"} {
		if !strings.Contains(body, want) {
			t.Errorf("scan comment lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"Remove the secret", "rotate"} {
		if strings.Contains(body, bad) {
			t.Errorf("scan comment says %q although no secret was found:\n%s", bad, body)
		}
	}

	for _, err := range []error{secretErr, scanErr} {
		if body := refusalComment(continued, err, ""); strings.Contains(body, "no PR was opened") {
			t.Errorf("comment for a task that continues PR #12 says no PR was opened:\n%s", body)
		}
	}
}

// One alert per refused commit per task: the auto-preserve push and the final
// push of the same task must not both alert (GH-6284).
func TestSecretGate_OneAlertPerCommitPerTask(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := CheckSecretGate(ctx, f.local, "pilot/GH-9", "GH-9"); !IsPushRefused(err) {
			t.Fatalf("run %d: got %v, want a refusal", i, err)
		}
	}
	if len(f.sink.events) != 1 {
		t.Fatalf("same task, same commit: %d alerts, want 1", len(f.sink.events))
	}
	if err := CheckSecretGate(ctx, f.local, "pilot/GH-9", "GH-10"); !IsPushRefused(err) {
		t.Fatalf("other task: got %v", err)
	}
	if len(f.sink.events) != 2 {
		t.Errorf("another task refusing the same commit: %d alerts, want 2", len(f.sink.events))
	}
	// A call without a task ID (autopilot) is not deduplicated.
	for i := 0; i < 2; i++ {
		_ = CheckSecretGate(ctx, f.local, "pilot/GH-9", "")
	}
	if len(f.sink.events) != 4 {
		t.Errorf("calls without a task ID: %d alerts in total, want 4", len(f.sink.events))
	}
}

// The real sequence: the dirty-worktree auto-preserve push is refused, then the
// final push of the same task is refused for the same commit. One alert.
func TestSecretGate_AutoPreserveThenFinalPushAlertsOnce(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("main.go", "package main\n", "feat: code")
	if err := os.WriteFile(filepath.Join(f.local, "notes.md"), []byte("token = "+secretGateValue+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: "GH-9", Branch: "pilot/GH-9"}
	r := newSilentRunnerTask359()

	if _, preserved := preserveDirtyWorktreeAsWIP(context.Background(), f.git, task, r.log); !preserved {
		t.Fatal("dirty worktree was not preserved")
	}
	result := &ExecutionResult{TaskID: "GH-9", Success: true}
	if r.pushTaskBranch(context.Background(), task, f.git, result, r.log) {
		t.Fatal("the final push of a refused commit went through")
	}

	if len(f.sink.events) != 1 {
		t.Errorf("%d alerts for one refused commit, want 1", len(f.sink.events))
	}
}
