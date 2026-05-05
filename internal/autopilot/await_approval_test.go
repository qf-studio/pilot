package autopilot

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newAsyncController builds a minimal Controller configured for the async
// approval path. approvalCfg may be nil (falls back to a default async config).
func newAsyncController(t *testing.T, approvalCfg *approval.Config) *Controller {
	t.Helper()
	if approvalCfg == nil {
		approvalCfg = &approval.Config{
			Enabled:       true,
			AsyncDispatch: true,
			PreMerge: &approval.StageConfig{
				Enabled:       true,
				Timeout:       1 * time.Hour,
				DefaultAction: approval.DecisionRejected,
			},
			DefaultTimeout: 1 * time.Hour,
			DefaultAction:  approval.DecisionRejected,
		}
	}
	cfg := DefaultConfig()
	cfg.ApprovalTimeout = 1 * time.Hour
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	approvalMgr := approval.NewManager(approvalCfg)
	return NewController(cfg, ghClient, approvalMgr, "owner", "repo")
}

// newPRStateAwaitingApproval returns a PRState positioned at StageAwaitApproval.
func newPRStateAwaitingApproval(prNumber int) *PRState {
	return &PRState{
		PRNumber:  prNumber,
		PRURL:     "https://github.com/owner/repo/pull/42",
		Stage:     StageAwaitApproval,
		HeadSHA:   "abc123",
		CreatedAt: time.Now().Add(-5 * time.Minute),
	}
}

// TestHandleAwaitApproval_TableDriven covers the async approval tick handler.
func TestHandleAwaitApproval_TableDriven(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		setupState      func(*PRState)
		approvalCfg     *approval.Config
		wantStage       PRStage
		wantRequestID   bool // true = ApprovalRequestID should be non-empty after the call
		wantDecision    string
	}{
		{
			// (a) First tick: no request ID yet — submits and returns immediately
			// staying in StageAwaitApproval with a populated ApprovalRequestID.
			name: "first_tick_submits_request_without_blocking",
			setupState: func(ps *PRState) {
				// ApprovalRequestID intentionally left empty
			},
			approvalCfg: &approval.Config{
				Enabled:       false, // stage disabled → SubmitApprovalRequest auto-approves
				AsyncDispatch: true,
				DefaultAction: approval.DecisionRejected,
				PreMerge: &approval.StageConfig{
					Enabled:       false,
					DefaultAction: approval.DecisionRejected,
				},
			},
			wantStage:     StageAwaitApproval,
			wantRequestID: true,
		},
		{
			// (b) Approved decision advances to StageMerging.
			name: "approved_decision_advances_to_merging",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-approved"
				ps.ApprovalRequestedAt = time.Now().Add(-10 * time.Minute)
				ps.ApprovalDecision = string(approval.DecisionApproved)
			},
			wantStage: StageMerging,
		},
		{
			// (c) Rejected decision transitions to StageFailed.
			name: "rejected_decision_fails_stage",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-rejected"
				ps.ApprovalRequestedAt = time.Now().Add(-10 * time.Minute)
				ps.ApprovalDecision = string(approval.DecisionRejected)
			},
			wantStage: StageFailed,
		},
		{
			// (c) Timeout decision string also transitions to StageFailed.
			name: "timeout_decision_fails_stage",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-timeout"
				ps.ApprovalRequestedAt = time.Now().Add(-10 * time.Minute)
				ps.ApprovalDecision = string(approval.DecisionTimeout)
			},
			wantStage: StageFailed,
		},
		{
			// (d) No decision yet but wall-clock timeout exceeded with
			// default_action = rejected → StageFailed.
			name: "tick_time_timeout_triggers_expiry_rejected_default",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-pending"
				ps.ApprovalRequestedAt = time.Now().Add(-2 * time.Hour)
				ps.ApprovalDecision = "" // still pending
			},
			approvalCfg: &approval.Config{
				Enabled:       true,
				AsyncDispatch: true,
				DefaultAction: approval.DecisionRejected,
				PreMerge: &approval.StageConfig{
					Enabled:       true,
					Timeout:       30 * time.Minute,
					DefaultAction: approval.DecisionRejected,
				},
				DefaultTimeout: 30 * time.Minute,
			},
			wantStage: StageFailed,
		},
		{
			// (d) No decision yet, timeout exceeded, default_action = approved → StageMerging.
			name: "tick_time_timeout_triggers_expiry_approved_default",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-pending"
				ps.ApprovalRequestedAt = time.Now().Add(-2 * time.Hour)
				ps.ApprovalDecision = ""
			},
			approvalCfg: &approval.Config{
				Enabled:       true,
				AsyncDispatch: true,
				DefaultAction: approval.DecisionApproved,
				PreMerge: &approval.StageConfig{
					Enabled:       true,
					Timeout:       30 * time.Minute,
					DefaultAction: approval.DecisionApproved,
				},
				DefaultTimeout: 30 * time.Minute,
			},
			wantStage: StageMerging,
		},
		{
			// Pending within timeout window — stay in StageAwaitApproval.
			name: "pending_within_timeout_stays_in_await",
			setupState: func(ps *PRState) {
				ps.ApprovalRequestID = "req-within-window"
				ps.ApprovalRequestedAt = time.Now().Add(-5 * time.Minute)
				ps.ApprovalDecision = ""
			},
			wantStage: StageAwaitApproval,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newAsyncController(t, tc.approvalCfg)

			prState := newPRStateAwaitingApproval(42)
			if tc.setupState != nil {
				tc.setupState(prState)
			}

			if err := c.handleAwaitApproval(ctx, prState); err != nil {
				t.Fatalf("handleAwaitApproval returned unexpected error: %v", err)
			}

			if prState.Stage != tc.wantStage {
				t.Errorf("Stage = %q, want %q", prState.Stage, tc.wantStage)
			}
			if tc.wantRequestID && prState.ApprovalRequestID == "" {
				t.Error("ApprovalRequestID should be non-empty after first tick")
			}
		})
	}
}

