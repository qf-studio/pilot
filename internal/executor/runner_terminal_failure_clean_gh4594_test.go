package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// commitPlusStrayDirtBackend simulates the exact GH-4594 incident shape: the
// attempt does real, legitimate work (a committed file — so the "no commits
// at all" auto-preserve safety net, GH-4517, never fires) but also leaves an
// extra file it never staged or committed — the "leftover ` M version.go`
// observed x3" symptom from the incident report, here surviving all the way
// to a terminal (non-retryable) quality-gate failure.
type commitPlusStrayDirtBackend struct{}

func (b *commitPlusStrayDirtBackend) Name() string      { return "commit-plus-stray-dirt" }
func (b *commitPlusStrayDirtBackend) IsAvailable() bool { return true }

func (b *commitPlusStrayDirtBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	committedFile := filepath.Join(opts.ProjectPath, "change.txt")
	if err := os.WriteFile(committedFile, []byte("real committed change"), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "change.txt"}, {"commit", "-m", "real change"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
		}
	}

	// Leftover dirt: never staged, never committed.
	strayFile := filepath.Join(opts.ProjectPath, "version.go")
	if err := os.WriteFile(strayFile, []byte("partial stray edit"), 0o644); err != nil {
		return nil, err
	}

	return &BackendResult{Success: true, Output: "done"}, nil
}

// terminallyFailingQualityChecker fails without requesting a retry, so the
// quality-gate loop exits on its very first pass via the "no more retries
// allowed" path (runner.go's finalOutcome branch) rather than the
// quality-gate retry-reset loop already covered by
// TestRunner_QualityRetry_DirectMode_ResetsToCleanPreAttemptState.
type terminallyFailingQualityChecker struct{}

func (c *terminallyFailingQualityChecker) Check(_ context.Context) (*QualityOutcome, error) {
	return &QualityOutcome{
		Passed:        false,
		ShouldRetry:   false,
		RetryFeedback: "synthetic terminal gate failure",
		Attempt:       1,
	}, nil
}

// TestRunner_DirectMode_TerminalFailure_LeavesCloneClean is the GH-4594
// regression guard for the final leg of the incident: after a direct-mode
// (non-worktree) execution fails for good — no quality-gate retries left —
// the shared clone must be left clean, with no leftover uncommitted dirt.
// Before this fix, only the quality-gate *retry* loop reset the tree
// (GH-4594 subtask 2); the terminal-failure return path left whatever
// uncommitted dirt the failed attempt wrote behind, so the *next* dispatch
// on this same project's shared clone tripped the git_clean preflight check.
//
// The legitimate commit the attempt made must survive the cleanup —
// discarding a human-reviewable commit just to satisfy git_clean would be
// its own regression (it's exactly what the quality-gate retry loop's
// "keep the last attempt's commit" behavior and GH-4517's auto-preserved WIP
// commit both depend on).
func TestRunner_DirectMode_TerminalFailure_LeavesCloneClean(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	preAttemptSHA := strings.TrimSpace(headSHA(t, localRepo))

	runner := NewRunnerWithBackend(&commitPlusStrayDirtBackend{})
	runner.config = &BackendConfig{UseWorktree: false} // direct mode: no worktree isolation
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &terminallyFailingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-4594-3",
		Title:       "terminal quality-gate failure leaves uncommitted dirt",
		Description: "gate fails with ShouldRetry=false; no retry-loop reset ever fires",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4594-3",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, _ := runner.Execute(ctx, task)
	if result == nil || result.Success {
		t.Fatalf("expected terminal task failure, got %+v", result)
	}

	// The never-committed stray file must be gone.
	if _, err := os.Stat(filepath.Join(localRepo, "version.go")); !os.IsNotExist(err) {
		t.Errorf("version.go stray dirt should have been discarded after terminal failure, stat err=%v", err)
	}

	// The legitimate commit must survive: HEAD is ahead of the pre-attempt
	// baseline by exactly one commit, and its file is still present.
	if got := strings.TrimSpace(headSHA(t, localRepo)); got == preAttemptSHA {
		t.Errorf("HEAD unchanged at pre-attempt SHA %s — the attempt's legitimate commit must not be discarded", preAttemptSHA)
	}
	if _, err := os.Stat(filepath.Join(localRepo, "change.txt")); err != nil {
		t.Errorf("change.txt from the legitimate commit should survive cleanup, stat err=%v", err)
	}
	countOut := gitOutput(t, localRepo, "rev-list", "--count", preAttemptSHA+"..HEAD")
	if got := strings.TrimSpace(countOut); got != "1" {
		t.Errorf("commits ahead of pre-attempt baseline = %s, want 1 (the legitimate commit)", got)
	}

	// The clone must be fully clean.
	status := gitOutput(t, localRepo, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("working tree should be clean after terminal failure, got:\n%s", status)
	}

	// GH-4594: the actual regression this guards against — a subsequent
	// dispatch's git_clean preflight check on the same (shared, non-worktree)
	// clone must not fail because of this attempt's leftovers.
	if err := checkGitClean(ctx, localRepo); err != nil {
		t.Errorf("next dispatch's git_clean preflight check failed: %v", err)
	}
}
