package executor

import (
	"context"
	"log/slog"
	"time"
)

// GH-6037: the acceptance evidence runs before the pre-PR review and the review
// reads it as the PR body, so a failing evidence command is a defect the review
// (and its fix round) sees instead of one only the post-merge reviewer can flag.

// AcceptanceEvidenceCollection is one run of the acceptance-evidence step: the
// rendered "## Evidence" / "## Not verified" sections and the per-item results
// behind them.
type AcceptanceEvidenceCollection struct {
	// Section is the rendered evidence text; "" when there is nothing to report.
	Section string
	// Results are the per-item outcomes, paste-output command results included.
	Results []AcceptanceEvidenceResult
	// Duration is the wall-clock time of the collection.
	Duration time.Duration
}

// section is the rendered evidence text; "" for a nil collection.
func (e *AcceptanceEvidenceCollection) section() string {
	if e == nil {
		return ""
	}
	return e.Section
}

// seconds is the measured collection time in seconds; 0 for a nil collection.
func (e *AcceptanceEvidenceCollection) seconds() float64 {
	if e == nil {
		return 0
	}
	return e.Duration.Seconds()
}

// prePRReviewTimeBudget is the context time a pre-PR review can use: every pass
// gets the full timeout (GH-5980).
func (r *Runner) prePRReviewTimeBudget() time.Duration {
	return time.Duration(r.config.PrePRReview.EffectivePasses()) * r.config.PrePRReview.EffectiveTimeout()
}

// collectAcceptanceEvidenceForReview runs the acceptance classification and the
// paste-output commands once, ahead of the pre-PR review, and returns the
// collection the review reads and the PR body reuses (appendAcceptanceEvidence).
// It returns nil, leaving the evidence to run at PR creation as before, when the
// review will not run, when the gate is disabled, or when ctx has less time left
// than the evidence budget plus the review budget — the review then proceeds
// without the evidence.
func (r *Runner) collectAcceptanceEvidenceForReview(ctx context.Context, task *Task, workDir string, gates *QualityGatesResult) *AcceptanceEvidenceCollection {
	if r == nil || task == nil || r.config == nil || r.config.PrePRReviewMode() == PrePRReviewModeOff {
		return nil
	}
	if r.prePRReviewer == nil || !task.CreatePR || task.DirectCommit || task.Branch == "" {
		return nil
	}
	if (gates == nil || !gates.AllPassed) && r.qualityCheckerFactory != nil {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		need := r.config.EffectiveAcceptanceEvidenceBudget() + r.prePRReviewTimeBudget()
		if left := time.Until(deadline); left < need {
			if r.log != nil {
				r.log.Info("Pre-PR review: evidence not ready",
					slog.String("task_id", task.ID),
					slog.Duration("left", left.Round(time.Second)),
					slog.Duration("need", need),
				)
			}
			return nil
		}
	}
	return r.collectAcceptanceEvidence(ctx, task, workDir, gates)
}

// evidenceAfterFixRound returns the evidence the PR body carries once the fix
// round ended. A round that ended ReviewFixFixed moved HEAD, so the first
// result describes a tree that is no longer pushed: the paste-output commands
// run again on the fixed SHA and the first result is discarded. The gate output
// is not reused, because it was captured before the fix. Any other ending leaves
// HEAD where it was and the first result stands; with no first result the
// evidence runs at PR creation, on the fixed tree.
func (r *Runner) evidenceAfterFixRound(ctx context.Context, task *Task, workDir string, fix *ReviewFixOutcome, first *AcceptanceEvidenceCollection) *AcceptanceEvidenceCollection {
	if first == nil || fix == nil || fix.Status != ReviewFixFixed {
		return first
	}
	return r.collectAcceptanceEvidence(ctx, task, workDir, nil)
}
