package autopilot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// localMergeAttempt is the result of replaying a PR's merge locally in a
// scratch worktree.
//
// GH-4328: ghClient.UpdatePullRequestBranch is GitHub's server-side
// merge-from-base. It only succeeds when the divergence is non-conflicting —
// a textual conflict (which go.sum always is once two branches both touch
// it) makes the API return a bare error with no information about which
// files actually conflict. attemptLocalMerge reproduces the same merge
// locally so the conflicted files can be inspected, as a prerequisite to
// attempting mechanical resolution instead of falling straight through to
// close-and-reexecute.
type localMergeAttempt struct {
	// WorktreePath is the scratch worktree the merge was attempted in.
	WorktreePath string
	// ConflictedFiles lists the paths git left in a conflicted (unmerged)
	// state. Empty means the merge completed cleanly.
	ConflictedFiles []string
}

// Conflicted reports whether the local merge left any file unmerged.
func (r *localMergeAttempt) Conflicted() bool {
	return r != nil && len(r.ConflictedFiles) > 0
}

// attemptLocalMerge fetches branchName and baseBranch from origin, checks
// out branchName into a fresh scratch worktree of repoPath, and merges
// origin/baseBranch into it.
//
// The caller MUST invoke the returned cleanup func exactly once, regardless
// of whether the merge succeeded, conflicted, or errored, to remove the
// scratch worktree.
func attemptLocalMerge(ctx context.Context, repoPath, branchName, baseBranch string) (*localMergeAttempt, func(), error) {
	noopCleanup := func() {}

	if err := runGitCmd(ctx, repoPath, "fetch", "origin", branchName, baseBranch); err != nil {
		return nil, noopCleanup, fmt.Errorf("fetch origin %s %s: %w", branchName, baseBranch, err)
	}

	worktreePath, err := os.MkdirTemp("", "pilot-conflict-resolve-*")
	if err != nil {
		return nil, noopCleanup, fmt.Errorf("create scratch worktree dir: %w", err)
	}
	// `git worktree add` creates worktreePath itself and refuses to target an
	// existing directory, so drop the MkdirTemp placeholder first.
	if err := os.Remove(worktreePath); err != nil {
		return nil, noopCleanup, fmt.Errorf("remove scratch worktree placeholder: %w", err)
	}

	cleanup := func() {
		_ = runGitCmd(ctx, repoPath, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}

	if err := runGitCmd(ctx, repoPath, "worktree", "add", "--detach", worktreePath, "origin/"+branchName); err != nil {
		cleanup()
		return nil, noopCleanup, fmt.Errorf("worktree add for origin/%s: %w", branchName, err)
	}

	if mergeErr := runGitCmd(ctx, worktreePath, "merge", "--no-edit", "origin/"+baseBranch); mergeErr != nil {
		conflicted, listErr := conflictedFiles(ctx, worktreePath)
		if listErr != nil {
			cleanup()
			return nil, noopCleanup, fmt.Errorf("merge origin/%s failed (%v) and conflicted files could not be listed: %w", baseBranch, mergeErr, listErr)
		}
		if len(conflicted) == 0 {
			// Merge failed for a reason other than a textual conflict
			// (e.g. bad revision) — surface the original error rather than
			// silently reporting a clean merge.
			cleanup()
			return nil, noopCleanup, fmt.Errorf("merge origin/%s failed with no conflicted files: %w", baseBranch, mergeErr)
		}
		return &localMergeAttempt{WorktreePath: worktreePath, ConflictedFiles: conflicted}, cleanup, nil
	}

	return &localMergeAttempt{WorktreePath: worktreePath}, cleanup, nil
}

// conflictedFiles lists paths left in an unmerged state in dir.
func conflictedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := gitOutput(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func runGitCmd(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutput(ctx, dir, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
