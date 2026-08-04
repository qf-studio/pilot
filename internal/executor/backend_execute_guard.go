// Package executor — GH-4703
//
// Chokepoint for backend.Execute calls that centralizes ExecuteOptions.
// ProjectPath assignment so worktree routing is a structural invariant
// rather than a per-call-site convention.
//
// "ExecuteOptions.ProjectPath must be executionPath, not task.ProjectPath"
// has been violated and reactively patched three times, each time at a
// different call site in runner.go, because every call site rebuilt
// ExecuteOptions{ProjectPath: ...} by hand and had to re-derive the
// convention from memory:
//
//	TASK-323       runner.go (smart-retry, no-op-decline retry)
//	GH-3577/PR#3580 runner.go (quality-gate retry, intent-judge retry)
//	#4702          runner.go (self-review)
//
// The third recurrence (#4702) cost a blocked rebuild and a phantom
// reimplementation staged into the shared daemon repo root. This file
// applies the same pattern repo_guardrail.go already uses for repo
// identity (see ValidateTargetRepo) to path identity: one function that
// every backend.Execute call must route through, which sets the path
// authoritatively and refuses loudly if it collapses back to the task's
// (possibly shared, non-isolated) project root when worktree isolation
// was expected.
package executor

import (
	"context"
	"errors"
	"fmt"
)

// ErrProjectPathNotIsolated is the sentinel returned when backendExecute
// detects that a call would run against task.ProjectPath (the daemon's
// shared repo root / the user's real repo) while worktree isolation was
// expected for this task. This is exactly the bug shape that recurred
// across TASK-323, GH-3577/PR#3580, and #4702: a retry or review call site
// silently falling back to the shared root instead of the task's worktree.
var ErrProjectPathNotIsolated = errors.New("backend execute: project path not isolated from task project root")

// backendExecute is the single chokepoint through which every
// r.backend.Execute call in this package must flow (mirrors
// repo_guardrail.go's ValidateTargetRepo chokepoint for repo identity).
//
// executionPath is the resolved execution directory for this call — the
// worktree path when worktree isolation is active for the task, or the
// project root for tasks that are legitimately root-scoped (LocalMode,
// Q&A, CLI: task.Branch == "" or task.DirectCommit). It always wins over
// whatever the caller populated in opts.ProjectPath, which removes the
// "did this call site remember to use executionPath instead of
// task.ProjectPath" decision from every call site — it is no longer
// possible to build an ExecuteOptions with the wrong path field, only to
// call backendExecute with the wrong executionPath argument, which the
// guard below catches.
//
// The guard fires — returning ErrProjectPathNotIsolated instead of
// silently proceeding — when the resolved path collapses to
// task.ProjectPath while worktree isolation was expected
// (r.config.UseWorktree && task.Branch != "" && !task.DirectCommit). That
// condition is the exact shape of the historical bug: a worktree-eligible
// task whose execution path resolved (correctly or via upstream failure)
// to the shared root instead of an isolated worktree.
//
// The documented exception is preserved: LocalMode/Q&A/CLI tasks
// (task.Branch == "" or task.DirectCommit) are not expected to be
// worktree-isolated and legitimately pass through with
// executionPath == task.ProjectPath.
func (r *Runner) backendExecute(ctx context.Context, task *Task, executionPath string, opts ExecuteOptions) (*BackendResult, error) {
	opts.ProjectPath = executionPath

	worktreeExpected := task != nil && r.config != nil && r.config.UseWorktree && task.Branch != "" && !task.DirectCommit
	if worktreeExpected && opts.ProjectPath == task.ProjectPath {
		return nil, fmt.Errorf("%w: task %s expected worktree isolation (use_worktree=true, branch=%q, direct_commit=false) but execution path resolved to the project root %q instead of an isolated worktree",
			ErrProjectPathNotIsolated, task.ID, task.Branch, task.ProjectPath)
	}

	return r.backend.Execute(ctx, opts)
}
