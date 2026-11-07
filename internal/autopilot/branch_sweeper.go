package autopilot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
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

// Reasons a swept branch is held (GH-6181), logged as reason= on the
// "stale branch sweep: kept branch" line.
const (
	staleKeepFixIssue    = "fix_issue"
	staleKeepPendingTask = "pending_task"
	staleKeepYoung       = "young"
)

// DispatcherQueueView is the optional queue-side companion of
// DispatcherLiveness: the task IDs the dispatcher holds as queued or pending
// (not yet running) for one project — task IDs collide across repos. The Dispatcher passed to SetDispatcherLiveness satisfies
// it, so the sweep picks it up without extra wiring.
type DispatcherQueueView interface {
	GetQueuedTaskIDs(projectPath string) []string
}

// BorrowedBranchLister is the non-destructive listing side of the
// borrowed-branch registry (*executor.Runner): every branch a fix issue is
// currently recorded as continuing. Optional on BorrowedBranchLookup.
type BorrowedBranchLister interface {
	BorrowedBranches() []string
}

// staleAutopilotMetaRe finds every autopilot-meta comment in an issue body and
// staleMetaBranchRe the branch field inside one.
//
// Decision: deliberately more permissive than the dispatcher's footer parser
// (cmd/pilot/handlers.go extractAutopilotMetaFooter, last-line only, outside
// code spans): that parser decides where work RUNS and must reject quoted
// examples; this one decides what is KEPT, where a false positive only delays a
// delete by a tick and a false negative loses a fix issue's branch.
var (
	staleAutopilotMetaRe = regexp.MustCompile(`(?s)<!--\s*autopilot-meta\b(.*?)-->`)
	staleMetaBranchRe    = regexp.MustCompile(`\bbranch:(\S+)`)
)

// autopilotMetaBranches returns every pilot/* branch named by an
// autopilot-meta comment in body.
func autopilotMetaBranches(body string) []string {
	var out []string
	for _, m := range staleAutopilotMetaRe.FindAllStringSubmatch(body, -1) {
		if b := staleMetaBranchRe.FindStringSubmatch(m[1]); len(b) == 2 && strings.HasPrefix(b[1], staleBranchPrefix) {
			out = append(out, b[1])
		}
	}
	return out
}

// staleBranchLiveTasks returns the task IDs an executor is working on right now
// (same sources as the orphan-running sweep) and, separately, the IDs the
// dispatcher still holds as queued/pending (GH-6181): a queued task has no
// worker yet, but its branch must survive until it runs.
func (c *Controller) staleBranchLiveTasks() (running, queued map[string]bool) {
	running = make(map[string]bool)
	queued = make(map[string]bool)
	if c.monitor != nil {
		for _, id := range c.monitor.GetRunningTaskIDs() {
			running[id] = true
		}
	}
	if c.dispatcherLive != nil {
		for _, id := range c.dispatcherLive.GetRunningTaskIDs() {
			running[id] = true
		}
		if qv, ok := c.dispatcherLive.(DispatcherQueueView); ok {
			for _, id := range qv.GetQueuedTaskIDs(c.projectPath) {
				queued[id] = true
			}
		}
	}
	return running, queued
}

// staleBranchReferencedByFixIssues returns the branches an open autopilot-fix
// issue still points at via its autopilot-meta footer, plus every branch in the
// borrowed-branch registry (GH-6181). A fix issue continues the failed PR's
// branch after autopilot closed that PR, so the branch must outlive the closed
// PR for as long as the fix issue is open. An error means the set is unknown;
// the caller must skip the sweep (fail-closed).
func (c *Controller) staleBranchReferencedByFixIssues(ctx context.Context) (map[string]bool, error) {
	issues, err := c.ghClient.ListIssues(ctx, c.owner, c.repo, &github.ListIssuesOptions{
		Labels: []string{"autopilot-fix"},
		State:  github.StateOpen,
	})
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool)
	for _, issue := range issues {
		for _, b := range autopilotMetaBranches(issue.Body) {
			held[b] = true
		}
	}
	// The registry entry is dropped when the fix issue closes (GH-5927), so
	// what is left belongs to issues that are still open.
	if lister, ok := c.borrowedBranchLookup.(BorrowedBranchLister); ok {
		for _, b := range lister.BorrowedBranches() {
			held[b] = true
		}
	}
	return held, nil
}

// staleBranchReferencedByPending returns the branches of queued/pending tasks:
// each task's own pilot/<id> branch plus any branch named by the autopilot-meta
// footer of its issue (a hand-filed revision issue carries the footer without
// the autopilot-fix label, GH-5379). An error means the set is unknown.
func (c *Controller) staleBranchReferencedByPending(ctx context.Context, queued map[string]bool) (map[string]bool, error) {
	held := make(map[string]bool, len(queued))
	for id := range queued {
		held[staleBranchPrefix+id] = true
		num, ok := strings.CutPrefix(id, "GH-")
		if !ok {
			continue
		}
		n, convErr := strconv.Atoi(num)
		if convErr != nil || n <= 0 {
			continue
		}
		issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, n)
		if err != nil {
			return nil, fmt.Errorf("get issue %d: %w", n, err)
		}
		for _, b := range autopilotMetaBranches(issue.Body) {
			held[b] = true
		}
	}
	return held, nil
}

