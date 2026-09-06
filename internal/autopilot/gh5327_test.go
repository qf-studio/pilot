package autopilot

import (
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestReviewTriggerEligible_TableDriven is GH-5327: reviewTriggerEligible
// replaces the old ad-hoc two-stage exclusion (StageReviewRequested,
// StageFailed) with an include-list that fails closed. Every PRStage
// defined in types.go must be covered here so an unrecognized future stage
// (simulated below via an unexported literal) is proven to reject, not
// silently pass through as eligible.
func TestReviewTriggerEligible_TableDriven(t *testing.T) {
	tests := []struct {
		stage PRStage
		want  bool
	}{
		{StagePRCreated, true},
		{StageWaitingCI, true},
		{StageCIPassed, true},
		{StageCIFailed, true},
		{StageAwaitApproval, false},
		{StageMerging, false},
		{StageMerged, false},
		{StagePostMergeCI, false},
		{StageReleasing, false},
		{StageReviewRequested, false},
		{StageFailed, false},
		{PRStage("some_future_stage"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.stage), func(t *testing.T) {
			if got := reviewTriggerEligible(tt.stage); got != tt.want {
				t.Errorf("reviewTriggerEligible(%q) = %v, want %v", tt.stage, got, tt.want)
			}
		})
	}
}

// TestOnReviewRequested_StageGuard_TableDriven verifies OnReviewRequested's
// webhook path applies the same reviewTriggerEligible guard: a PR sitting in
// an ineligible stage (e.g. StageAwaitApproval or StageMerging) must not be
// yanked into StageReviewRequested by a changes_requested review, while a PR
// in an eligible stage still transitions as before.
func TestOnReviewRequested_StageGuard_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		startStage PRStage
		wantStage  PRStage
	}{
		{"eligible stage still transitions", StageWaitingCI, StageReviewRequested},
		{"awaiting approval rejects the transition", StageAwaitApproval, StageAwaitApproval},
		{"merging rejects the transition", StageMerging, StageMerging},
		{"releasing rejects the transition", StageReleasing, StageReleasing},
		{"already review_requested rejects the transition", StageReviewRequested, StageReviewRequested},
		{"failed rejects the transition", StageFailed, StageFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}
			ghClient := github.NewClient(testutil.FakeGitHubToken)
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
			pr, ok := c.GetPRState(42)
			if !ok {
				t.Fatalf("expected PR to be tracked")
			}
			pr.mu.Lock()
			pr.Stage = tt.startStage
			pr.mu.Unlock()

			c.OnReviewRequested(42, "submitted", "changes_requested", "reviewer1")

			pr, ok = c.GetPRState(42)
			if !ok {
				t.Fatalf("expected PR to remain tracked")
			}
			pr.mu.Lock()
			gotStage := pr.Stage
			pr.mu.Unlock()
			if gotStage != tt.wantStage {
				t.Errorf("stage = %q, want %q", gotStage, tt.wantStage)
			}
		})
	}
}
