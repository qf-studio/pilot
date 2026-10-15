package executor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/qf-studio/pilot/internal/memory"
)

// preservedAdoption records that a GH-4517 auto-preserved wip commit was
// adopted as the task's deliverable (GH-5709) instead of failing the run.
//
// Incident 2026-10-15, #5701: the session backgrounded a long `go test`, its
// wait loop hit the Bash timeout and it ended the turn with staged-but-
// uncommitted work. The executor auto-preserved that work as a wip commit and
// failed the task as "needs manual review" — yet the commit was the complete,
// gate-green implementation and a human had to open the PR by hand.
//
// Adoption lets the normal gate → push → PR finalisation run on the preserved
// commit. If the gates then go red the task still fails with the
// auto-preserved marker (adoption.failureText), exactly as before.
type preservedAdoption struct {
	sha    string // full SHA of the preserved commit
	branch string
	stage  string // classification point that preserved it, e.g. "before no-commit retry"

	finished bool // finishAdoption already ran (GH-5718: fire exactly once)
}

func (a *preservedAdoption) shortSHA() string {
	return a.sha[:min(7, len(a.sha))]
}

// failureText is today's auto-preserved failure text (so IsPermanentFailure
// and parseAutoPreservedSHABranch keep matching) followed by the gate output.
func (a *preservedAdoption) failureText(gateOutput string) string {
	return fmt.Sprintf(
		"worktree had uncommitted work %s — auto-preserved as %s on branch %s; needs manual review, not a genuine no-op\n\nquality gates failed on the preserved commit:\n\n%s",
		a.stage, a.shortSHA(), a.branch, gateOutput,
	)
}

// canAdoptPreserved reports whether a dirty worktree may be adopted as the
// deliverable once preserved. All of these must hold:
//   - a normal PR task (adoption feeds the push/PR finalisation);
//   - the backend ended on a successful result event — a crash or timeout
//     gives no evidence the work was finished;
//   - quality gates are enabled and configured, so "green" is real evidence
//     (without gates the preserved commit stays a manual-review failure);
//   - no decline evidence (DECLINED marker / no_op+reason): a dirty tree
//     contradicts it, which is exactly why GH-4964 makes preserve win.
//
// It may auto-enable the minimal build gate (ensureQualityCheckerFactory),
// the same call the gate section makes later.
func (r *Runner) canAdoptPreserved(task *Task, backendResult *BackendResult, state *progressState, executionPath string, log *slog.Logger) bool {
	if !task.CreatePR || task.DirectCommit || task.LocalMode || task.Branch == "" || task.SkipQualityGates {
		return false
	}
	if backendResult == nil || !backendResult.Success || backendResult.Error != "" || backendResult.ErrorType != "" {
		return false
	}
	if state.exitSignalNoOp && state.exitSignalReason != "" {
		return false
	}
	if _, declined := parseDeclinedReason(backendResult.LastAssistantText); declined {
		return false
	}
	r.ensureQualityCheckerFactory(executionPath, log)
	return r.qualityCheckerFactory != nil
}

// adoptPreservedWorktree is the pre-failure half of the GH-5709 rescue for
// the "before no-commit retry" classification point: when the worktree is
// dirty and canAdoptPreserved holds, it auto-preserves the work and returns
// the adoption so the caller continues into the normal quality-gate flow.
// Returns nil — with no side effects — when the tree is clean or adoption is
// not allowed, leaving preserveDirtyOrFail's behaviour untouched.
func (r *Runner) adoptPreservedWorktree(ctx context.Context, git *GitOperations, task *Task, result *ExecutionResult, backendResult *BackendResult, state *progressState, executionPath string, log *slog.Logger, stage string) *preservedAdoption {
	if !result.Success || !r.canAdoptPreserved(task, backendResult, state, executionPath, log) {
		return nil
	}
	sha, preserved := preserveDirtyWorktreeAsWIP(ctx, git, task, log)
	if !preserved {
		return nil
	}
	return r.beginAdoption(task, result, sha, stage, true)
}

// adoptGhostPreserved is the same rescue for the ghost-SHA classification
// point: applyGhostSHAGuard already committed the dirty work and marked the
// result failed, so adoption undoes that verdict. It returns nil (result left
// as the guard set it) when adoption is not allowed.
func (r *Runner) adoptGhostPreserved(task *Task, result *ExecutionResult, backendResult *BackendResult, state *progressState, executionPath string, log *slog.Logger) *preservedAdoption {
	if !r.canAdoptPreserved(task, backendResult, state, executionPath, log) {
		return nil
	}
	sha := result.CommitSHA
	result.Success = true
	result.Error = ""
	// GH-5718: applyGhostSHAGuardWithPreserve already recorded work_preserved
	// for this commit, so the ghost path must not record a second one.
	return r.beginAdoption(task, result, sha, "at no-op classification", false)
}

// beginAdoption starts an adoption. recordPreserved controls the
// work_preserved event: the worktree path preserves the work itself and has
// not recorded one yet; the ghost path's guard already did.
func (r *Runner) beginAdoption(task *Task, result *ExecutionResult, sha, stage string, recordPreserved bool) *preservedAdoption {
	a := &preservedAdoption{sha: sha, branch: task.Branch, stage: stage}
	result.CommitSHA = sha
	if recordPreserved {
		r.recordExecutionEvent(task.LogExecutionID(), memory.StageWorkPreserved, fmt.Sprintf(
			"worktree had uncommitted work %s — auto-preserved as %s on branch %s; adopting as the deliverable pending quality gates",
			stage, a.shortSHA(), task.Branch,
		))
	}
	r.reportProgress(task.ID, "Adopting", 91, "Uncommitted work auto-preserved; verifying it with quality gates")
	return a
}

// finishAdoption logs the one INFO line per adoption (task id, sha, gate
// result) so the daemon log shows which runs were rescued, and — when the
// gates were green — records the StageWorkAdopted event. gateResult is
// "passed" or "failed". It fires at most once per adoption (GH-5718): the
// gate section's explicit red/green sites and the deferred finalizer in
// executeWithOptions can both reach it.
func (r *Runner) finishAdoption(task *Task, a *preservedAdoption, gateResult string, log *slog.Logger) {
	if a.finished {
		return
	}
	a.finished = true
	msg := "executor: auto-preserved commit failed quality gates — not adopted"
	if gateResult == "passed" {
		msg = "executor: adopted auto-preserved commit as the deliverable"
		r.recordExecutionEvent(task.LogExecutionID(), memory.StageWorkAdopted, fmt.Sprintf(
			"auto-preserved commit %s on branch %s adopted as the deliverable (quality gates %s)",
			a.shortSHA(), a.branch, gateResult,
		))
	}
	log.Info(msg,
		slog.String("task_id", task.ID),
		slog.String("sha", a.shortSHA()),
		slog.String("gate_result", gateResult),
	)
}
