package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_SetApprovalDecision_RecordsDecisionBy_GH4823 is TASK-459
// Phase 4 task 4b's acceptance test for the "real" decision path (an operator
// HTTP webhook or a Telegram/Slack channel-tap, both of which flow through
// approval.Manager into Controller.SetApprovalDecision with a caller-supplied
// "by" identity — see approval_decision_race_test.go's production-wiring
// coverage for the race-arbitration side of this same method).
// applyApprovalDecision used to have zero visibility into who made the
// decision it was about to act on; SetApprovalDecision now mirrors "by" onto
// PRState.ApprovalDecisionBy so that evidence survives into the gate and its
// logs. Table-driven over both the memoryStore-backed path and the no-store
// fast path (test wiring / a controller with no memory.Store), since
// SetApprovalDecision has two independent write sites.
func TestController_SetApprovalDecision_RecordsDecisionBy_GH4823(t *testing.T) {
	t.Run("memoryStore-backed path", func(t *testing.T) {
		tmpDir := t.TempDir()
		store, err := memory.NewStore(tmpDir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer func() { _ = store.Close() }()

		const reqID = "req-gh4823-store"
		if err := store.SaveExecution(&memory.Execution{
			ID:                "exec-gh4823-store",
			TaskID:            "GH-4823",
			ProjectPath:       "/tmp/gh4823-store",
			Status:            "running",
			ApprovalRequestID: reqID,
		}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}

		ghClient := github.NewClient(testutil.FakeGitHubToken)
		cfg := DefaultConfig()
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		c.memoryStore = store

		c.mu.Lock()
		c.activePRs[61] = &PRState{PRNumber: 61, IssueNumber: 4823, ApprovalRequestID: reqID}
		c.mu.Unlock()

		if err := c.SetApprovalDecision(context.Background(), reqID, string(approval.DecisionApproved), "telegram:alice"); err != nil {
			t.Fatalf("SetApprovalDecision: %v", err)
		}

		pr, ok := c.GetPRState(61)
		if !ok {
			t.Fatal("PR state missing after SetApprovalDecision")
		}
		if pr.ApprovalDecisionBy != "telegram:alice" {
			t.Errorf("ApprovalDecisionBy = %q, want %q", pr.ApprovalDecisionBy, "telegram:alice")
		}
	})

	t.Run("no memory store: in-memory fast path", func(t *testing.T) {
		ghClient := github.NewClient(testutil.FakeGitHubToken)
		cfg := DefaultConfig()
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		// c.memoryStore left nil deliberately — exercises SetApprovalDecision's
		// other write site (no backing store to arbitrate a race).

		c.mu.Lock()
		c.activePRs[62] = &PRState{PRNumber: 62, IssueNumber: 4824, ApprovalRequestID: "req-gh4823-nostore"}
		c.mu.Unlock()

		if err := c.SetApprovalDecision(context.Background(), "req-gh4823-nostore", string(approval.DecisionRejected), "webhook:bob"); err != nil {
			t.Fatalf("SetApprovalDecision: %v", err)
		}

		pr, ok := c.GetPRState(62)
		if !ok {
			t.Fatal("PR state missing after SetApprovalDecision")
		}
		if pr.ApprovalDecisionBy != "webhook:bob" {
			t.Errorf("ApprovalDecisionBy = %q, want %q", pr.ApprovalDecisionBy, "webhook:bob")
		}
	})
}
