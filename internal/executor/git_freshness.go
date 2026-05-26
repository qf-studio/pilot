package executor

import (
	"context"
	"fmt"
	"os/exec"
)

// commitSHAIsNew returns true iff sha exists in the repo AND is NOT an ancestor of
// origin/<baseBranch>. A SHA already reachable from the base branch is a parent SHA —
// proof the executor made no new commit. Returns false (not new) on empty sha.
func commitSHAIsNew(ctx context.Context, repoPath, sha, baseBranch string) (bool, error) {
	if sha == "" {
		return false, nil
	}
	// `git merge-base --is-ancestor SHA origin/BASE` exits 0 when SHA is an ancestor of BASE.
	// We want the opposite: exit 1 means SHA is NOT an ancestor — it's a fresh commit.
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "merge-base", "--is-ancestor", sha, "origin/"+baseBranch)
	err := cmd.Run()
	if err == nil {
		return false, nil // exit 0: SHA is ancestor of base — parent SHA, not fresh
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil // exit 1: SHA is not ancestor — fresh commit
	}
	return false, fmt.Errorf("merge-base check failed: %w", err)
}
