package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// approvedOnTimeoutManager is an approval manager whose pre_merge stage expires
// almost immediately and, on expiry, APPROVES — the legal-but-dangerous config
// GH-5599 must not let release a live-smoke hold.
func approvedOnTimeoutManager() *approval.Manager {
	return approval.NewManager(&approval.Config{
		Enabled:        true,
		DefaultTimeout: time.Millisecond,
		DefaultAction:  approval.DecisionApproved,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       time.Millisecond,
			DefaultAction: approval.DecisionApproved,
		},
	})
}

// TestHandleAwaitApproval_Path3_LiveSmokeFailsClosedOnTimeout covers GH-5599:
// the controller's post-restart wall-clock guard must reject a live-smoke hold
// on expiry even with default_action: approved, while any other escalation
// still follows the configured default.
func TestHandleAwaitApproval_Path3_LiveSmokeFailsClosedOnTimeout(t *testing.T) {
	tests := []struct {
		name      string
		reason    string
		wantStage PRStage
		wantDec   approval.Decision
	}{
		{"live-smoke hold fails closed", liveSmokeReasonPrefix + "Live smoke on excerpts", StageFailed, approval.DecisionRejected},
		{"combined size-floor + live-smoke hold fails closed", "diff too large; " + liveSmokeReasonPrefix + "Live smoke", StageFailed, approval.DecisionRejected},
		{"other escalation keeps default_action", "scope drift detected", StageMerging, approval.DecisionApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), approvedOnTimeoutManager(), "owner", "repo")
			prState := &PRState{
				PRNumber: 5561, IssueNumber: 55, Stage: StageAwaitApproval,
				EscalationReason:    tt.reason,
				ApprovalRequestID:   "req-expired",
				ApprovalRequestedAt: time.Now().Add(-2 * time.Hour),
			}
			if err := c.handleAwaitApproval(context.Background(), prState); err != nil {
				t.Fatalf("handleAwaitApproval: %v", err)
			}
			if prState.Stage != tt.wantStage {
				t.Errorf("Stage = %s, want %s", prState.Stage, tt.wantStage)
			}
			if prState.ApprovalDecision != string(tt.wantDec) {
				t.Errorf("ApprovalDecision = %q, want %q", prState.ApprovalDecision, tt.wantDec)
			}
		})
	}
}

// TestSubmitAsyncApprovalRequest_LiveSmokeMetadata covers GH-5599: the request
// handed to the approval channel names the escalation reason, the bullets and
// the approving-means-you-ran-it note, and is flagged fail-closed; a routine
// escalation carries the reason but neither the note nor the flag.
func TestSubmitAsyncApprovalRequest_LiveSmokeMetadata(t *testing.T) {
	newReq := func(reason string) *approval.Request {
		mgr := asyncApprovalManager()
		handler := &mockCapturingApprovalHandler{}
		mgr.RegisterHandler(handler)
		c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), mgr, "owner", "repo")
		prState := &PRState{PRNumber: 5561, IssueNumber: 55, Stage: StageAwaitApproval, EscalationReason: reason}
		if err := c.handleAwaitApproval(context.Background(), prState); err != nil {
			t.Fatalf("handleAwaitApproval: %v", err)
		}
		if len(handler.sent) != 1 {
			t.Fatalf("sent %d requests, want 1", len(handler.sent))
		}
		return handler.sent[0]
	}

	live := newReq(liveSmokeReasonPrefix + "Live smoke on excerpts; Real API key run")
	if got, _ := live.Metadata[approval.MetaEscalationReason].(string); !strings.Contains(got, "Live smoke on excerpts") {
		t.Errorf("escalation_reason = %q, want live-smoke reason", got)
	}
	bullets, _ := live.Metadata["live_smoke_bullets"].([]string)
	if len(bullets) != 2 || bullets[0] != "Live smoke on excerpts" || bullets[1] != "Real API key run" {
		t.Errorf("live_smoke_bullets = %v", bullets)
	}
	if note, _ := live.Metadata[approval.MetaApprovalNote].(string); !strings.Contains(note, "ran") {
		t.Errorf("approval_note = %q, want the you-ran-it contract", note)
	}
	if !live.FailsClosedOnTimeout() {
		t.Error("live-smoke request must fail closed on timeout")
	}

	routine := newReq("scope drift detected")
	if got, _ := routine.Metadata[approval.MetaEscalationReason].(string); got != "scope drift detected" {
		t.Errorf("routine escalation_reason = %q", got)
	}
	if routine.FailsClosedOnTimeout() {
		t.Error("routine escalation must keep the configured timeout behaviour")
	}
	if _, ok := routine.Metadata[approval.MetaApprovalNote]; ok {
		t.Error("routine escalation must not carry the live-smoke approval note")
	}
}

