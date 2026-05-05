package memory

import (
	"context"
	"os"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-approval-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	store, err := NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("NewStore: %v", err)
	}
	return store, func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

func samplePendingApproval(requestID string, expiresAt time.Time) PendingApproval {
	return PendingApproval{
		RequestID:   requestID,
		TaskID:      "TASK-01",
		Stage:       "pre_merge",
		Title:       "Deploy to production",
		Description: "merge PR #42",
		ChatID:      "chat-123",
		MessageID:   999,
		Approvers:   []string{"user-1", "user-2"},
		Metadata:    map[string]interface{}{"pr_url": "https://github.com/org/repo/pull/42"},
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now().Add(-time.Minute),
	}
}

func TestInsertAndLoadPendingApproval(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	p := samplePendingApproval("req-1", time.Now().Add(time.Hour))

	if err := store.InsertPendingApproval(ctx, p); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	rows, err := store.LoadPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	got := rows[0]
	if got.RequestID != p.RequestID {
		t.Errorf("RequestID: want %q, got %q", p.RequestID, got.RequestID)
	}
	if got.TaskID != p.TaskID {
		t.Errorf("TaskID: want %q, got %q", p.TaskID, got.TaskID)
	}
	if got.Stage != p.Stage {
		t.Errorf("Stage: want %q, got %q", p.Stage, got.Stage)
	}
	if got.ChatID != p.ChatID {
		t.Errorf("ChatID: want %q, got %q", p.ChatID, got.ChatID)
	}
	if got.MessageID != p.MessageID {
		t.Errorf("MessageID: want %d, got %d", p.MessageID, got.MessageID)
	}
	if len(got.Approvers) != len(p.Approvers) {
		t.Errorf("Approvers: want %v, got %v", p.Approvers, got.Approvers)
	}
	if got.Metadata["pr_url"] != p.Metadata["pr_url"] {
		t.Errorf("Metadata pr_url: want %v, got %v", p.Metadata["pr_url"], got.Metadata["pr_url"])
	}
	// ExpiresAt round-trips through unix seconds
	if got.ExpiresAt.Unix() != p.ExpiresAt.Unix() {
		t.Errorf("ExpiresAt: want %v, got %v", p.ExpiresAt.Unix(), got.ExpiresAt.Unix())
	}
}

func TestInsertPendingApproval_Upsert(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	p := samplePendingApproval("req-upsert", time.Now().Add(time.Hour))

	if err := store.InsertPendingApproval(ctx, p); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Update the message ID and re-insert (upsert).
	p.MessageID = 12345
	if err := store.InsertPendingApproval(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := store.LoadPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(rows))
	}
	if rows[0].MessageID != 12345 {
		t.Errorf("expected updated MessageID 12345, got %d", rows[0].MessageID)
	}
}

func TestDeletePendingApproval(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	p := samplePendingApproval("req-del", time.Now().Add(time.Hour))

	if err := store.InsertPendingApproval(ctx, p); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.DeletePendingApproval(ctx, "req-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := store.LoadPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows after delete, got %d", len(rows))
	}
}

func TestDeletePendingApproval_Idempotent(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	// Deleting a nonexistent row must not return an error.
	if err := store.DeletePendingApproval(ctx, "nonexistent"); err != nil {
		t.Errorf("delete nonexistent: expected nil, got %v", err)
	}
}

func TestPrunePendingApprovals(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// Insert one expired and one active row.
	expired := samplePendingApproval("req-expired", now.Add(-time.Minute))
	active := samplePendingApproval("req-active", now.Add(time.Hour))

	if err := store.InsertPendingApproval(ctx, expired); err != nil {
		t.Fatalf("insert expired: %v", err)
	}
	if err := store.InsertPendingApproval(ctx, active); err != nil {
		t.Fatalf("insert active: %v", err)
	}

	n, err := store.PrunePendingApprovals(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 pruned row, got %d", n)
	}

	rows, err := store.LoadPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("load after prune: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 remaining row, got %d", len(rows))
	}
	if rows[0].RequestID != "req-active" {
		t.Errorf("expected req-active to remain, got %q", rows[0].RequestID)
	}
}

func TestLoadPendingApprovals_Empty(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	rows, err := store.LoadPendingApprovals(context.Background())
	if err != nil {
		t.Fatalf("LoadPendingApprovals on empty table: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestLoadPendingApprovals_NilMetadata(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	p := samplePendingApproval("req-nil-meta", time.Now().Add(time.Hour))
	p.Metadata = nil

	if err := store.InsertPendingApproval(ctx, p); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := store.LoadPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// Nil metadata should not cause a panic.
	_ = rows[0].Metadata
}
