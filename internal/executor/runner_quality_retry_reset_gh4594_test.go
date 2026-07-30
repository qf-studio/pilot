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

// stackingAttemptBackend simulates a Claude Code invocation that, on every
// call, writes and commits a brand-new file named after the attempt number.
// It never touches a previous attempt's file — exactly the shape of a "fresh
// session with no memory of the last rejected attempt" retry.
type stackingAttemptBackend struct {
	mu    sync.Mutex
	calls int
}

func (b *stackingAttemptBackend) Name() string      { return "stacking-attempt" }
func (b *stackingAttemptBackend) IsAvailable() bool { return true }

func (b *stackingAttemptBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
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

func (b *stackingAttemptBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// TestRunner_QualityRetry_DirectMode_ResetsToCleanPreAttemptState is the
// GH-4594 regression guard for the direct-mode (non-worktree) quality-gate
// retry loop: the reported incident showed leftover ` M version.go` diffs
// stacking across three retries because each retry's edits landed on top of
// the previous (rejected) attempt's leftovers instead of a clean slate.
//
// The quality gate here is rigged to always fail, forcing the maximum
// number of retries. Each simulated attempt commits a distinctly-named file
// so a stacking bug is unambiguous: without the pre-attempt reset, every
// attempt's file and commit survives; with it, only the last attempt's file
// and commit remain, and the working tree ends up clean.
func TestRunner_QualityRetry_DirectMode_ResetsToCleanPreAttemptState(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	preAttemptSHA := strings.TrimSpace(headSHA(t, localRepo))

	backend := &stackingAttemptBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false} // direct mode: no worktree isolation
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &failingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-4594-2",
		Title:       "force repeated quality-gate retries in direct mode",
		Description: "gates always fail, so every allowed retry fires",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4594-2",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The task ultimately fails (gates never pass) — we only care about the
	// git state the retry loop leaves behind in the shared direct-mode clone.
	result, _ := runner.Execute(ctx, task)
	if result == nil || result.Success {
		t.Fatalf("expected task failure (gates never pass), got %+v", result)
	}

	calls := backend.callCount()
	if calls < 2 {
		t.Fatalf("expected >=2 backend calls (initial + at least one quality retry), got %d", calls)
	}

	// Exactly one commit should separate the pre-attempt baseline from HEAD:
	// the last attempt's. Every earlier (rejected) attempt's commit must have
	// been discarded by the reset before the next retry ran.
	countOut := gitOutput(t, localRepo, "rev-list", "--count", preAttemptSHA+"..HEAD")
	if got := strings.TrimSpace(countOut); got != "1" {
		t.Errorf("commits ahead of pre-attempt baseline = %s, want 1 (earlier rejected attempts must not stack)", got)
	}

	// Only the LAST attempt's file should exist; every earlier attempt's file
	// is leftover dirt from a rejected try and must have been removed.
	entries, err := os.ReadDir(localRepo)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var attemptFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "attempt_") {
			attemptFiles = append(attemptFiles, e.Name())
		}
	}
	wantFile := fmt.Sprintf("attempt_%d.txt", calls)
	if len(attemptFiles) != 1 || attemptFiles[0] != wantFile {
		t.Errorf("attempt files present = %v, want exactly [%s] (earlier attempts' leftovers must be discarded)", attemptFiles, wantFile)
	}

	// The clone must be clean — no leftover modified/untracked files — so the
	// next dispatch's git_clean preflight check doesn't wedge (GH-4594).
	status := gitOutput(t, localRepo, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("working tree should be clean after the retry loop, got:\n%s", status)
	}
}
