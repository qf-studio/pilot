package executor

import "sync"

// TASK-620 L2: pre-PR review counters, exported by the gateway as
// pilot_pre_pr_reviews_total{verdict} and pilot_pre_pr_fixes_total{outcome}.
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
)

// RecordPrePRReview counts one pre-PR review that ended with verdict. Called by
// runPrePRReview for every review that returned a postable verdict.
func RecordPrePRReview(verdict PostMergeVerdict) { prePRReviews.inc(string(verdict)) }

// RecordPrePRFix counts one pre-PR fix round that ended with outcome. The
// outcome labels are reserved for the fix round (L3); nothing calls this yet.
func RecordPrePRFix(outcome string) { prePRFixes.inc(outcome) }

// PrePRReviewsTotal returns the pre-PR review counts by verdict.
func PrePRReviewsTotal() map[string]int64 { return prePRReviews.snapshot() }

// PrePRFixesTotal returns the pre-PR fix counts by outcome.
func PrePRFixesTotal() map[string]int64 { return prePRFixes.snapshot() }
