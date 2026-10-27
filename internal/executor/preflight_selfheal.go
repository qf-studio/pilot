package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// preflightSelfHealTimeout bounds the whole ownership test plus reset, the
// remote lookup (ls-remote) being the only step that can touch the network.
const preflightSelfHealTimeout = 30 * time.Second

// pilotBranchPrefix marks a Pilot task branch. The default branch never has it.
const pilotBranchPrefix = "pilot/"

// healOwnership is the result of the three ownership conditions that must all
// hold before the git_clean pre-flight may discard a dirty direct-mode clone.
type healOwnership struct {
	Branch      string
	PilotBranch bool // current branch starts with pilot/
	Pushed      bool // branch HEAD exists on the remote
	NoActiveRun bool // ledger has no other running/queued row for the project
	Detail      string
}

func (o healOwnership) owned() bool { return o.PilotBranch && o.Pushed && o.NoActiveRun }

// String renders the three condition results for the pre-flight error text, so
// an operator sees why the heal did not run.
func (o healOwnership) String() string {
	return fmt.Sprintf("self-heal skipped: branch %q is a pilot/ branch=%t, HEAD pushed to remote=%t, no other run active=%t%s",
		o.Branch, o.PilotBranch, o.Pushed, o.NoActiveRun, o.Detail)
}

// checkHealOwnership evaluates the three ownership conditions. It never
// modifies the tree. Any lookup failure counts as the condition not holding:
// the heal discards work, so it needs positive proof of ownership.
func (r *Runner) checkHealOwnership(ctx context.Context, task *Task, git *GitOperations) healOwnership {
	var o healOwnership
	var notes []string

	branch, err := git.GetCurrentBranch(ctx)
	if err != nil {
		notes = append(notes, fmt.Sprintf(" (branch lookup: %v)", err))
	}
	o.Branch = branch
	o.PilotBranch = strings.HasPrefix(branch, pilotBranchPrefix)

	if o.PilotBranch {
		o.Pushed = git.headOnRemote(ctx, branch)
	}

	if r.logStore == nil {
		notes = append(notes, " (no execution ledger wired)")
	} else {
		busy, ledgerErr := r.otherActiveExecution(task)
		switch {
		case ledgerErr != nil:
			notes = append(notes, fmt.Sprintf(" (ledger lookup: %v)", ledgerErr))
		case busy != "":
			notes = append(notes, fmt.Sprintf(" (active execution %s)", busy))
		default:
			o.NoActiveRun = true
		}
	}
	o.Detail = strings.Join(notes, "")
	return o
}

// otherActiveExecution returns the ID of a running/queued ledger row for the
// task's project other than the task's own (whose row is "running" while its
// pre-flight executes), or "" when there is none.
func (r *Runner) otherActiveExecution(task *Task) (string, error) {
	rows, err := r.logStore.GetNonTerminalExecutions(task.ProjectPath)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.TaskID == task.ID ||
			(task.ExecutionID != "" && row.ID == task.ExecutionID) ||
			(task.ParentExecutionID != "" && row.ID == task.ParentExecutionID) {
			continue
		}
		return row.ID, nil
	}
	return "", nil
}

// headOnRemote reports whether the branch's HEAD commit is already on the
// remote: contained in the local remote-tracking ref, or equal to what
// `git ls-remote` reports for the branch.
func (g *GitOperations) headOnRemote(ctx context.Context, branch string) bool {
	head, err := g.GetCurrentCommitSHA(ctx)
	if err != nil {
		return false
	}
	if _, err := g.gitOutput(ctx, "merge-base", "--is-ancestor", head, "refs/remotes/origin/"+branch); err == nil {
		return true
	}
	out, err := g.gitOutput(ctx, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return false
	}
	fields := strings.Fields(out)
	return len(fields) >= 1 && fields[0] == head
}

// preflightGitCleanSelfHeal runs after the git_clean pre-flight failed in
// direct mode. If the dirt provably belongs to a finished Pilot run it resets
// the tree to HEAD and returns healed=true (the caller re-runs the check once).
// Otherwise the tree is untouched and the returned error is the original
// pre-flight error extended with the three condition results.
func (r *Runner) preflightGitCleanSelfHeal(ctx context.Context, task *Task, executionPath string, preflightErr error) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightSelfHealTimeout)
	defer cancel()

	git := NewGitOperations(executionPath)
	dirty, err := git.dirtyProjectPaths(ctx)
	if err != nil || len(dirty) == 0 {
		return false, preflightErr
	}

	own := r.checkHealOwnership(ctx, task, git)
	if !own.owned() {
		return false, fmt.Errorf("%w; %s", preflightErr, own)
	}

	if err := git.ResetHardToCommit(ctx, "HEAD"); err != nil {
		return false, fmt.Errorf("%w; self-heal reset failed: %v", preflightErr, err)
	}

	msg := fmt.Sprintf("pre-flight self-heal: discarded %d uncommitted change(s) left by finished run on branch %s", len(dirty), own.Branch)
	r.log.Warn(msg, slog.String("task_id", task.ID), slog.Int("files", len(dirty)), slog.String("branch", own.Branch))
	r.saveLogEntry(task.LogExecutionID(), "warn", msg)
	detail, _ := json.Marshal(map[string]any{"branch": own.Branch, "files": len(dirty)})
	r.recordExecutionEvent(task.LogExecutionID(), memory.StagePreflightSelfHeal, string(detail))
	r.reportProgress(task.ID, "Pre-flight Self-Heal", 1, msg)
	return true, nil
}

// runPreflight runs the pre-flight checks. In direct mode a git_clean failure
// first gets one self-heal attempt (GH-5901) and one re-run of the checks.
func (r *Runner) runPreflight(ctx context.Context, task *Task, executionPath string, opts PreflightOptions) error {
	err := RunPreflightChecksWithOptions(ctx, executionPath, opts)
	var pfErr *PreflightError
	if err == nil || opts.SkipGitClean || !errors.As(err, &pfErr) || pfErr.CheckName != "git_clean" {
		return err
	}
	healed, healErr := r.preflightGitCleanSelfHeal(ctx, task, executionPath, err)
	if !healed {
		return healErr
	}
	return RunPreflightChecksWithOptions(ctx, executionPath, opts)
}
