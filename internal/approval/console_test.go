package approval

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// failingTransport fails the test on any outbound HTTP request.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("console handler made an outbound call: %s", r.URL)
	return nil, errors.New("outbound call refused")
}

func consoleTestRequest(id string, expires time.Time) *Request {
	return &Request{
		ID:               id,
		TaskID:           "GH-6290",
		Stage:            StagePreMerge,
		Title:            "Merge PR #7",
		Description:      "needs a human",
		Metadata:         map[string]interface{}{"pr_number": 7},
		Approvers:        []string{"alice"},
		PreferredChannel: "console",
		Project:          "/work/proj",
		CreatedAt:        time.Now(),
		ExpiresAt:        expires,
	}
}

func TestConsoleHandler_Name(t *testing.T) {
	if got := NewConsoleHandler().Name(); got != "console" {
		t.Errorf("Name() = %q, want console", got)
	}
}

func TestConsoleHandler_SendInsertsOneRowAndNoOutboundCall(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = failingTransport{t}
	t.Cleanup(func() { http.DefaultTransport = old })

	store := newMockPendingStore()
	h := NewConsoleHandler().WithStore(store)
	req := consoleTestRequest("req-1", time.Now().Add(time.Hour))

	ch, err := h.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	if store.len() != 1 {
		t.Fatalf("rows = %d, want 1", store.len())
	}
	row := store.get("req-1")
	if row == nil {
		t.Fatal("row req-1 not stored")
	}
	if row.TaskID != req.TaskID || row.Stage != string(req.Stage) || row.Title != req.Title ||
		row.Description != req.Description || row.Project != req.Project ||
		row.PreferredChannel != "console" || len(row.Approvers) != 1 || row.Approvers[0] != "alice" ||
		!row.CreatedAt.Equal(req.CreatedAt) || !row.ExpiresAt.Equal(req.ExpiresAt) {
		t.Errorf("row = %+v, does not mirror request %+v", row, req)
	}
	if n, ok := row.Metadata["pr_number"]; !ok || n != 7 {
		t.Errorf("metadata = %v, want pr_number 7", row.Metadata)
	}

	select {
	case resp, ok := <-ch:
		t.Fatalf("response channel delivered (%v, %v), want silence", resp, ok)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestConsoleHandler_SendWithoutStoreFails(t *testing.T) {
	if _, err := NewConsoleHandler().SendApprovalRequest(context.Background(), consoleTestRequest("r", time.Now().Add(time.Hour))); err == nil {
		t.Fatal("want an error when no store is attached")
	}
}

func TestConsoleHandler_SendInsertErrorFails(t *testing.T) {
	store := newMockPendingStore()
	store.insertErr = errors.New("disk full")
	h := NewConsoleHandler().WithStore(store)
	if _, err := h.SendApprovalRequest(context.Background(), consoleTestRequest("r", time.Now().Add(time.Hour))); err == nil {
		t.Fatal("want an error when the row cannot be stored")
	}
}

func TestConsoleHandler_CancelDeletesRow(t *testing.T) {
	store := newMockPendingStore()
	h := NewConsoleHandler().WithStore(store)
	if _, err := h.SendApprovalRequest(context.Background(), consoleTestRequest("req-1", time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	if err := h.CancelRequest(context.Background(), "req-1"); err != nil {
		t.Fatalf("CancelRequest: %v", err)
	}
	if store.len() != 0 {
		t.Errorf("rows = %d after cancel, want 0", store.len())
	}
	if err := h.CancelRequest(context.Background(), "req-1"); err != nil {
		t.Errorf("second CancelRequest = %v, want nil", err)
	}
}

func TestConsoleHandler_PruneExpiredDeletesOnlyExpiredConsoleRows(t *testing.T) {
	store := newMockPendingStore()
	h := NewConsoleHandler().WithStore(store)
	for id, exp := range map[string]time.Time{
		"expired": time.Now().Add(-time.Minute),
		"live":    time.Now().Add(time.Hour),
	} {
		if _, err := h.SendApprovalRequest(context.Background(), consoleTestRequest(id, exp)); err != nil {
			t.Fatalf("SendApprovalRequest(%s): %v", id, err)
		}
	}
	other := consoleTestRequest("tg-expired", time.Now().Add(-time.Minute))
	other.PreferredChannel = "telegram"
	if err := store.InsertPendingApproval(&memory.PendingApproval{ID: other.ID, PreferredChannel: "telegram", ExpiresAt: other.ExpiresAt}); err != nil {
		t.Fatal(err)
	}

	n, err := h.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	if store.get("expired") != nil || store.get("live") == nil || store.get("tg-expired") == nil {
		t.Errorf("rows after prune: expired=%v live=%v tg-expired=%v, want only the expired console row gone",
			store.get("expired"), store.get("live"), store.get("tg-expired"))
	}
}

// The default channel's orphan sweep must not delete console rows.
func TestConsoleChannelIsKnownAndValid(t *testing.T) {
	found := false
	for _, n := range knownChannelNames {
		if n == "console" {
			found = true
		}
	}
	if !found {
		t.Error("console missing from knownChannelNames: the telegram orphan sweep would delete its rows")
	}
	if !ApprovalSourceValues["console"] {
		t.Error("console is not a valid approval_source value")
	}
	if NormalizeChannelName("console") != "console" {
		t.Error("console must not be aliased")
	}
}

func TestConsoleHandler_DecisionThroughManagerResolvesRequest(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.PreMerge.Enabled = true
	cfg.PreMerge.Timeout = time.Minute
	writer := &mockPRStateWriter{}
	m := NewManager(cfg).WithStateWriter(writer)
	store := newMockPendingStore()
	m.RegisterHandler(NewConsoleHandler().WithStore(store))

	req := consoleTestRequest("req-1", time.Time{})
	id, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitApprovalRequest: %v", err)
	}
	if store.get(id) == nil {
		t.Fatal("request not stored for the console")
	}
	if got := len(m.GetPendingRequests()); got != 1 {
		t.Fatalf("manager pending = %d, want 1", got)
	}

	if err := m.RecordDecision(context.Background(), id, DecisionApproved, "console-user"); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if len(writer.calls) != 1 || writer.calls[0].requestID != id || writer.calls[0].decision != string(DecisionApproved) || writer.calls[0].by != "console-user" {
		t.Errorf("state writer calls = %+v, want one approved by console-user", writer.calls)
	}
	if got := len(m.GetPendingRequests()); got != 0 {
		t.Errorf("manager pending = %d after decision, want 0", got)
	}
	// The waiter's cancellation withdraws the row through CancelRequest.
	deadline := time.Now().Add(2 * time.Second)
	for store.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.len() != 0 {
		t.Errorf("rows = %d after decision, want 0", store.len())
	}
}
