package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

// applyGhostSHAGuard enforces GH-3126: reject executions that produced no new commit.
// When Claude makes no new commit, git log returns the parent (pre-execution) SHA.
// Recording that as CommitSHA causes IsTaskShipped to return true on a no-op run,
// triggering pilot-done + issue close with no actual work delivered.
// Skipped for LocalMode tasks (read-only intents have no commit expectation — GH-3642).
// Fails open on check errors (e.g. no origin configured in test repos): only rejects
// when the check conclusively shows the SHA is already on origin/<base>.
func applyGhostSHAGuard(ctx context.Context, task *Task, result *ExecutionResult, executionPath string, log *slog.Logger) {
	if task.LocalMode || result.CommitSHA == "" || !result.Success {
		return
	}
	ghostBase := task.BaseBranch
	if ghostBase == "" {
		ghostBase, _ = NewGitOperations(executionPath).GetDefaultBranch(ctx)
		if ghostBase == "" {
			ghostBase = "main"
		}
	}
	if isNew, checkErr := commitSHAIsNew(ctx, executionPath, result.CommitSHA, ghostBase); checkErr != nil {
		log.Warn("executor: ghost-SHA check skipped (will not block)",
			slog.String("task_id", task.ID),
			slog.String("sha", result.CommitSHA[:min(7, len(result.CommitSHA))]),
			slog.Any("error", checkErr),
		)
	} else if !isNew {
		log.Warn("executor: harvested SHA is already on base branch — no new commit",
			slog.String("task_id", task.ID),
			slog.String("sha", result.CommitSHA[:min(7, len(result.CommitSHA))]),
			slog.String("base", ghostBase),
		)
		result.CommitSHA = ""
		result.Success = false
		result.Error = "no new commit produced — worktree HEAD matches base branch parent"
	}
}

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
