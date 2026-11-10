package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6272: a refused push is never forgiven by an older head of the same
// branch on the remote, is a terminal non-infra outcome, and ends in
// pilot-needs-human with one comment that never holds the value.

// pushRefusedFixture is a secretGateFixture whose branch pilot/GH-9 already
// exists on the remote (an earlier, clean push) and whose HEAD adds a
// registered secret value on top. A fake gh logs its argv and a fake PR creator
// records any PR creation.
type pushRefusedFixture struct {
	*secretGateFixture
	ghLog   string
	creator *fakePRCreatorGH4031
}

func newPushRefusedFixture(t *testing.T) *pushRefusedFixture {
	t.Helper()
	f := newSecretGateFixture(t)
	f.commit("main.go", "package main\n", "feat: first")
	f.run("push", "-u", "origin", "pilot/GH-9")
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commit("notes.md", "token = "+secretGateValue+"\n", "docs: notes")

	fakeBin := t.TempDir()
	ghLog := filepath.Join(t.TempDir(), "gh-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + ghLog + "\"\ncase \"$*\" in\n  *\"pr list\"*) echo '[]' ;;\n  *\"pr create\"*) echo https://github.com/o/r/pull/1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return &pushRefusedFixture{secretGateFixture: f, ghLog: ghLog, creator: &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/1"}}
}

func (f *pushRefusedFixture) task() *Task {
	return &Task{ID: "GH-9", Title: "add feature", Description: "d", Branch: "pilot/GH-9", BaseBranch: "main", CreatePR: true}
}

func (f *pushRefusedFixture) ghCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.ghLog)
	if err != nil {
		return ""
	}
	return string(b)
}

// assertNoPR fails when a PR was created by the fake creator or by `gh pr create`.
func (f *pushRefusedFixture) assertNoPR(t *testing.T, result *ExecutionResult) {
	t.Helper()
	if f.creator.capturedTitle != "" {
		t.Errorf("PR creator was called with %q after a refused push", f.creator.capturedTitle)
	}
	if strings.Contains(f.ghCalls(t), "pr create") {
		t.Errorf("gh pr create ran after a refused push:\n%s", f.ghCalls(t))
	}
	if result.PRUrl != "" {
		t.Errorf("PRUrl = %q after a refused push", result.PRUrl)
	}
}

// assertRefusedTerminal checks the outcome every site must produce.
func assertRefusedTerminal(t *testing.T, result *ExecutionResult) {
	t.Helper()
	if result.Success {
		t.Error("Success = true after a refused push")
	}
	if !strings.HasPrefix(result.Error, "push refused") {
		t.Errorf("Error = %q, want it to start with %q", result.Error, "push refused")
	}
	if strings.Contains(result.Error, "push failed") || strings.Contains(result.Error, "attempt(s)") {
		t.Errorf("Error = %q reads as a retried infra failure", result.Error)
	}
	if strings.Contains(result.Error, secretGateValue) {
		t.Errorf("Error holds the value: %q", result.Error)
	}
	if got := TerminalStatus(result); got != "needs_human" {
		t.Errorf("TerminalStatus = %q, want needs_human (never infra)", got)
	}
}

func TestPushRefused_EpicSiteDoesNotCreatePR(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	r.prCreator = f.creator
	result := &ExecutionResult{TaskID: "GH-9", Success: true, IsEpic: true}

	r.finalizeEpicBranchPR(context.Background(), f.task(), f.git, result, []string{"completed"})

	assertRefusedTerminal(t, result)
	f.assertNoPR(t, result)
	if !strings.Contains(f.ghCalls(t), "--add-label pilot-needs-human") {
		t.Errorf("issue did not get pilot-needs-human:\n%s", f.ghCalls(t))
	}
}

func TestPushRefused_DecomposedParentSiteDoesNotCreatePR(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	r.prCreator = f.creator
	result := &ExecutionResult{TaskID: "GH-9", Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), f.task(), f.git, result)

	assertRefusedTerminal(t, result)
	f.assertNoPR(t, result)
	// One gate run means one attempt: no retry, so no sleep between attempts.
	if len(f.sink.events) != 1 {
		t.Errorf("gate ran %d times, want 1 attempt", len(f.sink.events))
	}
}

func TestPushRefused_TimeoutSalvageSiteDoesNotCreatePR(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	r.prCreator = f.creator
	result := &ExecutionResult{TaskID: "GH-9"}
	var preserve bool

	r.pushAndCreatePRAfterTimeout(context.Background(), f.task(), f.git, result, nil, r.log, "", &preserve)

	assertRefusedTerminal(t, result)
	f.assertNoPR(t, result)
}

