package memory

import (
	"context"
	"database/sql"
	"errors"
	"sync"
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

// TestSetApprovalDecision_AlreadyDecided_Guard verifies the atomic
// `AND approval_decision = ''` guard: a second decision on an
// already-decided row must not overwrite the first, and must return the
// typed ErrApprovalAlreadyDecided rather than sql.ErrNoRows (GH-4757).
func TestSetApprovalDecision_AlreadyDecided_Guard(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	const execID = "exec-guard-1"
	const reqID = "req-guard-1"
	seedExecution(t, s, execID, reqID)

	if err := s.SetApprovalDecision(context.Background(), reqID, "approved", "alice"); err != nil {
		t.Fatalf("first SetApprovalDecision: %v", err)
	}

	err := s.SetApprovalDecision(context.Background(), reqID, "rejected", "mallory")
	if !errors.Is(err, ErrApprovalAlreadyDecided) {
		t.Fatalf("second SetApprovalDecision: got %v, want ErrApprovalAlreadyDecided", err)
	}

	exec, getErr := s.GetExecution(execID)
	if getErr != nil {
		t.Fatalf("GetExecution: %v", getErr)
	}
	if exec.ApprovalDecision != "approved" {
		t.Errorf("ApprovalDecision flipped: got %q, want %q (first writer must win)", exec.ApprovalDecision, "approved")
	}
	if exec.ApprovalDecisionBy != "alice" {
		t.Errorf("ApprovalDecisionBy flipped: got %q, want %q", exec.ApprovalDecisionBy, "alice")
	}
}

// TestSetApprovalDecision_ConcurrentRace verifies that of two goroutines
// racing to decide the same request, exactly one succeeds and the other gets
// ErrApprovalAlreadyDecided — and the persisted decision matches whichever
// one actually won, never a blend or a silent overwrite (GH-4757 acceptance
// criterion 1).
func TestSetApprovalDecision_ConcurrentRace(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	const execID = "exec-race-1"
	const reqID = "req-race-1"
	seedExecution(t, s, execID, reqID)

	var wg sync.WaitGroup
	results := make([]error, 2)
	deciders := []string{"approved", "rejected"}
	by := []string{"alice", "bob"}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = s.SetApprovalDecision(context.Background(), reqID, deciders[idx], by[idx])
		}(i)
	}
	wg.Wait()

	var nilCount, alreadyDecidedCount int
	for _, err := range results {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, ErrApprovalAlreadyDecided):
			alreadyDecidedCount++
		default:
			t.Fatalf("unexpected error from racing SetApprovalDecision: %v", err)
		}
	}
	if nilCount != 1 || alreadyDecidedCount != 1 {
		t.Fatalf("got %d winners and %d already-decided, want exactly 1 and 1", nilCount, alreadyDecidedCount)
	}

	exec, err := s.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	// The recorded decision must match exactly one of the two racing writes
	// — never empty, never a mix of the two.
	if exec.ApprovalDecision != "approved" && exec.ApprovalDecision != "rejected" {
		t.Fatalf("ApprovalDecision = %q, want either approved or rejected", exec.ApprovalDecision)
	}
	if results[0] == nil && exec.ApprovalDecision != "approved" {
		t.Errorf("goroutine 0 won but recorded decision is %q, want approved", exec.ApprovalDecision)
	}
	if results[1] == nil && exec.ApprovalDecision != "rejected" {
		t.Errorf("goroutine 1 won but recorded decision is %q, want rejected", exec.ApprovalDecision)
	}
}
