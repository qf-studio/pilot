package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stackingAttemptRecorderBackend simulates a Claude Code invocation that, on
// every call, first records how many earlier attempts' files are still
// present in the working tree, then writes and commits a brand-new file
// named after the attempt number. It never removes an earlier attempt's
// file itself, so the recorded counts reveal whether something reset the
// tree between calls — the same "stacking" shape as stackingAttemptBackend
// (git_reset_gh4594_test.go's sibling), but instrumented so a worktree-mode
// test can assert the tree was NOT reset (unlike the direct-mode guard).
type stackingAttemptRecorderBackend struct {
	mu         sync.Mutex
	calls      int
	seenCounts []int
}

func (b *stackingAttemptRecorderBackend) Name() string      { return "stacking-attempt-recorder" }
func (b *stackingAttemptRecorderBackend) IsAvailable() bool { return true }

func (b *stackingAttemptRecorderBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()

	entries, err := os.ReadDir(opts.ProjectPath)
	if err != nil {
		return nil, err
	}
	existing := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "attempt_") {
			existing++
		}
	}
	b.mu.Lock()
	b.seenCounts = append(b.seenCounts, existing)
	b.mu.Unlock()

	fname := filepath.Join(opts.ProjectPath, fmt.Sprintf("attempt_%d.txt", n))
	if err := os.WriteFile(fname, []byte(fmt.Sprintf("attempt %d", n)), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", fmt.Sprintf("attempt %d", n)}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
		}
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

func (b *stackingAttemptRecorderBackend) counts() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]int, len(b.seenCounts))
	copy(out, b.seenCounts)
	return out
}

// TestRunner_QualityRetry_WorktreeMode_AttemptsStackAcrossRetries is the
// worktree-mode counterpart to
// TestRunner_QualityRetry_DirectMode_ResetsToCleanPreAttemptState
// (runner_quality_retry_reset_gh4594_test.go). GH-4594 subtask 2 added a
// pre-attempt hard-reset before every quality-gate retry, but ONLY for
// direct (non-worktree) mode — worktree mode was never part of the
// incident (every execution already gets its own throwaway worktree) and
// must keep its pre-existing "no reset between retries" behavior. This test
// fails if that gating is ever accidentally widened to worktree mode.
func TestRunner_QualityRetry_WorktreeMode_AttemptsStackAcrossRetries(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &stackingAttemptRecorderBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: true} // worktree mode: must behave exactly as before GH-4594
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &failingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-4594-4-worktree-retry",
		Title:       "force repeated quality-gate retries in worktree mode",
		Description: "gates always fail, so every allowed retry fires",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4594-4-worktree-retry",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The task ultimately fails (gates never pass) — we only care about what
	// each retry saw in the isolated worktree.
	result, _ := runner.Execute(ctx, task)
	if result == nil || result.Success {
		t.Fatalf("expected task failure (gates never pass), got %+v", result)
	}

	counts := backend.counts()
	if len(counts) < 2 {
		t.Fatalf("expected >=2 backend calls (initial + at least one quality retry), got %d", len(counts))
	}

	// counts[i] is how many earlier attempts' files were still present when
	// call i started: 0 before the first call, 1 before the second, etc. Any
	// value less than that index means something reset the worktree between
	// retries — the direct-mode-only behavior added by GH-4594 subtask 2 must
	// not have leaked into worktree mode.
	for i, c := range counts {
		if c != i {
			t.Errorf("REGRESSION: retry %d saw %d prior attempt files still present, want %d — worktree-mode retries must not be reset (that's direct-mode-only behavior)", i, c, i)
		}
	}
}

// TestRunner_WorktreeMode_TerminalFailure_LeavesProjectRepoUntouched is the
// worktree-mode counterpart to
// TestRunner_DirectMode_TerminalFailure_LeavesCloneClean
// (runner_terminal_failure_clean_gh4594_test.go). GH-4594 subtask 3 added a
// deferred hard-reset-to-HEAD cleanup for direct-mode terminal failures, but
// it's gated off entirely for worktree mode (preAttemptSHA stays empty
// there) because worktree isolation already handles this: every execution
// gets its own throwaway worktree, discarded by cleanupWorktree regardless
// of outcome. This test guards that the new direct-mode cleanup logic (and
// the branch-creation changes from subtasks 1 and its runner_decompose.go
// counterpart) never touch the real project repo in worktree mode — after a
// terminal quality-gate failure, the real repo must be exactly as it was
// before Execute ran.
func TestRunner_WorktreeMode_TerminalFailure_LeavesProjectRepoUntouched(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()
	git := NewGitOperations(localRepo)

	preAttemptSHA := strings.TrimSpace(headSHA(t, localRepo))
	preAttemptBranch, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}

	runner := NewRunnerWithBackend(&commitPlusStrayDirtBackend{})
	runner.config = &BackendConfig{UseWorktree: true} // worktree mode: no direct-mode cleanup should apply
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &terminallyFailingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-4594-4-worktree-terminal",
		Title:       "terminal quality-gate failure in worktree mode",
		Description: "gate fails with ShouldRetry=false; the failed attempt's commit+dirt live only in the throwaway worktree",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4594-4-worktree-terminal",
		CreatePR:    true,
	}

	execCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, _ := runner.Execute(execCtx, task)
	if result == nil || result.Success {
		t.Fatalf("expected terminal task failure, got %+v", result)
	}

	// The real project repo must be entirely untouched: still on its original
	// branch, at its original commit, with no trace of the failed attempt's
	// files — they only ever existed in the now-deleted worktree.
	if gotBranch, err := git.GetCurrentBranch(ctx); err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	} else if gotBranch != preAttemptBranch {
		t.Errorf("REGRESSION: project repo branch = %q, want unchanged %q (worktree mode must never check out the task branch in the real repo)", gotBranch, preAttemptBranch)
	}
	if got := strings.TrimSpace(headSHA(t, localRepo)); got != preAttemptSHA {
		t.Errorf("REGRESSION: project repo HEAD = %s, want unchanged %s — a failed worktree-mode attempt must never touch the real repo's history", got, preAttemptSHA)
	}
	for _, f := range []string{"change.txt", "version.go"} {
		if _, err := os.Stat(filepath.Join(localRepo, f)); !os.IsNotExist(err) {
			t.Errorf("REGRESSION: %s leaked into the real project repo (should only ever exist in the discarded worktree), stat err=%v", f, err)
		}
	}
	status := gitOutput(t, localRepo, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("REGRESSION: real project repo has uncommitted changes after worktree-mode terminal failure:\n%s", status)
	}
}