// TestNotifyApprovalRequired_LiveSmokeHoldNamesReason covers GH-5599: the chat
// notification for a live-smoke hold names the reason, lists at least one
// bullet and states what approving means.
func TestNotifyApprovalRequired_LiveSmokeHoldNamesReason(t *testing.T) {
	var sent string
	notifier, tgServer := newTestNotifier(t, &sent)
	defer tgServer.Close()

	var mergeAttempted, commented atomic.Bool
	server := liveSmokeServer(t, liveSmokeBody, &mergeAttempted, &commented)
	defer server.Close()
	c := newLiveSmokeController(server)
	c.SetNotifier(notifier)

	prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed: %v", err)
	}
	if sent == "" {
		t.Fatal("no approval notification sent")
	}
	for _, want := range []string{"key-gated", "Live smoke on four labelled excerpts", "•", "Approving means YOU ran"} {
		if !strings.Contains(sent, want) {
			t.Errorf("notification missing %q:\n%s", want, sent)
		}
	}

	// A routine gate still reads as before, plus its reason.
	routine := &PRState{PRNumber: 7, EscalationReason: "scope drift detected"}
	if err := notifier.NotifyApprovalRequired(context.Background(), routine); err != nil {
		t.Fatalf("NotifyApprovalRequired: %v", err)
	}
	if !strings.Contains(sent, "scope drift detected") || strings.Contains(sent, "Approving means YOU ran") {
		t.Errorf("routine notification = %q", sent)
	}
}

