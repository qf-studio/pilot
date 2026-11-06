package executor_test

import (
	"testing"

	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/executor"
)

// TestPrePRHold_ReasonTextIsClassifierSafe: the hold reason is substring-matched
// by briefs.ClassifyFailure and by the executor's outcome classifier table, so
// it must not change with the fix status ("regate_timeout" would read as a
// timeout and "reverted" fixes as a failed run) and must stay unclassified. It
// lives in the external test package because internal/briefs imports executor.
func TestPrePRHold_ReasonTextIsClassifierSafe(t *testing.T) {
	review := &executor.PrePRReviewOutcome{
		Mode:    executor.PrePRReviewModeEnforce,
		Verdict: executor.PostMergeRequestChanges,
		Review: &executor.PostMergeReview{
			Verdict: executor.PostMergeRequestChanges,
			Defects: []executor.PostMergeDefect{{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: "x"}},
		},
	}
	base, hold := executor.PrePRHoldReasonForTest(review, nil, executor.PrePRReviewConfig{})
	if !hold || base == "" {
		t.Fatalf("no hold for a nil fix: %q, %v", base, hold)
	}
	statuses := []executor.ReviewFixStatus{
		executor.ReviewFixReverted, executor.ReviewFixRegateError, executor.ReviewFixRegateTimeout,
		executor.ReviewFixFailed, executor.ReviewFixNoChange, executor.ReviewFixSkippedCtx, executor.ReviewFixSkippedBudget,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			reason, hold := executor.PrePRHoldReasonForTest(review, &executor.ReviewFixOutcome{Status: status}, executor.PrePRReviewConfig{})
			if !hold {
				t.Fatal("no hold")
			}
			if reason != base {
				t.Errorf("reason = %q, want it identical to %q whatever the fix status", reason, base)
			}
			if class, retryable := briefs.ClassifyFailure(reason); class != briefs.FailureUnknown || retryable {
				t.Errorf("ClassifyFailure(%q) = %q, %v, want unknown and not retryable", reason, class, retryable)
			}
			// No outcome signature may reclassify an untagged result either.
			if got := executor.TerminalStatus(&executor.ExecutionResult{Error: reason}); got != "failed" {
				t.Errorf("TerminalStatus(untagged %q) = %q, want the plain failed fallthrough", reason, got)
			}
			if got := executor.TerminalStatus(&executor.ExecutionResult{Outcome: "needs_human", Error: reason}); got != "needs_human" {
				t.Errorf("TerminalStatus(needs_human) = %q", got)
			}
		})
	}
}