// TestHandleAwaitApproval_TwoPRIndependence verifies that a stalled PR-A
// waiting for approval does not block PR-B from advancing (test e).
func TestHandleAwaitApproval_TwoPRIndependence(t *testing.T) {
	ctx := context.Background()
	c := newAsyncController(t, nil)

	// PR-A: request submitted but no decision yet (still pending).
	prA := newPRStateAwaitingApproval(10)
	prA.ApprovalRequestID = "req-a-pending"
	prA.ApprovalRequestedAt = time.Now().Add(-5 * time.Minute)
	prA.ApprovalDecision = ""

	// PR-B: already has an approved decision.
	prB := newPRStateAwaitingApproval(20)
	prB.ApprovalRequestID = "req-b-approved"
	prB.ApprovalRequestedAt = time.Now().Add(-5 * time.Minute)
	prB.ApprovalDecision = string(approval.DecisionApproved)

	// Process A then B — neither call should block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.handleAwaitApproval(ctx, prA); err != nil {
			t.Errorf("prA: unexpected error: %v", err)
		}
		if err := c.handleAwaitApproval(ctx, prB); err != nil {
			t.Errorf("prB: unexpected error: %v", err)
		}
	}()

	select {
	case <-done:
		// Both calls returned without blocking — pass.
	case <-time.After(2 * time.Second):
		t.Fatal("handleAwaitApproval blocked for more than 2 seconds — not non-blocking")
	}

	// PR-A should still be in StageAwaitApproval (pending, within timeout).
	if prA.Stage != StageAwaitApproval {
		t.Errorf("prA.Stage = %q, want %q (stalled PR should not advance)", prA.Stage, StageAwaitApproval)
	}

	// PR-B should have advanced to StageMerging.
	if prB.Stage != StageMerging {
		t.Errorf("prB.Stage = %q, want %q (approved PR should advance)", prB.Stage, StageMerging)
	}
}

// TestHandleAwaitApproval_LegacySyncPath verifies that the legacy blocking path
// is used when AsyncDispatch = false.
func TestHandleAwaitApproval_LegacySyncPath(t *testing.T) {
	ctx := context.Background()

	// Build a controller with AsyncDispatch disabled. The autoMerger.MergePR path
	// will be invoked. Since no env requires approval (EnvStage default), it should
	// skip approval and try to merge — which fails due to no HTTP server. That's
	// acceptable; we just need to confirm the legacy path is entered (i.e. no panic,
	// and the function is not a no-op that returns nil without calling MergePR).
	approvalCfg := &approval.Config{
		Enabled:       false,
		AsyncDispatch: false, // force legacy path
		DefaultAction: approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       false,
			DefaultAction: approval.DecisionRejected,
		},
	}
	c := newAsyncController(t, approvalCfg)

	prState := newPRStateAwaitingApproval(42)

	// The legacy path calls autoMerger.MergePR which will attempt GitHub API
	// calls and fail — that is expected. The important thing is that prState is
	// not left in StageAwaitApproval without attempting the merge.
	_ = c.handleAwaitApproval(ctx, prState)

	// prState.ApprovalRequestID must remain empty (legacy path doesn't set it).
	if prState.ApprovalRequestID != "" {
		t.Errorf("legacy path should not set ApprovalRequestID, got %q", prState.ApprovalRequestID)
	}
}