// TestHandleCIPassed_ReescalationResetsStaleApproval covers GH-5599 problem 2:
// held → approved → merging → re-driven to CI → green → re-held must wait for a
// FRESH decision; the earlier DecisionApproved must not advance the PR.
func TestHandleCIPassed_ReescalationResetsStaleApproval(t *testing.T) {
	var mergeAttempted, commented atomic.Bool
	server := liveSmokeServer(t, liveSmokeBody, &mergeAttempted, &commented)
	defer server.Close()

	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoMerge = true
	cfg.RequiredChecks = []string{"build"}
	mgr := asyncApprovalManager()
	handler := &mockCapturingApprovalHandler{}
	mgr.RegisterHandler(handler)
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), mgr, "owner", "repo")
	ctx := context.Background()

	prState := &PRState{PRNumber: 5561, IssueNumber: 55, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	if err := c.handleCIPassed(ctx, prState); err != nil {
		t.Fatalf("handleCIPassed #1: %v", err)
	}
	if err := c.handleAwaitApproval(ctx, prState); err != nil { // submits request 1
		t.Fatalf("handleAwaitApproval submit: %v", err)
	}
	firstReq := prState.ApprovalRequestID
	if firstReq == "" {
		t.Fatal("first approval request not submitted")
	}

	// Human approves.
	prState.ApprovalDecision = string(approval.DecisionApproved)
	prState.ApprovalDecisionBy = "founder"
	if err := c.handleAwaitApproval(ctx, prState); err != nil {
		t.Fatalf("handleAwaitApproval approve: %v", err)
	}
	if prState.Stage != StageMerging {
		t.Fatalf("Stage = %s, want %s after approval", prState.Stage, StageMerging)
	}

	// A rebase / infra retry re-drives the PR: back through CI, green again.
	prState.Stage = StageCIPassed // StageWaitingCI → green again
	if err := c.handleCIPassed(ctx, prState); err != nil {
		t.Fatalf("handleCIPassed #2: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Fatalf("Stage = %s, want re-held in %s", prState.Stage, StageAwaitApproval)
	}
	if prState.ApprovalDecision != "" || prState.ApprovalRequestID != "" || prState.ApprovalDecisionBy != "" || !prState.ApprovalRequestedAt.IsZero() {
		t.Fatalf("approval state not reset: id=%q decision=%q by=%q at=%v",
			prState.ApprovalRequestID, prState.ApprovalDecision, prState.ApprovalDecisionBy, prState.ApprovalRequestedAt)
	}

	// Next tick: the stale approval must not release the hold; a fresh request goes out.
	if err := c.handleAwaitApproval(ctx, prState); err != nil {
		t.Fatalf("handleAwaitApproval after re-hold: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Errorf("stale approval advanced the PR: Stage = %s", prState.Stage)
	}
	if prState.ApprovalRequestID == "" || prState.ApprovalRequestID == firstReq {
		t.Errorf("want a fresh approval request, got id %q (first %q)", prState.ApprovalRequestID, firstReq)
	}
	if len(handler.sent) != 2 {
		t.Errorf("approval requests sent = %d, want 2", len(handler.sent))
	}
	if mergeAttempted.Load() {
		t.Error("PR must not merge")
	}
}

// TestHandleCIPassed_LiveSmokeHoldCommentOncePerHold covers GH-5599 problem 4:
// consecutive handleCIPassed entries for the same held PR post one comment.
func TestHandleCIPassed_LiveSmokeHoldCommentOncePerHold(t *testing.T) {
	var comments atomic.Int32
	var mergeAttempted, commented atomic.Bool
	innerServer := liveSmokeServer(t, liveSmokeBody, &mergeAttempted, &commented)
	defer innerServer.Close()
	inner := innerServer.Config.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			comments.Add(1)
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	c := newLiveSmokeController(server)

	prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	for i := 0; i < 2; i++ {
		prState.Stage = StageCIPassed
		if err := c.handleCIPassed(context.Background(), prState); err != nil {
			t.Fatalf("handleCIPassed #%d: %v", i+1, err)
		}
		if prState.Stage != StageAwaitApproval {
			t.Fatalf("entry %d: Stage = %s, want %s", i+1, prState.Stage, StageAwaitApproval)
		}
	}
	if got := comments.Load(); got != 1 {
		t.Errorf("hold comments posted = %d, want 1", got)
	}
}

// TestNotifyApprovalRequired_CombinedReasonCarriesEveryGate covers GH-5602: a
// live-smoke hold combined with another gate (size floor) must show BOTH in the
// chat message — the approver releases every gate, not just the smoke run.
func TestNotifyApprovalRequired_CombinedReasonCarriesEveryGate(t *testing.T) {
	var sent string
	notifier, tgServer := newTestNotifier(t, &sent)
	defer tgServer.Close()

	const sizeFloor = "diff below size floor"
	prState := &PRState{
		PRNumber:         5561,
		EscalationReason: sizeFloor + "; " + liveSmokeReasonPrefix + "Live smoke on four labelled excerpts",
	}
	if err := notifier.NotifyApprovalRequired(context.Background(), prState); err != nil {
		t.Fatalf("NotifyApprovalRequired: %v", err)
	}
	for _, want := range []string{sizeFloor, "Live smoke on four labelled excerpts", "Held: key-gated", "Approving means YOU ran"} {
		if !strings.Contains(sent, want) {
			t.Errorf("notification missing %q:\n%s", want, sent)
		}
	}
}

// TestHandleCIPassed_LiveSmokeHoldCommentSurvivesDroppedSegment covers GH-5602
// problem 3: re-entry whose combined reason differs only by a dropped
// non-live-smoke segment (size floor cleared by a fix push) posts no second
// hold comment; neither does re-entry after a rescind emptied EscalationReason.
func TestHandleCIPassed_LiveSmokeHoldCommentSurvivesDroppedSegment(t *testing.T) {
	var comments atomic.Int32
	var mergeAttempted, commented atomic.Bool
	innerServer := liveSmokeServer(t, liveSmokeBody, &mergeAttempted, &commented)
	defer innerServer.Close()
	inner := innerServer.Config.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			comments.Add(1)
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	c := newLiveSmokeController(server)

	// Previous hold carried an extra segment (e.g. reloaded from the state store).
	prState := &PRState{
		PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main",
		EscalationReason: "diff below size floor; " + liveSmokeReasonPrefix + "Live smoke on four labelled excerpts",
	}
	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed #1: %v", err)
	}
	if got := comments.Load(); got != 0 {
		t.Fatalf("hold comments after re-entry with dropped segment = %d, want 0", got)
	}

	// A rescind clears EscalationReason; the in-memory announced flag still dedupes.
	prState2 := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	if err := c.handleCIPassed(context.Background(), prState2); err != nil {
		t.Fatalf("handleCIPassed #2: %v", err)
	}
	prState2.EscalationReason = "" // rescindApprovalOnCIRegression
	prState2.Stage = StageCIPassed
	if err := c.handleCIPassed(context.Background(), prState2); err != nil {
		t.Fatalf("handleCIPassed #3: %v", err)
	}
	if got := comments.Load(); got != 1 {
		t.Errorf("hold comments = %d, want 1", got)
	}
}
