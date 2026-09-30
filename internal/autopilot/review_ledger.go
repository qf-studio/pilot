package autopilot

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5494: PR review verdicts are recorded in the ledger (pr_reviews) so the
// bench can report defect rate per model. Reviewer verdicts previously lived
// only in GitHub review bodies.

const (
	// defaultReviewPollInterval throttles review listing per PR: each poll is
	// one API call per PR, and merged PRs stay in scope for 7 days.
	defaultReviewPollInterval = 10 * time.Minute

	// postMergeReviewWindow is how long after merge reviews are still
	// collected. Post-merge reviews are the common case, but not forever.
	postMergeReviewWindow = 7 * 24 * time.Hour

	// reviewSweepLookback bounds which executions are candidates for the
	// post-merge sweep (creation time, wider than the merge window because a
	// PR can sit open before it merges). The merge time from GitHub is the
	// authoritative cutoff.
	reviewSweepLookback = 14 * 24 * time.Hour
)

// reviewLedger is the subset of memory.Store used to record PR reviews.
// It is asserted off c.memoryStore rather than added to approvalPersister so
// existing approvalPersister fakes keep compiling.
type reviewLedger interface {
	UpsertPRReview(r *memory.PRReview) error
	ListPRReviewIDs(projectPath string, prNumber int) (map[int64]string, error)
	ListRecentPRExecutions(projectPath string, since time.Time) ([]*memory.Execution, error)
}

// reviewCollectorState is the in-memory throttle/cache state for review
// collection. The zero value is usable; a zero pollInterval disables the
// throttle (every tick polls).
type reviewCollectorState struct {
	mu           sync.Mutex
	pollInterval time.Duration
	lastPoll     map[int]time.Time
	mergedAt     map[int]time.Time
	done         map[int]bool
}

// due reports whether prNumber may be polled now, and stamps the poll time.
func (s *reviewCollectorState) due(prNumber int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done[prNumber] {
		return false
	}
	if s.pollInterval > 0 {
		if last, ok := s.lastPoll[prNumber]; ok && now.Sub(last) < s.pollInterval {
			return false
		}
	}
	if s.lastPoll == nil {
		s.lastPoll = make(map[int]time.Time)
	}
	s.lastPoll[prNumber] = now
	return true
}

func (s *reviewCollectorState) markDone(prNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		s.done = make(map[int]bool)
	}
	s.done[prNumber] = true
	delete(s.mergedAt, prNumber)
	delete(s.lastPoll, prNumber)
}

func (s *reviewCollectorState) cachedMergedAt(prNumber int) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.mergedAt[prNumber]
	return t, ok
}

func (s *reviewCollectorState) setMergedAt(prNumber int, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mergedAt == nil {
		s.mergedAt = make(map[int]time.Time)
	}
	s.mergedAt[prNumber] = t
}

func (c *Controller) reviewLedger() reviewLedger {
	if c.memoryStore == nil || c.ghClient == nil {
		return nil
	}
	l, _ := c.memoryStore.(reviewLedger)
	return l
}

// collectActivePRReviews records new reviews of a PR still tracked by
// autopilot. Best-effort: failures are logged, never fail the PR's cycle.
func (c *Controller) collectActivePRReviews(ctx context.Context, pr *PRState) {
	ledger := c.reviewLedger()
	if ledger == nil {
		return
	}
	pr.mu.Lock()
	prNumber, issueNumber := pr.PRNumber, pr.IssueNumber
	pr.mu.Unlock()

	if !c.reviews.due(prNumber, time.Now()) {
		return
	}

	taskID := "GH-" + strconv.Itoa(issueNumber)
	if issueNumber == 0 {
		taskID = "PR-" + strconv.Itoa(prNumber)
	}
	var executionID string
	if exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID, c.projectPath); err == nil && exec != nil {
		executionID = exec.ID
	}
	c.collectPRReviews(ctx, ledger, prNumber, executionID)
}

