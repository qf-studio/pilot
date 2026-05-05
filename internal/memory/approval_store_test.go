package memory

import (
	"os"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	tmp, err := os.MkdirTemp("", "approval-store-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	s, err := NewStore(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		t.Fatalf("NewStore: %v", err)
	}
	return s, func() {
		_ = s.Close()
		_ = os.RemoveAll(tmp)
	}
}

func TestInsertAndLoadPendingApproval(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	a := &PendingApproval{
		ID:               "req-1",
		TaskID:           "GH-100",
		Stage:            "pre_merge",
		Title:            "Approve merge",
		Description:      "Please review",
		Metadata:         map[string]interface{}{"pr_url": "https://github.com/org/repo/pull/1"},
		Approvers:        []string{"alice", "bob"},
		PreferredChannel: "telegram",
		CreatedAt:        now,
		ExpiresAt:        now.Add(24 * time.Hour),
	}

	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
	got := all[0]

	if got.ID != a.ID {
		t.Errorf("ID: got %q, want %q", got.ID, a.ID)
	}
	if got.TaskID != a.TaskID {
		t.Errorf("TaskID: got %q, want %q", got.TaskID, a.TaskID)
	}
	if got.Stage != a.Stage {
		t.Errorf("Stage: got %q, want %q", got.Stage, a.Stage)
	}
	if got.Title != a.Title {
		t.Errorf("Title: got %q, want %q", got.Title, a.Title)
	}
	if got.Description != a.Description {
		t.Errorf("Description: got %q, want %q", got.Description, a.Description)
	}
	if got.PreferredChannel != a.PreferredChannel {
		t.Errorf("PreferredChannel: got %q, want %q", got.PreferredChannel, a.PreferredChannel)
	}
	if len(got.Approvers) != 2 || got.Approvers[0] != "alice" || got.Approvers[1] != "bob" {
		t.Errorf("Approvers: got %v, want %v", got.Approvers, a.Approvers)
	}
	if v, ok := got.Metadata["pr_url"].(string); !ok || v != "https://github.com/org/repo/pull/1" {
		t.Errorf("Metadata pr_url: got %v", got.Metadata)
	}
}

func TestInsertPendingApproval_Upsert(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	a := &PendingApproval{
		ID: "req-upsert", TaskID: "GH-200", Stage: "pre_execution",
		Title: "original", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	a.Title = "updated"
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record after upsert, got %d", len(all))
	}
	if all[0].Title != "updated" {
		t.Errorf("Title after upsert: got %q, want %q", all[0].Title, "updated")
	}
}

func TestDeletePendingApproval(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"del-1", "del-2"} {
		_ = s.InsertPendingApproval(&PendingApproval{
			ID: id, TaskID: "GH-300", Stage: "pre_merge",
			Title: "t", CreatedAt: now.Add(time.Duration(i) * time.Second),
			ExpiresAt: now.Add(time.Hour),
		})
	}

	if err := s.DeletePendingApproval("del-1"); err != nil {
		t.Fatalf("DeletePendingApproval: %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record after delete, got %d", len(all))
	}
	if all[0].ID != "del-2" {
		t.Errorf("expected del-2 to remain, got %q", all[0].ID)
	}
}

func TestPrunePendingApprovals(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-2 * time.Hour)
	future := now.Add(24 * time.Hour)

	expired := &PendingApproval{
		ID: "exp-1", TaskID: "GH-400", Stage: "pre_merge",
		Title: "expired", CreatedAt: past, ExpiresAt: past.Add(time.Hour),
	}
	active := &PendingApproval{
		ID: "act-1", TaskID: "GH-401", Stage: "pre_merge",
		Title: "active", CreatedAt: now, ExpiresAt: future,
	}

	if err := s.InsertPendingApproval(expired); err != nil {
		t.Fatalf("insert expired: %v", err)
	}
	if err := s.InsertPendingApproval(active); err != nil {
		t.Fatalf("insert active: %v", err)
	}

	deleted, err := s.PrunePendingApprovals(now)
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 row deleted, got %d", deleted)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals after prune: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record remaining, got %d", len(all))
	}
	if all[0].ID != "act-1" {
		t.Errorf("expected act-1 to remain, got %q", all[0].ID)
	}
}

func TestPrunePendingApprovals_NoneExpired(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	_ = s.InsertPendingApproval(&PendingApproval{
		ID: "future-1", TaskID: "GH-500", Stage: "pre_merge",
		Title: "t", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	deleted, err := s.PrunePendingApprovals(now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 rows deleted, got %d", deleted)
	}
}

func TestLoadPendingApprovals_Empty(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 records, got %d", len(all))
	}
}

func TestInsertPendingApproval_DefaultsCreatedAt(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	before := time.Now().UTC().Add(-time.Second)
	a := &PendingApproval{
		ID:        "zero-created",
		TaskID:    "GH-600",
		Stage:     "pre_merge",
		Title:     "t",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		// CreatedAt deliberately zero
	}
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
	if all[0].CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero after insert")
	}
	if all[0].CreatedAt.Before(before) {
		t.Errorf("CreatedAt %v is before test start %v", all[0].CreatedAt, before)
	}
}

func TestInsertPendingApproval_ZeroExpiresAtReturnsError(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	a := &PendingApproval{
		ID:     "zero-expires",
		TaskID: "GH-601",
		Stage:  "pre_merge",
		Title:  "t",
		// ExpiresAt deliberately zero
	}
	err := s.InsertPendingApproval(a)
	if err == nil {
		t.Fatal("expected error for zero ExpiresAt, got nil")
	}
}

func TestInsertPendingApproval_UpsertPreservesCreatedAt(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	original := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	a := &PendingApproval{
		ID:        "freeze-created",
		TaskID:    "GH-602",
		Stage:     "pre_merge",
		Title:     "original",
		CreatedAt: original,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Re-insert with a different CreatedAt — UPSERT must not overwrite the stored value.
	a.Title = "updated"
	a.CreatedAt = time.Now().UTC()
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record after upsert, got %d", len(all))
	}
	if !all[0].CreatedAt.Equal(original) {
		t.Errorf("CreatedAt changed on upsert: got %v, want %v", all[0].CreatedAt, original)
	}
	if all[0].Title != "updated" {
		t.Errorf("Title not updated: got %q", all[0].Title)
	}
}
