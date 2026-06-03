package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSilentRunnerTask359() *Runner {
	return &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// initRepoWithCommitTask359 creates a git repo on `main` with one commit and
// returns its path. No remote is configured, so pushes will fail — which is the
// point for the push-failure contract test.
func initRepoWithCommitTask359(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

// TestFinalizeEpicBranchPR_PushFailIsFailure is the Shape A regression guard:
// a parent branch with real commits that fails to push (no reachable remote)
// MUST mark the epic as failed — not warn-and-continue with Success=true.
func TestFinalizeEpicBranchPR_PushFailIsFailure(t *testing.T) {
	dir := initRepoWithCommitTask359(t)
	runGit(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-m", "feature work")

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-1", Success: true, IsEpic: true}
	task := &Task{ID: "GH-1", Title: "feat: add f", Description: "d", Branch: "feature", BaseBranch: "main", CreatePR: true}

	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result)

	if result.Success {
		t.Error("expected Success=false when epic push fails with no remote")
	}
	if !strings.Contains(result.Error, "push failed") {
		t.Errorf("expected push-failed error, got %q", result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL on push failure, got %q", result.PRUrl)
	}
}

// TestFinalizeEpicBranchPR_NoCommitsIsCleanSuccess verifies the reordered
// guard: a parent branch with no commits vs base (deliverables shipped via
// child PRs) is a legitimate success — PR creation is skipped, Success is left
// untouched, and no foreign SHA is harvested.
func TestFinalizeEpicBranchPR_NoCommitsIsCleanSuccess(t *testing.T) {
	dir := initRepoWithCommitTask359(t)

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-2", Success: true, IsEpic: true}
	task := &Task{ID: "GH-2", Title: "epic", Description: "d", Branch: "main", BaseBranch: "main", CreatePR: true}

	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result)

	if !result.Success {
		t.Errorf("expected Success=true for empty parent branch, error=%q", result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL, got %q", result.PRUrl)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
	if result.CommitSHA != "" {
		t.Errorf("expected no harvested SHA when guard skips, got %q", result.CommitSHA)
	}
}

// TestParseFirstPRURL covers the gh-list JSON parser used by
// GitOperations.FindMergedPRByBranch (Shape C idempotency).
func TestParseFirstPRURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"merged PR", `[{"url":"https://github.com/o/r/pull/12"}]`, "https://github.com/o/r/pull/12"},
		{"empty array", `[]`, ""},
		{"two entries takes first", `[{"url":"https://github.com/o/r/pull/1"},{"url":"https://github.com/o/r/pull/2"}]`, "https://github.com/o/r/pull/1"},
		{"garbage", `not json`, ""},
		{"empty string", ``, ""},
		{"null", `null`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseFirstPRURL([]byte(tt.in)); got != tt.want {
				t.Errorf("parseFirstPRURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