// sweepMergedPRReviews collects reviews for PRs that are no longer tracked
// (merged and removed) until postMergeReviewWindow after their merge. The
// candidates come from the executions ledger, since merged PRs leave the
// active set.
func (c *Controller) sweepMergedPRReviews(ctx context.Context) {
	ledger := c.reviewLedger()
	if ledger == nil || c.rateLimitCooldownActive() {
		return
	}
	now := time.Now()
	execs, err := ledger.ListRecentPRExecutions(c.projectPath, now.Add(-reviewSweepLookback))
	if err != nil {
		c.log.Warn("review ledger: listing recent PR executions failed", "error", err)
		return
	}

	seen := make(map[int]bool, len(execs))
	for _, exec := range execs {
		if ctx.Err() != nil {
			return
		}
		prNumber, ok := prNumberFromURL(exec.PRUrl, c.owner, c.repo)
		// execs are newest first: the first row for a PR is the latest one.
		if !ok || seen[prNumber] {
			continue
		}
		seen[prNumber] = true

		// Tracked PRs are collected by the active tick.
		c.mu.RLock()
		_, tracked := c.activePRs[prNumber]
		c.mu.RUnlock()
		if tracked || !c.reviews.due(prNumber, now) {
			continue
		}

		mergedAt, ok := c.reviews.cachedMergedAt(prNumber)
		if !ok {
			ghPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prNumber)
			if err != nil {
				if c.handleReviewAPIError(err, prNumber) {
					return
				}
				continue
			}
			if !ghPR.Merged {
				if ghPR.State == "closed" {
					c.reviews.markDone(prNumber) // closed unmerged: no verdict to track
				}
				continue
			}
			mergedAt, err = time.Parse(time.RFC3339, ghPR.MergedAt)
			if err != nil {
				c.log.Warn("review ledger: unparseable merged_at, skipping PR",
					"pr", prNumber, "merged_at", ghPR.MergedAt)
				continue
			}
			c.reviews.setMergedAt(prNumber, mergedAt)
		}
		if now.Sub(mergedAt) > postMergeReviewWindow {
			c.reviews.markDone(prNumber)
			continue
		}
		c.collectPRReviews(ctx, ledger, prNumber, exec.ID)
	}
}

// collectPRReviews lists a PR's reviews and upserts the ones whose ids are not
// yet in the ledger or whose verdict, re-parsed from the freshly fetched body,
// differs from the stored one (edited reviews). Pending (unsubmitted) reviews
// are skipped.
func (c *Controller) collectPRReviews(ctx context.Context, ledger reviewLedger, prNumber int, executionID string) {
	known, err := ledger.ListPRReviewIDs(c.projectPath, prNumber)
	if err != nil {
		c.log.Warn("review ledger: reading stored reviews failed", "pr", prNumber, "error", err)
		return
	}
	reviews, err := c.ghClient.ListPullRequestReviews(ctx, c.owner, c.repo, prNumber)
	if err != nil {
		c.handleReviewAPIError(err, prNumber)
		return
	}
	for _, r := range reviews {
		if r == nil || r.ID == 0 || strings.EqualFold(r.State, "PENDING") {
			continue
		}
		verdict := memory.ParseReviewVerdict(r.Body, r.State)
		stored, isKnown := known[r.ID]
		if isKnown && stored == verdict {
			continue
		}
		rec := &memory.PRReview{
			ReviewID:    r.ID,
			ProjectPath: c.projectPath,
			PRNumber:    prNumber,
			ExecutionID: executionID,
			Reviewer:    r.User.Login,
			State:       r.State,
			Verdict:     verdict,
		}
		if t, err := time.Parse(time.RFC3339, r.SubmittedAt); err == nil {
			rec.SubmittedAt = t
		}
		if err := ledger.UpsertPRReview(rec); err != nil {
			c.log.Warn("review ledger: upsert failed", "pr", prNumber, "review_id", r.ID, "error", err)
			continue
		}
		c.log.Info("recorded PR review verdict",
			"pr", prNumber, "review_id", r.ID, "state", r.State, "verdict", rec.Verdict,
			"previous_verdict", stored, "updated", isKnown)
	}
}

// handleReviewAPIError logs a GitHub failure and enters the shared rate-limit
// cooldown on a rate-limit error. It returns true when the caller should stop
// making GitHub calls this tick.
func (c *Controller) handleReviewAPIError(err error, prNumber int) bool {
	var rlErr *github.RateLimitError
	if errors.As(err, &rlErr) {
		wait := c.enterRateLimitCooldown(rlErr.RetryAfter)
		c.log.Warn("review ledger: GitHub rate limit hit, pausing until cooldown elapses",
			"pr", prNumber, "cooldown", wait, "error", err)
		return true
	}
	c.log.Warn("review ledger: GitHub call failed", "pr", prNumber, "error", err)
	return false
}

var prURLRe = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// prNumberFromURL extracts the PR number from a GitHub PR URL, only when the
// URL belongs to owner/repo.
func prNumberFromURL(prURL, owner, repo string) (int, bool) {
	m := prURLRe.FindStringSubmatch(prURL)
	if m == nil || !strings.EqualFold(m[1], owner) || !strings.EqualFold(m[2], repo) {
		return 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
