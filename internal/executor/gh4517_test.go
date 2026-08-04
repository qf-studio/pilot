package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestApplyGhostSHAGuard_DirtyWorktreeAutoPreserved is the AC1/AC2
// table-driven test for the GH-4517 harvester backstop: a worktree the
// ghost-SHA guard is about to classify no_op — and which the runner's
// deferred worktree cleanup would then delete — must not be silently
// discarded when it still has uncommitted work.
//
// Incident: pilot-console#26 (B8), 2026-07-23 — a 44-minute session did
// real, test-passing work and never ran `git commit`; the harvested SHA
// turned out to be a ghost (already on origin/main), so the run was
// classified no_op and the worktree deleted, destroying the work.
func TestApplyGhostSHAGuard_DirtyWorktreeAutoPreserved(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		dirty         bool
		wantPreserved bool
	}{
		{
			name:          "dirty worktree (new + modified files) is auto-committed and pushed, not classified no_op",
			dirty:         true,
			wantPreserved: true,
		},
		{
			name:          "clean worktree still classifies no_op exactly as today",
			dirty:         false,
			wantPreserved: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, bareDir := setupFreshnessRepo(t)

			// Seed a tracked file on main so the dirty case has something to
			// modify (setupFreshnessRepo's initial commit is --allow-empty).
			seedPath := filepath.Join(repoDir, "existing.txt")
			if err := os.WriteFile(seedPath, []byte("v1\n"), 0o644); err != nil {
				t.Fatalf("write seed file: %v", err)
			}
			runGit(t, repoDir, "add", "existing.txt")
			runGit(t, repoDir, "commit", "-m", "feat: seed file")
			runGit(t, repoDir, "push", "origin", "main")
			seedSHA := strings.TrimSpace(gitOutput(t, repoDir, "rev-parse", "HEAD"))

			branch := fmt.Sprintf("pilot/GH-4517-%v", tc.dirty)
			runGit(t, repoDir, "checkout", "-b", branch)

			if tc.dirty {
				// Modified tracked file + new untracked file — matches AC1's
				// "new and modified files".
				if err := os.WriteFile(seedPath, []byte("v2\n"), 0o644); err != nil {
					t.Fatalf("modify seed file: %v", err)
				}
				if err := os.WriteFile(filepath.Join(repoDir, "new_work.go"), []byte("package x\n"), 0o644); err != nil {
					t.Fatalf("write new file: %v", err)
				}
			}

			var logBuf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			task := &Task{ID: "GH-4517T", Branch: branch, BaseBranch: "main"}
			// seedSHA is already on origin/main — the ghost-SHA scenario: the
			// harvester "recovered" a SHA that turns out to be stale.
			result := &ExecutionResult{TaskID: task.ID, CommitSHA: seedSHA, Success: true}

			preserved := applyGhostSHAGuard(ctx, task, result, repoDir, log)

			if preserved != tc.wantPreserved {
				t.Fatalf("applyGhostSHAGuard preserved = %v, want %v (result: %+v)", preserved, tc.wantPreserved, result)
			}

			// Regardless of outcome, a no-op-classified run must never be
			// reported as completed.
			if result.Success {
				t.Error("result.Success must be false — not a completed classification")
			}

			if tc.wantPreserved {
				if result.CommitSHA == "" || result.CommitSHA == seedSHA {
					t.Errorf("expected a new commit SHA distinct from the ghost SHA, got %q", result.CommitSHA)
				}
				if strings.Contains(result.Error, "no new commit produced") {
					t.Errorf("preserved result must not carry the no_op error text, got %q", result.Error)
				}
				status := TerminalStatus(result)
				if status == "no_op" || status == "completed" {
					t.Errorf("TerminalStatus = %q, want neither no_op nor completed (failed-with-artifact)", status)
				}

				// AC1: worktree cleanup only after the push succeeds — verify
				// the commit actually landed on origin's branch, not just
				// locally.
				out := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch)
				if !strings.Contains(out, "auto-preserved") {
					t.Errorf("expected pushed commit message to mention auto-preserved, got %q", out)
				}

				// AC3: a WARN log is emitted.
				if !strings.Contains(logBuf.String(), "auto-preserved") {
					t.Error("expected a WARN log mentioning auto-preserved")
				}
			} else {
				// AC2: clean-tree no-commit case is byte-identical to the
				// pre-GH-4517 behavior.
				if result.CommitSHA != "" {
					t.Errorf("clean-tree case: CommitSHA should be cleared, got %q", result.CommitSHA)
				}
				if result.Error != "no new commit produced — worktree HEAD matches base branch parent" {
					t.Errorf("clean-tree case: unexpected error %q", result.Error)
				}
				if TerminalStatus(result) != "no_op" {
					t.Errorf("clean-tree case: TerminalStatus = %q, want no_op", TerminalStatus(result))
				}
			}
		})
	}
}

