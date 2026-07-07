package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakePRCreatorGH4031 is a minimal PRCreator stub for exercising the
// registry/non-GitHub CreatePR branch of finalizeDecomposedParentPR without
// depending on gh CLI or network access.
type fakePRCreatorGH4031 struct {
	url string
	err error
}

func (f *fakePRCreatorGH4031) CreatePR(_ context.Context, _, _, _, _ string) (string, error) {
	return f.url, f.err
}

// setupDecomposedParentRepoGH4031 builds a repo with a pushable bare remote
// (main, one commit), then creates and checks out branch with one additional
// commit so the caller has real, pushable work — mirroring the state a
// decomposed parent's worktree is in once all subtasks finished (GH-4028).
func setupDecomposedParentRepoGH4031(t *testing.T, branch string) string {
	t.Helper()
	repoDir, _ := setupFreshnessRepo(t)
	runGit(t, repoDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("subtask work\n"), 0644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, repoDir, "add", "f.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")
	return repoDir
}

// TestFinalizeDecomposedParentPR_SuccessPushesAndCreatesPR is acceptance (a):
// a decomposed parent whose subtasks all succeeded (real commits on the
// branch) must have finalizeDecomposedParentPR push the branch and create a
// PR — landing a non-empty PRUrl and leaving Success=true. GH-4028: before
// this fix, subtasks always ran with task.Branch cleared, so the direct
// path's push+PR block never fired for any subtask and the parent finished
// "completed" with pr_url="".
func TestFinalizeDecomposedParentPR_SuccessPushesAndCreatesPR(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`)) // no pre-existing merged/open PR

	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/GH-9001")

	r := newSilentRunnerTask359()
	r.prCreator = &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/7"}
	task := &Task{
		ID:            "GH-9001",
		Title:         "add feature",
		Description:   "d",
		Branch:        "pilot/GH-9001",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab", // non-github so the registry PRCreator branch is used
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}
	if result.PRUrl != "https://gitlab.example.com/o/r/-/merge_requests/7" {
		t.Errorf("PRUrl = %q, want mocked PR URL", result.PRUrl)
	}
	if result.Error != "" {
		t.Errorf("expected no error on success, got %q", result.Error)
	}

	// The branch must actually have been pushed to origin.
	git := NewGitOperations(repoDir)
	if !git.RemoteBranchExists(context.Background(), "pilot/GH-9001") {
		t.Error("expected branch to be pushed to origin")
	}
}

// TestFinalizeDecomposedParentPR_PRCreateFailIsFailure is acceptance (b):
// push succeeds but PR-create fails — the execution MUST be marked failed
// (fail-loud, TASK-379/TASK-359 Layer 1), not "completed" with an empty
// pr_url, so the task remains retry-eligible instead of silently stranding
// pushed work behind a closed completed-execution guard.
func TestFinalizeDecomposedParentPR_PRCreateFailIsFailure(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`)) // no pre-existing merged/open PR

	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/GH-9002")

	r := newSilentRunnerTask359()
	r.prCreator = &fakePRCreatorGH4031{err: errors.New("mr create: forbidden")}
	task := &Task{
		ID:            "GH-9002",
		Title:         "add feature",
		Description:   "d",
		Branch:        "pilot/GH-9002",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if result.Success {
		t.Error("expected Success=false when PR-create fails")
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL on PR-create failure, got %q", result.PRUrl)
	}
	if result.Error == "" {
		t.Error("expected a descriptive error, got empty string")
	}

	// The push itself must have succeeded despite the later PR-create
	// failure — the branch should exist on origin so no work is lost.
	git := NewGitOperations(repoDir)
	if !git.RemoteBranchExists(context.Background(), "pilot/GH-9002") {
		t.Error("expected branch to have been pushed to origin before PR-create failed")
	}
}
