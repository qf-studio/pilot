package autopilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
)

// staleBranchPrefix is the only ref namespace the sweep ever lists or deletes.
const staleBranchPrefix = "pilot/"

// DefaultStaleBranchMinAge is the no-PR tip-age floor when
// Config.StaleBranchMinAge is unset.
const DefaultStaleBranchMinAge = 24 * time.Hour

// BranchSweepClient is the in-tree GitHub client surface the stale-branch
// sweep needs (GH-5770): prefix-filtered branch listing and the tip commit
// date. Like StepLogClient it is a separate client sharing the daemon's token,
// because the studio-sdk client behind Controller.ghClient has neither.
// PR listing and branch deletion still go through ghClient/safeDeleteBranch.
type BranchSweepClient interface {
	ListBranches(ctx context.Context, owner, repo, prefix string) ([]ghadapter.BranchRef, error)
	GetBranch(ctx context.Context, owner, repo, branch string) (*ghadapter.Branch, error)
}

// WithBranchSweepClient wires the in-tree GitHub client used by the opt-in
// stale-branch sweep. Without it the sweep stays off even when
// Config.StaleBranchSweep is true (Run logs one WARN).
func WithBranchSweepClient(bc BranchSweepClient) ControllerOption {
	return func(c *Controller) {
		c.branchSweepClient = bc
	}
}

// staleBranchSweepInterval is the periodic sweep cadence: max(MergedPRScanWindow, 1h).
func (c *Controller) staleBranchSweepInterval() time.Duration {
	if w := c.config.MergedPRScanWindow; w > time.Hour {
		return w
	}
	return time.Hour
}

// staleBranchTracked returns the head branches of every PR autopilot tracks.
func (c *Controller) staleBranchTracked() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tracked := make(map[string]bool, len(c.activePRs))
	for _, pr := range c.activePRs {
		tracked[pr.BranchName] = true
	}
	return tracked
}

// staleBranchLiveTasks returns the task IDs an executor is working on right now
// (same sources as the orphan-running sweep).
func (c *Controller) staleBranchLiveTasks() map[string]bool {
	live := make(map[string]bool)
	if c.monitor != nil {
		for _, id := range c.monitor.GetRunningTaskIDs() {
			live[id] = true
		}
	}
	if c.dispatcherLive != nil {
		for _, id := range c.dispatcherLive.GetRunningTaskIDs() {
			live[id] = true
		}
	}
	return live
}

// sweepStaleBranches deletes orphaned pilot/* remote branches that no open PR
// references (GH-5770). The removePR/safeDeleteBranch path only fires for PRs
// autopilot tracks, so auto-preserve pushes, runs that died before
// CreatePullRequest, closed-unmerged PRs and merges missed while the daemon
// was down leave branches nobody deletes — and a stale pilot/GH-N makes the
// next executor re-pick of that issue fail non-fast-forward.
//
// Decision: PR-backed branches are judged by PR state alone (open => keep,
// closed/merged => delete); only branches with no PR at all are age-gated on
// their tip commit, fetched lazily via GetBranch so the common path costs no
// extra calls. Every failure is a WARN and the loop continues. Opt-in via
// Config.StaleBranchSweep; overlapping sweeps are dropped.
func (c *Controller) sweepStaleBranches(ctx context.Context) {
	if !c.config.StaleBranchSweep || c.ghClient == nil || c.branchSweepClient == nil {
		return
	}
	if !c.branchSweepRunning.CompareAndSwap(false, true) {
		return
	}
	defer c.branchSweepRunning.Store(false)
	if !c.backgroundScanAllowed("stale_branch_sweep") {
		return
	}

	branches, err := c.branchSweepClient.ListBranches(ctx, c.owner, c.repo, staleBranchPrefix)
	if err != nil {
		c.log.Warn("stale branch sweep: failed to list branches", "error", err)
		return
	}
	openPRs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
	if err != nil {
		// Fail-closed: without the open set we cannot tell orphans from live work.
		c.log.Warn("stale branch sweep: failed to list open PRs, skipping sweep", "error", err)
		return
	}
	closedPRs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "closed")
	if err != nil {
		c.log.Warn("stale branch sweep: failed to list closed PRs, skipping sweep", "error", err)
		return
	}

	openHeads := make(map[string]bool, len(openPRs))
	for _, pr := range openPRs {
		openHeads[pr.Head.Ref] = true
	}
	closedByHead := make(map[string]int, len(closedPRs)) // head ref -> newest PR number
	for _, pr := range closedPRs {
		if pr.Number > closedByHead[pr.Head.Ref] {
			closedByHead[pr.Head.Ref] = pr.Number
		}
	}
	tracked := c.staleBranchTracked()
	live := c.staleBranchLiveTasks()
	minAge := c.config.StaleBranchMinAge
	if minAge <= 0 {
		minAge = DefaultStaleBranchMinAge
	}

	var scanned, deleted, skippedOpen, skippedYoung, errs int
	for _, b := range branches {
		if ctx.Err() != nil {
			break
		}
		// Defense in depth: never touch anything outside the pilot/ namespace.
		if !strings.HasPrefix(b.Name, staleBranchPrefix) {
			continue
		}
		scanned++
		if openHeads[b.Name] || tracked[b.Name] || live[strings.TrimPrefix(b.Name, staleBranchPrefix)] {
			skippedOpen++
			c.log.Debug("stale branch sweep: skipping branch with open/tracked PR or live execution", "branch", b.Name)
			continue
		}

		prNumber, hasPR := closedByHead[b.Name]
		if !hasPR {
			br, gerr := c.branchSweepClient.GetBranch(ctx, c.owner, c.repo, b.Name)
			if gerr != nil {
				c.log.Warn("stale branch sweep: failed to read branch tip, skipping", "branch", b.Name, "error", gerr)
				errs++
				continue
			}
			// A missing commit date is unknown age — treat as young (fail-closed).
			if date := br.CommitDate(); date.IsZero() || time.Since(date) < minAge {
				skippedYoung++
				continue
			}
		}

		ok, derr := c.safeDeleteBranch(ctx, b.Name, prNumber)
		switch {
		case derr != nil:
			c.log.Warn("stale branch sweep: failed to delete branch", "branch", b.Name, "pr", prNumber, "error", derr)
			errs++
		case ok:
			deleted++
			c.log.Info("stale branch sweep: deleted orphaned branch", "branch", b.Name, "pr", prNumber)
		default:
			// Held by safeDeleteBranch (base of an open PR) or its check failed closed.
			skippedOpen++
		}
	}

	c.metrics.RecordStaleBranchesDeleted(deleted)
	c.log.Info(fmt.Sprintf("stale branch sweep: scanned=%d deleted=%d skipped_open=%d skipped_young=%d errors=%d",
		scanned, deleted, skippedOpen, skippedYoung, errs))
}
