package executor

import (
	"context"
	"fmt"
	"os/exec"
)

// commitSHAIsNew returns true iff sha is NOT an ancestor of origin/<baseBranch>.
// A SHA that is already on the base branch is a parent SHA — proof the executor
// made no new commit. Returns false (with no error) for an empty sha.
//
// Exit semantics of git merge-base --is-ancestor:
//
//	0 = sha IS an ancestor → already on base → not new
//	1 = sha is NOT an ancestor → new commit
//	other = error (e.g. unknown ref)
func commitSHAIsNew(ctx context.Context, repoPath, sha, baseBranch string) (bool, error) {
	if sha == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"merge-base", "--is-ancestor", sha, "origin/"+baseBranch)
	err := cmd.Run()
	if err == nil {
		return false, nil // SHA is ancestor of origin/baseBranch — not a new commit
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil // SHA is NOT an ancestor — it is a genuinely new commit
	}
	return false, fmt.Errorf("merge-base check: %w", err)
}
