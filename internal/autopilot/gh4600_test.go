package autopilot

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestParkedPR_LabelCommentAlert_NoFailedTransition is the GH-4600 regression
// test for GH-4595's fix direction ("apply a visible label + PR comment
// naming the gate reason, and surface an operator alert... restart must not
// flip parked PRs to failed"). It covers both ways a PR can reach the
// misconfig branch of submitAsyncApprovalRequest — approvalMgr nil, and an
// approvalMgr that exists but has approval.pre_merge.enabled=false — and
// asserts, per tick:
//
//  1. Stage stays StageAwaitApproval (never StageFailed).
//  2. labelParkedAwaitingApproval is applied to the linked issue exactly once.
//  3. Exactly one misconfig PR comment is posted.
//  4. Exactly one operator alert (alerts.EventTypeConfigError) fires.
//  5. A second tick on the same controller does not repeat any of 2-4
//     (Parked dedupes).
//  6. A full daemon-restart replay (GH-4598: fresh controller + RestoreState)
//     rehydrates Parked/EscalationReason, stays in StageAwaitApproval, and
//     does not repeat any of 2-4 either.
func TestParkedPR_LabelCommentAlert_NoFailedTransition(t *testing.T) {
	tests := []struct {
		name        string
		approvalMgr *approval.Manager
	}{
		{
			name:        "approvalMgr nil - no approval system wired at all",
			approvalMgr: nil,
		},
		{
			name: "approvalMgr configured but pre_merge.enabled=false",
			approvalMgr: approval.NewManager(&approval.Config{
				Enabled:  true,
				PreMerge: &approval.StageConfig{Enabled: false},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, srv := newRecordingGHServer()
			defer srv.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvStage

			store := newTestStateStore(t)

			c := NewController(cfg, ghClient, tt.approvalMgr, "owner", "repo")
			c.SetStateStore(store)
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			prState := &PRState{
				PRNumber:         200,
				PRURL:            "https://github.com/owner/repo/pull/200",
				IssueNumber:      100,
				BranchName:       "pilot/GH-100",
				HeadSHA:          "sha200",
				Stage:            StageAwaitApproval,
				EscalationReason: "PR adds 656 net lines (> 500 threshold)",
			}
			c.mu.Lock()
			c.activePRs[200] = prState
			c.mu.Unlock()

			labelPath := "/repos/owner/repo/issues/100/labels"
			commentPath := "/repos/owner/repo/issues/200/comments"

			// Tick 1: the PR must park quietly — not fail — while applying the
			// one-time label/comment/alert side effects.
			if err := c.ProcessPR(context.Background(), 200, nil); err != nil {
				t.Fatalf("ProcessPR tick 1: %v", err)
			}
			if prState.Stage != StageAwaitApproval {
				t.Fatalf("after tick 1: Stage = %v, want StageAwaitApproval", prState.Stage)
			}
			if !prState.Parked {
				t.Fatalf("after tick 1: Parked = false, want true")
			}
			if n := rec.count(http.MethodPost, labelPath); n != 1 {
				t.Errorf("after tick 1: AddLabels POSTs to %s = %d, want 1", labelPath, n)
			}
			if n := rec.count(http.MethodPost, commentPath); n != 1 {
				t.Errorf("after tick 1: PR comment POSTs = %d, want 1", n)
			}
			if len(sink.events) != 1 {
				t.Fatalf("after tick 1: alert events = %d, want 1: %+v", len(sink.events), sink.events)
			}
			ev := sink.events[0]
			if ev.Type != alerts.EventTypeConfigError {
				t.Errorf("alert Type = %v, want EventTypeConfigError", ev.Type)
			}
			if ev.Metadata["pr"] != strconv.Itoa(200) {
				t.Errorf("alert Metadata[pr] = %q, want %q", ev.Metadata["pr"], "200")
			}
			if ev.Metadata["issue"] != strconv.Itoa(100) {
				t.Errorf("alert Metadata[issue] = %q, want %q", ev.Metadata["issue"], "100")
			}

			// Tick 2 on the same controller: Parked must dedupe every side effect.
			if err := c.ProcessPR(context.Background(), 200, nil); err != nil {
				t.Fatalf("ProcessPR tick 2: %v", err)
			}
			if prState.Stage != StageAwaitApproval {
				t.Errorf("after tick 2: Stage = %v, want StageAwaitApproval", prState.Stage)
			}
			if n := rec.count(http.MethodPost, labelPath); n != 1 {
				t.Errorf("after tick 2: AddLabels POSTs = %d, want still 1", n)
			}
			if n := rec.count(http.MethodPost, commentPath); n != 1 {
				t.Errorf("after tick 2: PR comment POSTs = %d, want still 1", n)
			}
			if len(sink.events) != 1 {
				t.Errorf("after tick 2: alert events = %d, want still 1", len(sink.events))
			}

			// Restart replay (GH-4598): a brand new controller loads the same
			// on-disk store fresh and re-ticks the PR exactly as the poller
			// would after a daemon restart. It must rehydrate Parked and
			// EscalationReason, stay in StageAwaitApproval (never StageFailed),
			// and must not repeat the label/comment/alert side effects.
			cAfterRestart := NewController(cfg, ghClient, tt.approvalMgr, "owner", "repo")
			cAfterRestart.SetStateStore(store)
			restartSink := &fakeAlertSink{}
			cAfterRestart.SetAlertsEngine(restartSink)

			restored, err := cAfterRestart.RestoreState()
			if err != nil {
				t.Fatalf("RestoreState: %v", err)
			}
			if restored != 1 {
				t.Fatalf("RestoreState restored %d PRs, want 1", restored)
			}

			if err := cAfterRestart.ProcessPR(context.Background(), 200, nil); err != nil {
				t.Fatalf("ProcessPR post-restart tick: %v", err)
			}

			cAfterRestart.mu.RLock()
			restoredPR, ok := cAfterRestart.activePRs[200]
			cAfterRestart.mu.RUnlock()
			if !ok {
				t.Fatalf("PR 200 not present in the restarted controller's activePRs after RestoreState")
			}

			if restoredPR.Stage != StageAwaitApproval {
				t.Errorf("post-restart Stage = %v, want StageAwaitApproval (never StageFailed)", restoredPR.Stage)
			}
			if !restoredPR.Parked {
				t.Errorf("post-restart Parked = false, want true — restart must rehydrate the parked flag")
			}
			if restoredPR.EscalationReason != prState.EscalationReason {
				t.Errorf("post-restart EscalationReason = %q, want %q", restoredPR.EscalationReason, prState.EscalationReason)
			}
			if n := rec.count(http.MethodPost, labelPath); n != 1 {
				t.Errorf("post-restart: AddLabels POSTs = %d, want still 1 (idempotent across restart)", n)
			}
			if n := rec.count(http.MethodPost, commentPath); n != 1 {
				t.Errorf("post-restart: PR comment POSTs = %d, want still 1 (idempotent across restart)", n)
			}
			if len(restartSink.events) != 0 {
				t.Errorf("post-restart: new controller's alert sink events = %d, want 0 (no re-alert on replay)", len(restartSink.events))
			}
		})
	}
}
