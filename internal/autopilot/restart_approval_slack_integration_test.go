package autopilot

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeRestartSlackClient is a minimal approval.SlackClient double used to
// drive SlackHandler through a simulated daemon restart without depending on
// package approval's unexported test mocks.
type fakeRestartSlackClient struct{}

func (f *fakeRestartSlackClient) PostInteractiveMessage(_ context.Context, msg *approval.SlackInteractiveMessage) (*approval.SlackPostMessageResponse, error) {
	return &approval.SlackPostMessageResponse{OK: true, TS: "1234.5678", Channel: msg.Channel}, nil
}

func (f *fakeRestartSlackClient) UpdateInteractiveMessage(_ context.Context, _, _ string, _ []interface{}, _ string) error {
	return nil
}

func (f *fakeRestartSlackClient) PostEphemeral(_ context.Context, _, _ string) error {
	return nil
}

// TestRestartApprovalFlow_Slack_WritesExecutionDecisionAndResumesMerge is the
// Slack counterpart of TestRestartApprovalFlow_WritesExecutionDecisionAndResumesMerge
// (GH-3825's Telegram regression test). It exercises the GH-4411 chain
// end-to-end across a simulated daemon restart, using a real SQLite-backed
// memory.Store and autopilot.StateStore (not mocks):
//
//  1. A PR reaches StageAwaitApproval and submits an async approval request;
//     approval_request_id lands on the executions row and the pending Slack
//     approval is persisted.
//  2. The daemon "restarts": a brand new Controller and approval.Manager/
//     SlackHandler are constructed and rehydrate purely from the shared
//     on-disk store — nothing from the pre-restart in-memory goroutines
//     survives.
//  3. A click on the ORIGINAL Slack message (its button value still carries
//     the pre-restart requestID verbatim — GH-4411 chose "Rehydrate" over
//     "Re-post") must still write executions.approval_decision (no waiter
//     goroutine is left to consume the old ResponseCh), and the next
//     controller tick must resume the pipeline by advancing the restored PR
//     to StageMerging.
func TestRestartApprovalFlow_Slack_WritesExecutionDecisionAndResumesMerge(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gh4411-slack-restart-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4411"
	const execID = "exec-gh4411-restart"
	if err := store.SaveExecution(&memory.Execution{
		ID:          execID,
		TaskID:      taskID,
		ProjectPath: "/tmp/gh4411-restart-proj",
		Status:      "running",
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stateStore, err := NewStateStore(store.DB())
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}

	approvalCfg := &approval.Config{
		Enabled:        true,
		DefaultTimeout: 1 * time.Hour,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       1 * time.Hour,
			DefaultAction: approval.DecisionRejected,
		},
	}

	slackClient := &fakeRestartSlackClient{}
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	// --- Pre-restart process ---
	mgr1 := approval.NewManager(approvalCfg)
	sl1 := approval.NewSlackHandler(slackClient, "#approvals").WithStore(store)
	sl1.WithDecisionRecorder(mgr1)
	mgr1.RegisterHandler(sl1)

	cfg := DefaultConfig()
	cfg.ApprovalSource = ApprovalSourceSlack

	c1 := NewController(cfg, ghClient, mgr1, "owner", "repo")
	c1.SetMemoryStore(store)
	c1.SetStateStore(stateStore)
	mgr1.WithStateWriter(c1)

	c1.mu.Lock()
	c1.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something restart-worthy",
		IssueNumber: 4411,
		Stage:       StageAwaitApproval,
		CreatedAt:   time.Now(),
	}
	c1.mu.Unlock()

	ctx := context.Background()

	// Tick 1: submits the async approval request — persists ApprovalRequestID to
	// both the PR-state store and executions.approval_request_id.
	if err := c1.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("pre-restart tick: %v", err)
	}
	pr1, ok := c1.GetPRState(42)
	if !ok || pr1.ApprovalRequestID == "" {
		t.Fatalf("expected ApprovalRequestID to be set before restart, got %+v", pr1)
	}
	requestID := pr1.ApprovalRequestID

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution before restart: %v", err)
	}
	if exec.ApprovalRequestID != requestID {
		t.Fatalf("executions.approval_request_id = %q, want %q", exec.ApprovalRequestID, requestID)
	}
	if exec.ApprovalDecision != "" {
		t.Fatalf("executions.approval_decision should be empty before any decision, got %q", exec.ApprovalDecision)
	}

	// --- Simulated daemon restart: fresh Manager, SlackHandler and Controller,
	// sharing only the on-disk store/state store. No goroutine or in-memory map
	// from c1/mgr1/sl1 survives into this section. ---
	mgr2 := approval.NewManager(approvalCfg)
	sl2 := approval.NewSlackHandler(slackClient, "#approvals").WithStore(store)
	sl2.WithDecisionRecorder(mgr2)
	mgr2.RegisterHandler(sl2)
	if err := sl2.Rehydrate(ctx); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	c2 := NewController(cfg, ghClient, mgr2, "owner", "repo")
	c2.SetMemoryStore(store)
	c2.SetStateStore(stateStore)
	mgr2.WithStateWriter(c2)

	restored, err := c2.RestoreState()
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if restored != 1 {
		t.Fatalf("RestoreState: restored %d PRs, want 1", restored)
	}

	pr2, ok := c2.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 not restored into post-restart controller")
	}
	if pr2.Stage != StageAwaitApproval || pr2.ApprovalRequestID != requestID {
		t.Fatalf("restored PR state mismatch: %+v", pr2)
	}

	// A click on the pre-restart Slack message arrives — its button value
	// still carries "approve:<requestID>" verbatim (GH-4411: rehydrate honors
	// old buttons rather than requiring a freshly-posted message).
	handled := sl2.HandleInteraction(ctx, "approve", "approve:"+requestID, "U12345", "reviewer1", "")
	if !handled {
		t.Fatal("HandleInteraction did not recognize the approval action")
	}

	// executions.approval_decision must be written by the post-restart process.
	exec, err = store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after interaction: %v", err)
	}
	if exec.ApprovalDecision != string(approval.DecisionApproved) {
		t.Fatalf("executions.approval_decision = %q, want %q", exec.ApprovalDecision, approval.DecisionApproved)
	}
	if exec.ApprovalDecisionBy != "reviewer1" {
		t.Fatalf("executions.approval_decision_by = %q, want %q", exec.ApprovalDecisionBy, "reviewer1")
	}

	// Autopilot must resume the merge pipeline on its next tick.
	if err := c2.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("post-restart tick: %v", err)
	}
	pr2, _ = c2.GetPRState(42)
	if pr2.Stage != StageMerging {
		t.Fatalf("after post-restart approval: stage = %s, want %s", pr2.Stage, StageMerging)
	}
}
