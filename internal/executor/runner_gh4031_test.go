package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePRCreatorGH4031 is a minimal PRCreator stub for exercising the
// registry/non-GitHub CreatePR branch of finalizeDecomposedParentPR without
// depending on gh CLI or network access. capturedTitle records the title
// argument CreatePR was invoked with, so tests can assert on it.
type fakePRCreatorGH4031 struct {
	url string
	err error

	capturedTitle string
}

func (f *fakePRCreatorGH4031) CreatePR(_ context.Context, _, _, title, _ string) (string, error) {
	f.capturedTitle = title
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

// TestFinalizeDecomposedParentPR_AutoPrefixesNonConventionalTitle is the
// GH-4220 parity regression guard for the decomposed-parent path:
// finalizeDecomposedParentPR must route task.Title through the same
// normalizeTitle machinery as finalizeEpicBranchPR (runner.go,
// TestFinalizeEpicBranchPR_AutoPrefixesNonConventionalTitle) instead of
// passing "<task.ID>: <task.Title>" straight through to CreatePR — a raw
// issue title is never a conventional commit and fails validatePRTitle
// downstream deterministically.
func TestFinalizeDecomposedParentPR_AutoPrefixesNonConventionalTitle(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`)) // no pre-existing merged/open PR

	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/GH-9003")

	r := newSilentRunnerTask359()
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/8"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9003",
		Title:         "Executor crashes on nil epic plan",
		Labels:        []string{"bug"},
		Description:   "d",
		Branch:        "pilot/GH-9003",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}

	if creator.capturedTitle == fmt.Sprintf("%s: %s", task.ID, task.Title) {
		t.Errorf("title was passed through unmodified (%q) — expected auto-prefixing", creator.capturedTitle)
	}
	if err := validatePRTitle(creator.capturedTitle); err != nil {
		t.Errorf("captured title %q failed validatePRTitle: %v", creator.capturedTitle, err)
	}
	if !strings.HasPrefix(creator.capturedTitle, task.ID+": ") {
		t.Errorf("captured title %q should still carry the %q auto-close prefix", creator.capturedTitle, task.ID+": ")
	}
	if !strings.Contains(creator.capturedTitle, "fix:") {
		t.Errorf("captured title %q, want it to contain conventional type %q (from bug label)", creator.capturedTitle, "fix")
	}
}

// TestFinalizeDecomposedParentPR_TitleNormalizeFailureIsRecoverable mirrors
// TestFinalizeEpicBranchPR_TitleNormalizeFailureIsRecoverable for the
// decomposed-parent path: when normalizeTitle cannot produce a valid
// conventional title (empty title), finalizeDecomposedParentPR must fail
// loud with Success=false and never call CreatePR.
func TestFinalizeDecomposedParentPR_TitleNormalizeFailureIsRecoverable(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`))

	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/GH-9004")

	r := newSilentRunnerTask359()
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/9"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9004",
		Title:         "   ",
		Description:   "d",
		Branch:        "pilot/GH-9004",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}

	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if result.Success {
		t.Error("expected Success=false when the decomposed-parent title cannot be normalized")
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL, got %q", result.PRUrl)
	}
	if creator.capturedTitle != "" {
		t.Error("CreatePR must not be invoked when title normalization fails")
	}
}

// TestFinalizeDecomposedParentPR_TitleRejectionEscalatesOnSecondFailure
// mirrors TestFinalizeEpicBranchPR_TitleRejectionEscalatesOnSecondFailure
// (GH-4220 (e)): the decomposed-parent path must feed titleErr into the same
// GH-2363 record→escalate tracker (recordTitleRejection, title_rejection.go)
// the direct path uses, instead of failing loud with no escalation on every
// poll indefinitely.
func TestFinalizeDecomposedParentPR_TitleRejectionEscalatesOnSecondFailure(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`))

	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/GH-9005")

	r := newSilentRunnerTask359()
	r.titleRejections = newTitleRejectionTracker()
	creator := &fakePRCreatorGH4031{url: "https://gitlab.example.com/o/r/-/merge_requests/10"}
	r.prCreator = creator
	task := &Task{
		ID:            "GH-9005",
		Title:         "   ",
		Description:   "d",
		Branch:        "pilot/GH-9005",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
	}

	result1 := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}
	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result1)
	if result1.Success {
		t.Fatal("expected Success=false on first title-normalize failure")
	}
	if result1.TitleRejected {
		t.Error("first rejection must not escalate yet")
	}

	result2 := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}
	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result2)
	if result2.Success {
		t.Fatal("expected Success=false on second title-normalize failure")
	}
	if !result2.TitleRejected {
		t.Error("second consecutive title-normalize failure must escalate (TitleRejected=true)")
	}
}
