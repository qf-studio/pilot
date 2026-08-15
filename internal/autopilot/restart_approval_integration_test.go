package autopilot

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeRestartTelegramClient is a minimal approval.TelegramClient double used to
// drive TelegramHandler through a simulated daemon restart without depending on
// package approval's unexported test mocks.
type fakeRestartTelegramClient struct {
	mu     sync.Mutex
	nextID int64
}

func (f *fakeRestartTelegramClient) SendMessageWithKeyboard(_ context.Context, _, _, _ string, _ [][]approval.InlineKeyboardButton, _ int64) (*approval.MessageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	return &approval.MessageResponse{Result: &approval.MessageResult{MessageID: f.nextID}}, nil
}

func (f *fakeRestartTelegramClient) EditMessage(_ context.Context, _ string, _ int64, _, _ string) error {
	return nil
}

func (f *fakeRestartTelegramClient) AnswerCallback(_ context.Context, _, _ string) error {
	return nil
}

// TestRestartApprovalFlow_WritesExecutionDecisionAndResumesMerge exercises the
// full GH-3825 chain end-to-end across a simulated daemon restart, using a real
// SQLite-backed memory.Store and autopilot.StateStore (not mocks):
//
//  1. A PR reaches StageAwaitApproval and submits an async approval request;
//     approval_request_id lands on the executions row and the pending Telegram
//     approval is persisted.
//  2. The daemon "restarts": a brand new Controller and approval.Manager/
//     TelegramHandler are constructed and rehydrate purely from the shared
//     on-disk store — nothing from the pre-restart in-memory goroutines survives.
//  3. A Telegram button tap after restart must still write
//     executions.approval_decision (no waiter goroutine is left to consume the
//     old ResponseCh), and the next controller tick must resume the pipeline by
//     advancing the restored PR to StageMerging.
func TestRestartApprovalFlow_WritesExecutionDecisionAndResumesMerge(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gh3825-restart-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-77"
	const execID = "exec-gh3825-restart"
	if err := store.SaveExecution(&memory.Execution{
		ID:          execID,
		TaskID:      taskID,
		ProjectPath: "/tmp/gh3825-restart-proj",
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

	tgClient := &fakeRestartTelegramClient{}
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	// --- Pre-restart process ---
	mgr1 := approval.NewManager(approvalCfg)
	tg1 := approval.NewTelegramHandler(tgClient, "chat-1", 0).WithStore(store)
	tg1.WithDecisionRecorder(mgr1)
	mgr1.RegisterHandler(tg1)

	c1 := NewController(DefaultConfig(), ghClient, mgr1, "owner", "repo")
	c1.SetMemoryStore(store)
	c1.SetStateStore(stateStore)
	mgr1.WithStateWriter(c1)

	c1.mu.Lock()
	c1.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something restart-worthy",
		IssueNumber: 77,
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

	// --- Simulated daemon restart: fresh Manager, TelegramHandler and Controller,
	// sharing only the on-disk store/state store. No goroutine or in-memory map
	// from c1/mgr1/tg1 survives into this section. ---
	mgr2 := approval.NewManager(approvalCfg)
	tg2 := approval.NewTelegramHandler(tgClient, "chat-1", 0).WithStore(store)
	tg2.WithDecisionRecorder(mgr2)
	mgr2.RegisterHandler(tg2)
	if err := tg2.Rehydrate(ctx); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	c2 := NewController(DefaultConfig(), ghClient, mgr2, "owner", "repo")
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

	// A button tap arrives after restart — HandleCallback must persist the
	// decision directly (no waiter goroutine survived) rather than silently
	// dropping it.
	handled := tg2.HandleCallback(ctx, "callback-1", "approve:"+requestID, "12345", "reviewer1")
	if !handled {
		t.Fatal("HandleCallback did not recognize the approval callback")
	}

	// executions.approval_decision must be written by the post-restart process.
	exec, err = store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after callback: %v", err)
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
