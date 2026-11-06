package executor

import "sync"

// TASK-620 L2: pre-PR review counters, exported by the gateway as
// pilot_pre_pr_reviews_total{verdict} and pilot_pre_pr_fixes_total{outcome} and
// pilot_pre_pr_holds_total{project}.
// Process-lifetime; they reset to 0 on restart.

// prePRCounter is a mutex-guarded label -> count map.
type prePRCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (c *prePRCounter) inc(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = make(map[string]int64)
	}
	c.counts[label]++
}

func (c *prePRCounter) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.counts))
	for k, v := range c.counts {
		out[k] = v
	}
	return out
}

var (
	prePRReviews prePRCounter
	prePRFixes   prePRCounter
	prePRHolds   prePRCounter
)

// RecordPrePRReview counts one pre-PR review that ended with verdict. Called by
// runPrePRReview for every review that returned a postable verdict.
func RecordPrePRReview(verdict PostMergeVerdict) { prePRReviews.inc(string(verdict)) }

// RecordPrePRFix counts one pre-PR fix round that ended with outcome (a
// ReviewFixStatus). Called by runReviewFix.
func RecordPrePRFix(outcome string) { prePRFixes.inc(outcome) }

// RecordPrePRHold counts one pre-PR review hold (a run ended needs_human with
// unfixed blocking defects) for project. An empty project is recorded as
// "unknown" so the series is never label-less.
func RecordPrePRHold(project string) {
	if project == "" {
		project = "unknown"
	}
	prePRHolds.inc(project)
}

// PrePRReviewsTotal returns the pre-PR review counts by verdict.
func PrePRReviewsTotal() map[string]int64 { return prePRReviews.snapshot() }

// PrePRFixesTotal returns the pre-PR fix counts by outcome.
func PrePRFixesTotal() map[string]int64 { return prePRFixes.snapshot() }

// PrePRHoldsTotal returns the pre-PR hold counts by project.
func PrePRHoldsTotal() map[string]int64 { return prePRHolds.snapshot() }
