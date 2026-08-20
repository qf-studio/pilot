package autopilot

import (
	"context"
	"errors"
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

// gitIsStrictAncestor fetches ancestorBranch and descendantBranch from
// origin into a scratch worktree of repoPath — reusing attemptLocalMerge's
// fetch + `git worktree add --detach` shape above, so this read-only
// ancestry probe never touches repoPath's own working tree/index while some
// other goroutine may have it checked out for an unrelated operation (e.g.
// attemptMechanicalConflictResolution running on the same merging-time
// path) — and reports whether ancestorSHA is a STRICT ancestor of
// descendantSHA: reachable via `git merge-base --is-ancestor`, and not the
// same commit. Used by Controller.headIsStrictDescendant (controller.go)
// for the GH-5027 stacked-superset ancestry check.
func gitIsStrictAncestor(ctx context.Context, repoPath, ancestorBranch, ancestorSHA, descendantBranch, descendantSHA string) (bool, error) {
	if ancestorSHA == descendantSHA {
		return false, nil
	}

	if err := runGitCmd(ctx, repoPath, "fetch", "origin", ancestorBranch, descendantBranch); err != nil {
		return false, fmt.Errorf("fetch origin %s %s: %w", ancestorBranch, descendantBranch, err)
	}

	worktreePath, err := os.MkdirTemp("", "pilot-ancestry-check-*")
	if err != nil {
		return false, fmt.Errorf("create scratch worktree dir: %w", err)
	}
	// See attemptLocalMerge above: `git worktree add` refuses to target an
	// existing directory, so drop the MkdirTemp placeholder first.
	if err := os.Remove(worktreePath); err != nil {
		return false, fmt.Errorf("remove scratch worktree placeholder: %w", err)
	}
	defer func() {
		_ = runGitCmd(ctx, repoPath, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}()

	if err := runGitCmd(ctx, repoPath, "worktree", "add", "--detach", worktreePath, "origin/"+descendantBranch); err != nil {
		return false, fmt.Errorf("worktree add for origin/%s: %w", descendantBranch, err)
	}

	if _, err := gitOutput(ctx, worktreePath, "merge-base", "--is-ancestor", ancestorSHA, descendantSHA); err != nil {
		// `--is-ancestor` communicates its answer entirely via exit code:
		// 0 = ancestor, 1 = not-an-ancestor (a normal, expected answer, not
		// a detection failure), and anything else (bad revision, missing
		// object, network) is a genuine error the caller must not silently
		// treat as "not stacked".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("merge-base --is-ancestor %s %s: %w", ancestorSHA, descendantSHA, err)
	}
	return true, nil
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