// closedPRTime is when a closed PR last changed state. The SDK's PullRequest
// carries no closed_at, so this is the later of updated_at and merged_at: any
// activity after the close only makes the PR look younger, which is the safe
// direction. Zero means unknown.
func closedPRTime(pr *github.PullRequest) time.Time {
	var t time.Time
	for _, raw := range []string{pr.UpdatedAt, pr.MergedAt} {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil && ts.After(t) {
			t = ts
		}
	}
	return t
}

// sweepStaleBranches deletes orphaned pilot/* remote branches that no open PR
// references (GH-5770). The removePR/safeDeleteBranch path only fires for PRs
// autopilot tracks, so auto-preserve pushes, runs that died before
// CreatePullRequest, closed-unmerged PRs and merges missed while the daemon
// was down leave branches nobody deletes — and a stale pilot/GH-N makes the
// next executor re-pick of that issue fail non-fast-forward.
//
// Decision: an open PR keeps its branch; so does an open autopilot-fix issue or
// a queued/pending task that still references it (GH-6181: autopilot closes a
// failed PR and the fix issue continues the same branch, so a closed PR alone
// is not proof the branch is dead). Both age gates then apply: a closed-PR
// branch is held until its PR has been closed for minAge, a no-PR branch until
// its tip commit is minAge old (fetched lazily via GetBranch so the common path
// costs no extra calls). Failing to build any protection set skips the whole
// sweep. Other failures are a WARN and the loop continues. Opt-in via
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
		c.log.Warn("stale branch sweep: failed to list branches", "repo", c.repoKey(), "error", err)
		return
	}
	openPRs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
	if err != nil {
		// Fail-closed: without the open set we cannot tell orphans from live work.
		c.log.Warn("stale branch sweep: failed to list open PRs, skipping sweep", "repo", c.repoKey(), "error", err)
		return
	}
	closedPRs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "closed")
	if err != nil {
		c.log.Warn("stale branch sweep: failed to list closed PRs, skipping sweep", "repo", c.repoKey(), "error", err)
		return
	}

	openHeads := make(map[string]bool, len(openPRs))
	for _, pr := range openPRs {
		openHeads[pr.Head.Ref] = true
	}
	closedByHead := make(map[string]*github.PullRequest, len(closedPRs)) // head ref -> newest closed PR
	for _, pr := range closedPRs {
		if cur := closedByHead[pr.Head.Ref]; cur == nil || pr.Number > cur.Number {
			closedByHead[pr.Head.Ref] = pr
		}
	}
	tracked := c.staleBranchTracked()
	live, queued := c.staleBranchLiveTasks()
	fixRefs, err := c.staleBranchReferencedByFixIssues(ctx)
	if err != nil {
		c.log.Warn("stale branch sweep: failed to list open autopilot-fix issues, skipping sweep", "repo", c.repoKey(), "error", err)
		return
	}
	pendingRefs, err := c.staleBranchReferencedByPending(ctx, queued)
	if err != nil {
		c.log.Warn("stale branch sweep: failed to read queued task issues, skipping sweep", "repo", c.repoKey(), "error", err)
		return
	}
	minAge := c.config.StaleBranchMinAge
	if minAge <= 0 {
		minAge = DefaultStaleBranchMinAge
	}

	var scanned, deleted, skippedOpen, skippedYoung, skippedFixIssue, skippedPending, errs int
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
			c.log.Debug("stale branch sweep: skipping branch with open/tracked PR or live execution", "repo", c.repoKey(), "branch", b.Name)
			continue
		}

		lastClosed, hasPR := closedByHead[b.Name]
		prNumber := 0
		if hasPR {
			prNumber = lastClosed.Number
		}
		keep := func(reason string) {
			c.log.Info("stale branch sweep: kept branch", "repo", c.repoKey(), "branch", b.Name, "pr", prNumber, "reason", reason)
		}
		if fixRefs[b.Name] {
			skippedFixIssue++
			keep(staleKeepFixIssue)
			continue
		}
		if pendingRefs[b.Name] {
			skippedPending++
			keep(staleKeepPendingTask)
			continue
		}
		if hasPR {
			// A missing close time is unknown age — treat as young (fail-closed).
			if t := closedPRTime(lastClosed); t.IsZero() || time.Since(t) < minAge {
				skippedYoung++
				keep(staleKeepYoung)
				continue
			}
		} else {
			br, gerr := c.branchSweepClient.GetBranch(ctx, c.owner, c.repo, b.Name)
			if gerr != nil {
				c.log.Warn("stale branch sweep: failed to read branch tip, skipping", "repo", c.repoKey(), "branch", b.Name, "error", gerr)
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
			c.log.Warn("stale branch sweep: failed to delete branch", "repo", c.repoKey(), "branch", b.Name, "pr", prNumber, "error", derr)
			errs++
		case ok:
			deleted++
			c.log.Info("stale branch sweep: deleted orphaned branch", "repo", c.repoKey(), "branch", b.Name, "pr", prNumber)
		default:
			// Held by safeDeleteBranch (base of an open PR) or its check failed closed.
			skippedOpen++
		}
	}

	c.metrics.RecordStaleBranchesDeleted(deleted)
	c.log.Info(fmt.Sprintf("stale branch sweep: scanned=%d deleted=%d skipped_open=%d skipped_young=%d skipped_fix_issue=%d skipped_pending_task=%d errors=%d",
		scanned, deleted, skippedOpen, skippedYoung, skippedFixIssue, skippedPending, errs), "repo", c.repoKey())
}