// TestApplyGhostSHAGuardWithPreserve_RecordsExecutionEvent covers AC3: the
// auto-preserve path must emit an execution_events row (StageWorkPreserved)
// so it's visible in `pilot trace` / the dashboard, not just a log line.
func TestApplyGhostSHAGuardWithPreserve_RecordsExecutionEvent(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := setupFreshnessRepo(t)

	seedPath := filepath.Join(repoDir, "existing.txt")
	if err := os.WriteFile(seedPath, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGit(t, repoDir, "add", "existing.txt")
	runGit(t, repoDir, "commit", "-m", "feat: seed file")
	runGit(t, repoDir, "push", "origin", "main")
	seedSHA := strings.TrimSpace(gitOutput(t, repoDir, "rev-parse", "HEAD"))

	branch := "pilot/GH-4517WORK"
	runGit(t, repoDir, "checkout", "-b", branch)
	if err := os.WriteFile(seedPath, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("modify seed file: %v", err)
	}

	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	execID := "exec-gh4517"
	if err := store.SaveExecution(&memory.Execution{
		ID:          execID,
		TaskID:      "GH-4517WORK",
		ProjectPath: repoDir,
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	runner := NewRunner()
	runner.SetLogStore(store)

	task := &Task{ID: "GH-4517WORK", ExecutionID: execID, Branch: branch, BaseBranch: "main"}
	result := &ExecutionResult{TaskID: task.ID, CommitSHA: seedSHA, Success: true}

	runner.applyGhostSHAGuardWithPreserve(ctx, task, result, repoDir, slog.Default())

	if result.Success {
		t.Fatal("expected Success=false after auto-preserve")
	}
	if result.CommitSHA == "" || result.CommitSHA == seedSHA {
		t.Fatalf("expected a new preserved commit SHA, got %q", result.CommitSHA)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Stage == memory.StageWorkPreserved {
			found = true
			if !strings.Contains(e.Detail, "auto-preserved") {
				t.Errorf("event detail = %q, want mention of auto-preserved", e.Detail)
			}
		}
	}
	if !found {
		t.Errorf("expected a %q execution event, got stages: %+v", memory.StageWorkPreserved, events)
	}
}

// mockDirtyBackend simulates the exact incident behavior (pilot-console#26 /
// B8): the model edits files on disk and reports success, but never runs
// `git commit`. Used to exercise the runner's other no-op classification
// site — the GH-916 "no commits after retry" path in Execute — end to end.
// It records the ProjectPath from each call — GH-4708 (TASK-441 L1): a mock
// whose Execute discards its arguments certifies nothing about the seam it
// stands in for.
type mockDirtyBackend struct {
	mu             sync.Mutex
	dir            string
	execCount      int
	gotProjectPath string
}

func (m *mockDirtyBackend) Name() string      { return "mock-dirty" }
func (m *mockDirtyBackend) IsAvailable() bool { return true }
func (m *mockDirtyBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCount++
	m.gotProjectPath = opts.ProjectPath
	content := fmt.Sprintf("package x // attempt %d\n", m.execCount)
	if err := os.WriteFile(filepath.Join(m.dir, "implementation.go"), []byte(content), 0o644); err != nil {
		return nil, err
	}
	return &BackendResult{Success: true, Output: "implementation complete"}, nil
}

// TestRunner_DirtyWorktreeAfterNoCommitRetry_AutoPreserved covers the GH-916
// no-commit-after-retry path (runner.go, "No-commit detection and retry"):
// when the backend leaves real, uncommitted files on disk across both the
// initial attempt and the retry, the run must not be silently classified
// no_op — the dirty state must be auto-committed and pushed instead.
func TestRunner_DirtyWorktreeAfterNoCommitRetry_AutoPreserved(t *testing.T) {
	const branch = "pilot/GH-4517RETRY"
	dir, bareDir := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockDirtyBackend{dir: dir}
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	task := &Task{
		ID:          "GH-4517RETRY",
		Title:       "fix(executor): dirty worktree after no-commit retry",
		Description: "model edits files but never commits",
		ProjectPath: dir,
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Success {
		t.Error("expected Success=false — not a completed classification")
	}
	if strings.HasPrefix(result.Error, "no_changes:") {
		t.Errorf("expected auto-preserve, not the no_changes/no_op classification, got: %q", result.Error)
	}
	if !strings.Contains(result.Error, "auto-preserved") {
		t.Errorf("expected result.Error to mention auto-preserved, got: %q", result.Error)
	}
	if result.CommitSHA == "" {
		t.Error("expected a preserved commit SHA")
	}
	if status := TerminalStatus(result); status == "no_op" || status == "completed" {
		t.Errorf("TerminalStatus = %q, want neither no_op nor completed", status)
	}

	// Verify the commit actually landed on origin's branch (AC1: cleanup only
	// after push succeeds).
	out := gitOutput(t, bareDir, "log", "-1", "--format=%s", "refs/heads/"+branch)
	if !strings.Contains(out, "auto-preserved") {
		t.Errorf("expected pushed commit on origin, got log: %q", out)
	}

	backend.mu.Lock()
	count := backend.execCount
	gotProjectPath := backend.gotProjectPath
	backend.mu.Unlock()
	if count != 2 {
		t.Errorf("expected backend called 2 times (initial + retry), got %d", count)
	}
	if gotProjectPath != dir {
		t.Errorf("backend received ProjectPath %q, want %q", gotProjectPath, dir)
	}
}
