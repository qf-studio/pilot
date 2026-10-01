package executor

import (
	"fmt"
	"path/filepath"
)

// PR-creator kinds reported by resolvePRCreator (GH-5583).
const (
	// prCreatorKindProject: a creator registered under "project:<canonical
	// project path>" at startup (per-project `gitlab:` block).
	prCreatorKindProject = "project"
	// prCreatorKindGitHubSDK: the per-repo "github:<owner/repo>" registration
	// (M7 4d.4) for a github-sourced task.
	prCreatorKindGitHubSDK = "github-sdk"
	// prCreatorKindShared: the shared r.prCreator slot (set per event by the
	// non-GitHub issue handlers).
	prCreatorKindShared = "shared"
	// prCreatorKindGHCLI: no creator — fall back to `gh pr create`.
	prCreatorKindGHCLI = "gh-cli"
)

// ProjectPRCreatorKey returns the Runner.RegisterPRCreator key for a
// project-keyed creator: "project:" + the project path after
// filepath.EvalSymlinks (GH-5583). On hosted boxes configured project paths are
// symlinks while task.ProjectPath may arrive in either form, so both the
// startup registration and the runner lookup go through this one function.
// The lookup must key on task.ProjectPath, never the worktree path (GH-5582).
// Returns "" for an empty path (nothing to key on).
func ProjectPRCreatorKey(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	canonical := filepath.Clean(projectPath)
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	return "project:" + canonical
}

// resolvePRCreator picks the PR/MR creator for a task (GH-5583). Order:
//
//  1. "project:<canonical task.ProjectPath>" registration → "project"
//  2. "github:<task.SourceRepo>" registration when SourceAdapter=="github" → "github-sdk"
//  3. the shared r.prCreator slot for non-GitHub adapters → "shared"
//  4. nil → "gh-cli" (caller falls back to `gh pr create`)
//
// Steps 2–4 are the pre-existing behavior, unchanged. Step 1 wins over a stale
// shared slot: the shared slot is mutated per event by whichever poller ran
// last, so on its own it can point at an unrelated repo.
func (r *Runner) resolvePRCreator(task *Task) (PRCreator, string) {
	if key := ProjectPRCreatorKey(task.ProjectPath); key != "" {
		if c := r.prCreatorFor(key); c != nil {
			return c, prCreatorKindProject
		}
	}
	if task.SourceAdapter == "github" && task.SourceRepo != "" {
		if c := r.prCreatorFor("github:" + task.SourceRepo); c != nil {
			return c, prCreatorKindGitHubSDK
		}
	}
	if r.prCreator != nil && task.SourceAdapter != "" && task.SourceAdapter != "github" {
		return r.prCreator, prCreatorKindShared
	}
	return nil, prCreatorKindGHCLI
}

// mrSourceSuffix returns the MR-body line linking a task back to its source
// issue, with its leading blank line ("" when there is nothing to link).
//
// Shared-slot path: GitLab's "Closes #<iid>" for any non-empty SourceIssueID
// (unchanged since GH-3785).
// Project-keyed path: "Closes #<iid>" only when the task came from GitLab — for
// any other source SourceIssueID is not a GitLab IID, so a closing keyword
// would close an unrelated issue; emit "Source: <issue URL>" instead.
//
// Neither path calls extraFixesKeyword (GH-5191): it emits GitHub closing
// keywords and extra issue numbers aren't guaranteed to be GitLab IIDs.
func mrSourceSuffix(task *Task, kind string) string {
	if kind != prCreatorKindProject || task.SourceAdapter == "gitlab" {
		if task.SourceIssueID == "" {
			return ""
		}
		return fmt.Sprintf("\n\nCloses #%s", task.SourceIssueID)
	}
	return fmt.Sprintf("\n\nSource: %s", sourceIssueURL(task))
}

// sourceIssueURL reconstructs a human-followable reference to the task's
// source issue. The URL itself is not persisted on the executions row (tasks
// are rebuilt from it by buildTaskFromExecution), so it is derived from the
// fields that are: Linear's URL scheme matches cmd/pilot/handlers.go; when the
// tracker's base URL is unknown, fall back to "<adapter> <task id>".
func sourceIssueURL(task *Task) string {
	switch task.SourceAdapter {
	case "linear":
		return fmt.Sprintf("https://linear.app/issue/%s", task.ID)
	case "github":
		if task.SourceRepo != "" && task.SourceIssueID != "" {
			return fmt.Sprintf("https://github.com/%s/issues/%s", task.SourceRepo, task.SourceIssueID)
		}
	}
	if task.SourceAdapter == "" {
		return task.ID
	}
	return fmt.Sprintf("%s %s", task.SourceAdapter, task.ID)
}
