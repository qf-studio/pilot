package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
)

// ghostSHACleanNoCommitError is applyGhostSHAGuard's Error string when a
// ghost SHA was rejected AND preserveDirtyWorktreeAsWIP already ran and found
// nothing to preserve — i.e. structural proof the worktree is genuinely
// clean. GH-4964's decline classification is only safe to key off this
// EXACT string (not a prefix): the preserved-WIP branch's error text reads
// differently ("...auto-preserved as %s...") and must never be treated as
// decline evidence, since real uncommitted diffs contradict any no-op claim.
const ghostSHACleanNoCommitError = "no new commit produced — worktree HEAD matches base branch parent"

// applyGhostSHAGuard enforces GH-3126: reject executions that produced no new commit.
// When Claude makes no new commit, git log returns the parent (pre-execution) SHA.
// Recording that as CommitSHA causes IsTaskShipped to return true on a no-op run,
// triggering pilot-done + issue close with no actual work delivered.
// Skipped for LocalMode tasks (read-only intents have no commit expectation — GH-3642).
// Fails open on check errors (e.g. no origin configured in test repos): only rejects
// when the check conclusively shows the SHA is already on origin/<base>.
// Returns true when the GH-4517 dirty-worktree backstop fired: the ghost SHA
// was rejected, but the worktree still had uncommitted changes, so they were
// auto-committed and pushed to the task branch instead of being silently
// discarded. Callers should record this as a visible execution event — see
// Runner.applyGhostSHAGuardWithPreserve, the sole call site.
func applyGhostSHAGuard(ctx context.Context, task *Task, result *ExecutionResult, executionPath string, log *slog.Logger) bool {
	if task.LocalMode || result.CommitSHA == "" || !result.Success {
		return false
	}
	git := NewGitOperations(executionPath)
	ghostBase := task.BaseBranch
	if ghostBase == "" {
		ghostBase, _ = git.GetDefaultBranch(ctx)
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
		// GH-4517: the ghost-SHA guard only proves the *harvested* SHA is
		// stale — it says nothing about the worktree's working tree. Incident
		// pilot-console#26/B8 (2026-07-23): a 44-minute session did real,
		// test-passing work and never ran `git commit`; the worktree was then
		// silently deleted as a no-op by the runner's deferred cleanup,
		// destroying it. Before discarding, check for uncommitted changes and
		// preserve them onto the task branch rather than losing them.
		if sha, preserved := preserveDirtyWorktreeAsWIP(ctx, git, task, log); preserved {
			result.CommitSHA = sha
			result.Success = false
			result.Error = fmt.Sprintf(
				"worktree had uncommitted work at no-op classification — auto-preserved as %s on branch %s; needs manual review, not a genuine no-op",
				sha[:min(7, len(sha))], task.Branch,
			)
			return true
		}
		result.CommitSHA = ""
		result.Success = false
		result.Error = ghostSHACleanNoCommitError
	}
	return false
}

// preserveDirtyWorktreeAsWIP is the GH-4517 harvester backstop for a
// worktree that is about to be classified no_op — and thus deleted by the
// runner's deferred worktree cleanup — despite still holding uncommitted
// changes. It auto-commits any stageable dirty paths (GitOperations.Commit
// already excludes noise: .agent/, .claude/, node_modules/, lockfiles, etc.)
// to the task branch as a wip(<task-id>) commit and pushes it, so a human or
// a retry can pick the work up instead of it silently vanishing.
//
// Returns preserved=false with no side effects when the tree is genuinely
// clean, or when only excluded noise paths are dirty (GitOperations.Commit
// returns ErrNoStageableChanges) — the classic no_op case is left untouched,
// so genuine no-ops still classify exactly as before.
func preserveDirtyWorktreeAsWIP(ctx context.Context, git *GitOperations, task *Task, log *slog.Logger) (sha string, preserved bool) {
	if task.Branch == "" {
		return "", false
	}
	message := fmt.Sprintf("wip(%s): uncommitted session work (auto-preserved)", task.ID)
	newSHA, commitErr := git.Commit(ctx, message)
	if commitErr != nil {
		if !errors.Is(commitErr, ErrNoStageableChanges) {
			log.Warn("executor: dirty-worktree auto-preserve commit attempt failed",
				slog.String("task_id", task.ID),
				slog.Any("error", commitErr),
			)
		}
		return "", false
	}
	if pushErr := git.Push(ctx, task.Branch); pushErr != nil {
		// Pin the commit under a recovery ref (GH-3785 pattern) so it
		// survives worktree cleanup even though the push itself failed —
		// never leave a preserved commit reachable only via a worktree that
		// is about to be removed.
		recoveryRef, refErr := git.CreateRecoveryRef(ctx, task.ID, "HEAD")
		fields := []any{
			slog.String("task_id", task.ID),
			slog.String("sha", newSHA[:min(7, len(newSHA))]),
			slog.Any("push_error", pushErr),
		}
		if refErr == nil {
			fields = append(fields, slog.String("recovery_ref", recoveryRef))
		} else {
			fields = append(fields, slog.Any("recovery_ref_error", refErr))
		}
		log.Warn("executor: dirty-worktree auto-preserve push failed — commit pinned via recovery ref", fields...)
		return newSHA, true
	}
	log.Warn("executor: auto-preserved uncommitted session work — worktree was dirty at no-op classification",
		slog.String("task_id", task.ID),
		slog.String("branch", task.Branch),
		slog.String("sha", newSHA[:min(7, len(newSHA))]),
	)
	return newSHA, true
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
