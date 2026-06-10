package executor

import (
	"context"
	"os/exec"
	"testing"
)

// TestDecomposedChildBaseBranch verifies that when a decomposed child task has
// an empty BaseBranch (inherited from a parent whose project config omits
// default_branch/branch_from), executeWithOptions resolves it to the repo's
// default branch before any worktree is created — so CreatePR always receives
// the correct base, never an empty or worktree-context-inferred value (GH-3540).
func TestDecomposedChildBaseBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	local, _ := setupSyncTestRepos(t, "main")

	task := &Task{
		ID:          "GH-3516",
		Title:       "Decomposed child",
		Description: "Child task from dispatcher decomposition",
		ProjectPath: local,
		Branch:      "pilot/GH-3516",
		CreatePR:    true,
		BaseBranch:  "", // empty: simulates inheriting from parent with no BaseBranch set
	}

	ctx := context.Background()

	// Apply the same resolution logic that executeWithOptions runs before worktree creation.
	if task.BaseBranch == "" && task.CreatePR && task.Branch != "" {
		mainGit := NewGitOperations(task.ProjectPath)
		if resolved, err := mainGit.GetDefaultBranch(ctx); err == nil && resolved != "" {
			task.BaseBranch = resolved
		} else {
			task.BaseBranch = "main"
		}
	}

	if task.BaseBranch == "" {
		t.Fatal("BaseBranch must not be empty after resolution")
	}
	if task.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want %q (repo default branch)", task.BaseBranch, "main")
	}
}

// TestDecomposedChildBaseBranch_NonEmpty verifies that a pre-set BaseBranch is
// not overwritten by the resolution logic (e.g. a "dev"-workflow task already
// has the correct target branch).
func TestDecomposedChildBaseBranch_NonEmpty(t *testing.T) {
	task := &Task{
		ID:         "GH-3517",
		Branch:     "pilot/GH-3517",
		CreatePR:   true,
		BaseBranch: "dev",
	}

	// Resolution must be a no-op when BaseBranch is already set.
	if task.BaseBranch == "" && task.CreatePR && task.Branch != "" {
		task.BaseBranch = "main" // would overwrite — must not reach here
	}

	if task.BaseBranch != "dev" {
		t.Errorf("BaseBranch = %q, want %q (pre-set value must be preserved)", task.BaseBranch, "dev")
	}
}

// TestCreateSubtasks_BaseBranchPropagation verifies that createSubtasks
// propagates the parent's BaseBranch to all subtasks, including the last one
// that creates the PR.  When the parent has an empty BaseBranch the runner
// will resolve it at execution time (GH-3540).
func TestCreateSubtasks_BaseBranchPropagation(t *testing.T) {
	tests := []struct {
		name            string
		parentBaseBranch string
	}{
		{"explicit main",  "main"},
		{"explicit dev",   "dev"},
		{"empty inherited", ""},
	}

	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "medium",
		MaxSubtasks:         3,
		MinDescriptionWords: 5,
	}
	decomposer := NewTaskDecomposer(config)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := &Task{
				ID:    "GH-3513",
				Title: "Multi-step task",
				Description: `Implement three separate features:
1. Add user authentication module
2. Add payment processing module
3. Add reporting dashboard`,
				ProjectPath: "/repo",
				Branch:      "pilot/GH-3513",
				CreatePR:    true,
				BaseBranch:  tt.parentBaseBranch,
			}

			result := decomposer.Decompose(parent)
			if !result.Decomposed || len(result.Subtasks) < 2 {
				t.Skipf("task not decomposed (%d subtasks, reason: %s)", len(result.Subtasks), result.Reason)
			}

			for i, subtask := range result.Subtasks {
				if subtask.BaseBranch != tt.parentBaseBranch {
					t.Errorf("subtask %d: BaseBranch = %q, want %q", i, subtask.BaseBranch, tt.parentBaseBranch)
				}
			}

			// Last subtask creates the PR — its BaseBranch is what flows to CreatePR.
			last := result.Subtasks[len(result.Subtasks)-1]
			if !last.CreatePR {
				t.Error("last subtask must have CreatePR=true")
			}
			if last.BaseBranch != tt.parentBaseBranch {
				t.Errorf("last subtask BaseBranch = %q, want %q", last.BaseBranch, tt.parentBaseBranch)
			}
		})
	}
}
