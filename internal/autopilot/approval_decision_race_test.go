package autopilot

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestApproval_ProductionWiring_ConcurrentDecision_ExactlyOneWinner is the
// GH-4777 acceptance test: two decisions race for the same approval request
// through the PRODUCTION wiring — approval.Manager.RecordDecision calling
// Controller.SetApprovalDecision (exactly the composition cmd/pilot/main.go
// sets up via approvalMgr.WithStateWriter(controller)) backed by a real
// memory.Store, not a test-only mock writer.
//
// Before this fix, Controller.SetApprovalDecision warn-logged and swallowed
// every store error (including memory.ErrApprovalAlreadyDecided), so this
// exact composition made the store's atomic race guard dead code in
// production: both racing callers always got a nil error (the gateway's
// equivalent of a 200), and prState.ApprovalDecision was last-writer-wins.
//
// This test asserts:
//  1. Exactly one call returns nil (the gateway's 200) and exactly one
//     returns an error wrapping memory.ErrApprovalAlreadyDecided (the
//     gateway's 409).
//  2. prState.ApprovalDecision (the actionable field autopilot's ProcessPR
//     loop reads) matches the WINNER's decision — it never flips to the
//     loser's value and is never a blend of both.
//  3. The persisted executions row agrees with prState — the two never
//     diverge.
func TestApproval_ProductionWiring_ConcurrentDecision_ExactlyOneWinner(t *testing.T) {
	const reqID = "req-gh4777-race"
	const execID = "exec-gh4777-race"

	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID:                execID,
		TaskID:            "GH-4777",
		ProjectPath:       "/tmp/gh4777-race",
		Status:            "running",
		ApprovalRequestID: reqID,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	mgr := approval.NewManager(nil)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")
	c.memoryStore = store

	// GH-2685-style production wiring: the controller is the Manager's state
	// writer, exactly as cmd/pilot/main.go wires the single-controller daemon
	// mode (approvalMgr.WithStateWriter(gwAutopilotController)).
	mgr.WithStateWriter(c)

	c.mu.Lock()
	c.activePRs[55] = &PRState{
		PRNumber:          55,
		IssueNumber:       4777,
		ApprovalRequestID: reqID,
	}
	c.mu.Unlock()

	deciders := []struct {
		decision approval.Decision
		by       string
	}{
		{approval.DecisionApproved, "alice"},
		{approval.DecisionRejected, "bob"},
	}

	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range deciders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = mgr.RecordDecision(context.Background(), reqID, deciders[idx].decision, deciders[idx].by)
		}(i)
	}
	wg.Wait()

	var nilCount, alreadyDecidedCount int
	var winnerIdx = -1
	for i, err := range results {
		switch {
		case err == nil:
			nilCount++
			winnerIdx = i
		case errors.Is(err, memory.ErrApprovalAlreadyDecided):
			alreadyDecidedCount++
		default:
			t.Fatalf("unexpected error from racing RecordDecision: %v", err)
		}
	}
	if nilCount != 1 || alreadyDecidedCount != 1 {
		t.Fatalf("got %d winners (200) and %d already-decided (409), want exactly 1 and 1", nilCount, alreadyDecidedCount)
	}

	winnerDecision := string(deciders[winnerIdx].decision)

	pr, ok := c.GetPRState(55)
	if !ok {
		t.Fatal("PR state not found")
	}
	if pr.ApprovalDecision != winnerDecision {
		t.Errorf("prState.ApprovalDecision = %q, want winner's decision %q (must never flip to the loser's value)", pr.ApprovalDecision, winnerDecision)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.ApprovalDecision != winnerDecision {
		t.Errorf("persisted ApprovalDecision = %q, want winner's decision %q", exec.ApprovalDecision, winnerDecision)
	}
	if exec.ApprovalDecision != pr.ApprovalDecision {
		t.Errorf("persisted decision %q diverges from in-memory PRState decision %q", exec.ApprovalDecision, pr.ApprovalDecision)
	}
}

// TestApproval_ProductionWiring_MultiController_PropagatesAlreadyDecided
// verifies the multi-repo composition (MultiControllerStateWriter, wired by
// cmd/pilot/main.go when len(autopilotControllers) > 0) propagates the same
// typed error a single-controller deployment does — a controller that
// doesn't own requestID must not mask the error returned by the controller
// that does.
func TestApproval_ProductionWiring_MultiController_PropagatesAlreadyDecided(t *testing.T) {
	const reqID = "req-gh4777-multi"
	const execID = "exec-gh4777-multi"

	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID:                execID,
		TaskID:            "GH-4777",
		ProjectPath:       "/tmp/gh4777-multi",
		Status:            "running",
		ApprovalRequestID: reqID,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	mgr := approval.NewManager(nil)

	// otherRepo doesn't own reqID's PR — it must fall through to noopRepo's
	// controller without masking its result.
	otherRepo := NewController(DefaultConfig(), ghClient, mgr, "owner", "other-repo")
	owningRepo := NewController(DefaultConfig(), ghClient, mgr, "owner", "repo")
	owningRepo.memoryStore = store

	owningRepo.mu.Lock()
	owningRepo.activePRs[56] = &PRState{
		PRNumber:          56,
		IssueNumber:       4778,
		ApprovalRequestID: reqID,
	}
	owningRepo.mu.Unlock()

	mgr.WithStateWriter(NewMultiControllerStateWriter(otherRepo, owningRepo))

	if err := mgr.RecordDecision(context.Background(), reqID, approval.DecisionApproved, "alice"); err != nil {
		t.Fatalf("first RecordDecision: %v", err)
	}

	err = mgr.RecordDecision(context.Background(), reqID, approval.DecisionRejected, "bob")
	if !errors.Is(err, memory.ErrApprovalAlreadyDecided) {
		t.Fatalf("second RecordDecision: got %v, want memory.ErrApprovalAlreadyDecided", err)
	}

	pr, ok := owningRepo.GetPRState(56)
	if !ok {
		t.Fatal("PR state not found")
	}
	if pr.ApprovalDecision != "approved" {
		t.Errorf("prState.ApprovalDecision = %q, want %q (must never flip)", pr.ApprovalDecision, "approved")
	}
}