func TestPushRefused_HoldDoesNotReportBranchPushed(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-9"}
	var preserve bool

	r.holdPushedBranch(context.Background(), f.task(), f.git, result, r.log, "backend timeout", "", &preserve)

	if result.BranchPushed == nil || *result.BranchPushed {
		t.Fatalf("BranchPushed = %v, want false: the remote holds only the stale head", result.BranchPushed)
	}
	if !preserve {
		t.Error("worktree not preserved after a refused hold push")
	}
	if !strings.Contains(result.Error, "push refused") {
		t.Errorf("hold reason %q does not say the push was refused", result.Error)
	}
	if strings.Contains(result.Error, secretGateValue) {
		t.Errorf("hold reason holds the value: %q", result.Error)
	}
}

func TestPushRefused_PushBranchWithRetryIgnoresRemoteBranch(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := &Runner{}
	err := r.pushBranchWithRetry(context.Background(), f.git, f.task(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !IsPushRefused(err) {
		t.Fatalf("got %v, want a push refusal", err)
	}
	if len(f.sink.events) != 1 {
		t.Errorf("gate ran %d times, want 1", len(f.sink.events))
	}
}

// The comment names the commit and the file, never the value, and the
// execution is terminal-by-design: the dispatcher does not re-pick it.
func TestPushRefused_FailPushRefusedLabelsCommentsAndIsNotRepicked(t *testing.T) {
	f := newPushRefusedFixture(t)
	r := newSilentRunnerTask359()
	task := f.task()
	result := &ExecutionResult{TaskID: "GH-9"}
	err := CheckSecretGate(context.Background(), f.local, "pilot/GH-9", "GH-9")
	if !IsPushRefused(err) {
		t.Fatalf("setup: %v", err)
	}

	r.failPushRefused(context.Background(), task, f.git, result, r.log, err)

	assertRefusedTerminal(t, result)
	calls := f.ghCalls(t)
	if !strings.Contains(calls, "issue comment 9") || !strings.Contains(calls, "notes.md") {
		t.Errorf("no comment naming the file:\n%s", calls)
	}
	if !strings.Contains(calls, "--add-label pilot-needs-human") {
		t.Errorf("no pilot-needs-human label:\n%s", calls)
	}
	if strings.Contains(calls, secretGateValue) {
		t.Errorf("a gh call holds the value:\n%s", calls)
	}
	if !IsTerminalByDesignStatus(TerminalStatus(result)) {
		t.Error("needs_human must be terminal-by-design (no re-pick, no failure report)")
	}
	if !IsPermanentFailure(result.Error) {
		t.Error("IsPermanentFailure = false for a push refusal")
	}
}

func TestPushRefused_NotInfraWithoutOutcome(t *testing.T) {
	for _, errText := range []string{
		"push refused: commit abc123 adds a secret value in notes.md",
		"push refused: the push base could not be computed: no merge base",
		"push refused: commit abc123 adds a secret value in notes.md — recovery: branch=b recovery_ref=refs/pilot-recovery/GH-9",
	} {
		if got := TerminalStatus(&ExecutionResult{Error: errText}); got == "infra" || got == "failed" {
			t.Errorf("TerminalStatus(%q) = %q", errText, got)
		}
	}
	if got := TerminalStatus(&ExecutionResult{Error: "push failed after 3 attempt(s): x"}); got != "infra" {
		t.Errorf("an ordinary push failure must stay infra, got %q", got)
	}
}

func TestPushRefused_ForgivenessHelper(t *testing.T) {
	f := newPushRefusedFixture(t)
	ctx := context.Background()
	refused := gateRefusalForTest(t, f)
	if pushFailureForgiven(ctx, f.git, "pilot/GH-9", refused) {
		t.Error("a refusal was forgiven because the branch exists on the remote")
	}
	if !pushFailureForgiven(ctx, f.git, "pilot/GH-9", os.ErrClosed) {
		t.Error("an ordinary push error on a branch that exists on the remote must still be forgiven (GH-1389)")
	}
	if pushFailureForgiven(ctx, f.git, "pilot/no-such-branch", os.ErrClosed) {
		t.Error("forgiven although the branch is not on the remote")
	}
}

// gateRefusalForTest returns the gate's refusal for the fixture's HEAD.
func gateRefusalForTest(t *testing.T, f *pushRefusedFixture) error {
	t.Helper()
	err := CheckSecretGate(context.Background(), f.local, "pilot/GH-9", "GH-9")
	if !IsPushRefused(err) {
		t.Fatalf("setup: got %v, want a refusal", err)
	}
	return err
}
