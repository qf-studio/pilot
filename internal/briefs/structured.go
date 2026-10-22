package briefs

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// reviewOverdueAfter is the age past which a review item is listed first.
const reviewOverdueAfter = 24 * time.Hour

// Failure classes reported in the failed section.
const (
	FailureTimeout   = "timeout"
	FailureRateLimit = "rate_limit"
	FailureNetwork   = "network"
	FailureAuth      = "auth"
	FailureConflict  = "conflict"
	FailureCI        = "ci_failed"
	FailureUnknown   = "unknown"
)

// ReviewInput is a pending item the structured brief lists under
// review_needed. Reason is reduced to one line when the brief is built.
type ReviewInput struct {
	IssueRef     string
	Title        string
	PRURL        string
	Reason       string
	WaitingSince time.Time
}

// BuildStructured assembles a StructuredBrief from terminal executions
// (completed and failed), pending review items and the optional pool usage
// percentage (nil when the daemon does not know it). It is pure so section
// ordering and the cost rule are testable without a store.
func BuildStructured(now time.Time, period BriefPeriod, execs []*memory.Execution, reviews []ReviewInput, poolPercent *float64) *StructuredBrief {
	b := &StructuredBrief{
		Schema:       StructuredBriefSchema,
		Kind:         KindDaily,
		GeneratedAt:  now,
		PeriodStart:  period.Start,
		PeriodEnd:    period.End,
		Shipped:      []ShippedRun{},
		ReviewNeeded: []ReviewItem{},
		Failed:       []FailedRun{},
	}

	delivered := make(map[string]bool)
	for _, e := range execs {
		if e.Status == "completed" {
			delivered[e.TaskID] = true
		}
	}

	for _, e := range execs {
		switch e.Status {
		case "completed":
			title := e.TaskTitle
			if title == "" {
				title = e.TaskID
			}
			run := ShippedRun{
				IssueRef:     receiptIssueRef(e),
				Title:        title,
				PRURL:        e.PRUrl,
				DurationMs:   e.DurationMs,
				LinesAdded:   e.LinesAdded,
				LinesRemoved: e.LinesRemoved,
				KeySource:    memory.NormalizeKeySource(e.KeySource),
			}
			// Decision: cost is shown only on the tenant's own key. Platform
			// runs are covered by the included allowance, and "unknown" is
			// treated like platform so a mis-provisioned box never leaks a
			// cost the tenant is not billed for.
			if run.KeySource == memory.KeySourceOwn {
				cost := e.EstimatedCostUSD
				run.CostUSD = &cost
			}
			b.Shipped = append(b.Shipped, run)
		case "failed":
			if delivered[e.TaskID] {
				continue // retried and shipped later
			}
			class, retryable := ClassifyFailure(e.Error)
			b.Failed = append(b.Failed, FailedRun{
				IssueRef:     receiptIssueRef(e),
				FailureClass: class,
				Retryable:    retryable,
			})
		}
	}

	for _, r := range reviews {
		b.ReviewNeeded = append(b.ReviewNeeded, ReviewItem{
			IssueRef:     r.IssueRef,
			Title:        r.Title,
			PRURL:        r.PRURL,
			Reason:       oneLine(r.Reason),
			WaitingSince: r.WaitingSince,
			Overdue:      now.Sub(r.WaitingSince) > reviewOverdueAfter,
		})
	}
	// Overdue items first; within each group the longest-waiting first.
	sort.SliceStable(b.ReviewNeeded, func(i, j int) bool {
		a, c := b.ReviewNeeded[i], b.ReviewNeeded[j]
		if a.Overdue != c.Overdue {
			return a.Overdue
		}
		return a.WaitingSince.Before(c.WaitingSince)
	})

	if poolPercent != nil {
		b.Pool = &PoolUsage{PercentUsed: *poolPercent}
	}
	return b
}

// MarshalPayload returns the console JSON payload.
func (b *StructuredBrief) MarshalPayload() ([]byte, error) {
	return json.Marshal(b)
}

// ClassifyFailure maps a run's error text to a coarse failure class and
// whether retrying is likely to help. Anything unrecognized is
// FailureUnknown and not retryable, so it surfaces for a human.
func ClassifyFailure(errText string) (class string, retryable bool) {
	e := strings.ToLower(errText)
	switch {
	case containsAny(e, "rate limit", "rate_limit", "429", "overloaded", "quota"):
		return FailureRateLimit, true
	case containsAny(e, "timed out", "timeout", "deadline exceeded"):
		return FailureTimeout, true
	case containsAny(e, "connection refused", "connection reset", "no such host", "network", "eof", "503", "502"):
		return FailureNetwork, true
	case containsAny(e, "unauthorized", "401", "403", "forbidden", "authentication", "invalid api key"):
		return FailureAuth, false
	case containsAny(e, "merge conflict", "conflict"):
		return FailureConflict, false
	case containsAny(e, "ci failed", "checks failed", "test failed", "build failed", "lint failed"):
		return FailureCI, false
	}
	return FailureUnknown, false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// oneLine returns the first non-empty line of s, trimmed and capped.
func oneLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 140 {
			line = line[:140] + "..."
		}
		return line
	}
	return ""
}
