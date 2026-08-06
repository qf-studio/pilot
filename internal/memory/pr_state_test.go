package memory

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// seedExecution inserts a minimal execution row with the given ID and optional
// approval_request_id. Uses newTestStore (defined in approval_store_test.go).
func seedExecution(t *testing.T, s *Store, execID, approvalRequestID string) {
	t.Helper()
	exec := &Execution{
		ID:                execID,
		TaskID:            "GH-2667",
		ProjectPath:       "/tmp/test-proj",
		Status:            "running",
		ApprovalRequestID: approvalRequestID,
	}
	if err := s.SaveExecution(exec); err != nil {
		t.Fatalf("seedExecution: %v", err)
	}
}

func TestSetApprovalDecision_RoundTrip(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	const execID = "exec-approval-1"
	const reqID = "req-abc"

	seedExecution(t, s, execID, reqID)

	before := time.Now().UTC().Truncate(time.Second)
	if err := s.SetApprovalDecision(context.Background(), reqID, "approved", "alice"); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	exec, err := s.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}

	if exec.ApprovalRequestID != reqID {
		t.Errorf("ApprovalRequestID: got %q, want %q", exec.ApprovalRequestID, reqID)
	}
	if exec.ApprovalDecision != "approved" {
		t.Errorf("ApprovalDecision: got %q, want %q", exec.ApprovalDecision, "approved")
	}
	if exec.ApprovalDecisionBy != "alice" {
		t.Errorf("ApprovalDecisionBy: got %q, want %q", exec.ApprovalDecisionBy, "alice")
	}
	if exec.ApprovalDecisionAt == nil {
		t.Fatal("ApprovalDecisionAt: expected non-nil")
	}
	if exec.ApprovalDecisionAt.Before(before) || exec.ApprovalDecisionAt.After(after) {
		t.Errorf("ApprovalDecisionAt %v out of expected range [%v, %v]",
			exec.ApprovalDecisionAt, before, after)
	}
}

func TestSetApprovalDecision_Rejected(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	seedExecution(t, s, "exec-rej", "req-rej")

	if err := s.SetApprovalDecision(context.Background(), "req-rej", "rejected", "bob"); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}

	exec, err := s.GetExecution("exec-rej")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.ApprovalDecision != "rejected" {
		t.Errorf("ApprovalDecision: got %q, want rejected", exec.ApprovalDecision)
	}
	if exec.ApprovalDecisionBy != "bob" {
		t.Errorf("ApprovalDecisionBy: got %q, want bob", exec.ApprovalDecisionBy)
	}
}

func TestSetApprovalDecision_NoMatchingRow(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	err := s.SetApprovalDecision(context.Background(), "nonexistent-req", "approved", "system")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestApprovalRequestID_PersistedOnSave(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	const execID = "exec-persist"
	const reqID = "req-persist"
	seedExecution(t, s, execID, reqID)

	exec, err := s.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.ApprovalRequestID != reqID {
		t.Errorf("ApprovalRequestID after save: got %q, want %q", exec.ApprovalRequestID, reqID)
	}
	// Decision fields should be zero until SetApprovalDecision is called.
	if exec.ApprovalDecision != "" {
		t.Errorf("ApprovalDecision should be empty before decision, got %q", exec.ApprovalDecision)
	}
	if exec.ApprovalDecisionAt != nil {
		t.Errorf("ApprovalDecisionAt should be nil before decision")
	}
}

func TestApprovalDecision_EmptyRequestID_NoRows(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	// Execution without an approval_request_id should not be matched.
	seedExecution(t, s, "exec-no-req", "")

	err := s.SetApprovalDecision(context.Background(), "", "approved", "system")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for empty requestID, got %v", err)
	}
}

func TestGetExecutionByApprovalRequestID_Found(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	const execID = "exec-by-req-1"
	const reqID = "req-by-exec-1"
	seedExecution(t, s, execID, reqID)

	exec, err := s.GetExecutionByApprovalRequestID(reqID)
	if err != nil {
		t.Fatalf("GetExecutionByApprovalRequestID: %v", err)
	}
	if exec.ID != execID {
		t.Errorf("ID: got %q, want %q", exec.ID, execID)
	}
	if exec.ApprovalRequestID != reqID {
		t.Errorf("ApprovalRequestID: got %q, want %q", exec.ApprovalRequestID, reqID)
	}
	if exec.ProjectPath != "/tmp/test-proj" {
		t.Errorf("ProjectPath: got %q, want /tmp/test-proj", exec.ProjectPath)
	}
}

func TestGetExecutionByApprovalRequestID_NotFound(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	if _, err := s.GetExecutionByApprovalRequestID("no-such-request"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetExecutionByApprovalRequestID_EmptyRequestID(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	// Every non-approval execution defaults approval_request_id to '' — an
	// empty requestID must never match one of those rows.
	seedExecution(t, s, "exec-empty-req", "")

	if _, err := s.GetExecutionByApprovalRequestID(""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for empty requestID, got %v", err)
	}
}
