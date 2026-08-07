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

// TestInsertAndLoadPendingApproval_Project is the GH-4773 round-trip
// regression test: a row's Project column must survive insert -> load
// unchanged, and a legacy row that never set Project must load back as "".
func TestInsertAndLoadPendingApproval_Project(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	a := &PendingApproval{
		ID:        "req-project",
		TaskID:    "GH-700",
		Stage:     "pre_merge",
		Title:     "Approve merge",
		Project:   "/home/user/projects/pilot",
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}
	legacy := &PendingApproval{
		ID:        "req-legacy",
		TaskID:    "GH-701",
		Stage:     "pre_merge",
		Title:     "Approve merge (pre-GH-4773)",
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := s.InsertPendingApproval(a); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}
	if err := s.InsertPendingApproval(legacy); err != nil {
		t.Fatalf("InsertPendingApproval (legacy): %v", err)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	byID := make(map[string]*PendingApproval, len(all))
	for _, row := range all {
		byID[row.ID] = row
	}

	if got := byID["req-project"]; got == nil || got.Project != "/home/user/projects/pilot" {
		t.Errorf("req-project.Project = %v, want /home/user/projects/pilot", got)
	}
	if got := byID["req-legacy"]; got == nil || got.Project != "" {
		t.Errorf("req-legacy.Project = %q, want empty (no backfill)", got.Project)
	}
}

// TestApprovalPendingProjectColumn_MigrationIdempotent verifies that
// re-opening the store (which re-runs the full migrations list, including
// the GH-4773 `ALTER TABLE approval_pending ADD COLUMN project`) against an
// existing DB tolerates the already-added column and preserves rows written
// before the reopen.
func TestApprovalPendingProjectColumn_MigrationIdempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-approval-migration-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store1.InsertPendingApproval(&PendingApproval{
		ID: "pre-reopen", TaskID: "GH-702", Stage: "pre_merge", Title: "t",
		Project: "/proj/a", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}
	_ = store1.Close()

	// Re-open: migration must run the ALTER TABLE statement idempotently
	// (the runner tolerates "duplicate column" per store.go's migration loop).
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (second open, post-migration): %v", err)
	}
	defer func() { _ = store2.Close() }()

	all, err := store2.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals after reopen: %v", err)
	}
	if len(all) != 1 || all[0].Project != "/proj/a" {
		t.Fatalf("post-reopen rows = %+v, want 1 row with Project=/proj/a", all)
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

	deleted, err := s.PrunePendingApprovals(now, []string{""})
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

	deleted, err := s.PrunePendingApprovals(now.Add(-time.Minute), []string{""})
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 rows deleted, got %d", deleted)
	}
}

// TestPrunePendingApprovals_ChannelScoped is the GH-4772 regression test:
// PrunePendingApprovals must only delete expired rows whose preferred_channel
// is in the caller's `channels` list — a Slack-scoped sweep must never touch
// an expired Telegram row (and vice versa), because the owning handler still
// needs to edit its message / record a timeout decision for it.
func TestPrunePendingApprovals_ChannelScoped(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-2 * time.Hour)

	for _, row := range []*PendingApproval{
		{ID: "tg-exp", TaskID: "GH-700", Stage: "pre_merge", Title: "t", PreferredChannel: "telegram", CreatedAt: past, ExpiresAt: past.Add(time.Hour)},
		{ID: "slack-exp", TaskID: "GH-701", Stage: "pre_merge", Title: "t", PreferredChannel: "slack", CreatedAt: past, ExpiresAt: past.Add(time.Hour)},
		{ID: "legacy-exp", TaskID: "GH-702", Stage: "pre_merge", Title: "t", PreferredChannel: "", CreatedAt: past, ExpiresAt: past.Add(time.Hour)},
	} {
		if err := s.InsertPendingApproval(row); err != nil {
			t.Fatalf("insert %s: %v", row.ID, err)
		}
	}

	// A Slack-scoped sweep must only remove the slack row.
	deleted, err := s.PrunePendingApprovals(now, []string{"slack"})
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 row deleted by slack-scoped sweep, got %d", deleted)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	remaining := make(map[string]bool, len(all))
	for _, a := range all {
		remaining[a.ID] = true
	}
	if !remaining["tg-exp"] {
		t.Error("expected telegram row to survive a slack-scoped sweep")
	}
	if !remaining["legacy-exp"] {
		t.Error("expected legacy (empty-channel) row to survive a slack-scoped sweep")
	}
	if remaining["slack-exp"] {
		t.Error("expected slack row to be deleted by a slack-scoped sweep")
	}

	// A telegram+legacy-scoped sweep (the default channel's own scope)
	// should then clean up both remaining rows.
	deleted, err = s.PrunePendingApprovals(now, []string{"telegram", ""})
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 rows deleted by telegram+legacy-scoped sweep, got %d", deleted)
	}
}

// TestPrunePendingApprovals_EmptyChannelsIsNoop verifies the documented
// "nothing to scope to" semantics: an empty channels slice deletes nothing,
// rather than falling back to unscoped delete-everything.
func TestPrunePendingApprovals_EmptyChannelsIsNoop(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-2 * time.Hour)
	_ = s.InsertPendingApproval(&PendingApproval{
		ID: "exp-noop", TaskID: "GH-703", Stage: "pre_merge", Title: "t",
		PreferredChannel: "telegram", CreatedAt: past, ExpiresAt: past.Add(time.Hour),
	})

	deleted, err := s.PrunePendingApprovals(now, nil)
	if err != nil {
		t.Fatalf("PrunePendingApprovals: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 rows deleted with empty channels, got %d", deleted)
	}
}

// TestPrunePendingApprovalsOutside_SweepsOrphanChannels is the GH-4772
// fallback test: a row whose preferred_channel matches none of the known
// handler names must still be prunable once expired.
func TestPrunePendingApprovalsOutside_SweepsOrphanChannels(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-2 * time.Hour)

	for _, row := range []*PendingApproval{
		{ID: "orphan-exp", TaskID: "GH-704", Stage: "pre_merge", Title: "t", PreferredChannel: "unknown-channel", CreatedAt: past, ExpiresAt: past.Add(time.Hour)},
		{ID: "tg-exp2", TaskID: "GH-705", Stage: "pre_merge", Title: "t", PreferredChannel: "telegram", CreatedAt: past, ExpiresAt: past.Add(time.Hour)},
	} {
		if err := s.InsertPendingApproval(row); err != nil {
			t.Fatalf("insert %s: %v", row.ID, err)
		}
	}

	deleted, err := s.PrunePendingApprovalsOutside(now, []string{"telegram", "slack", "github", "github-review"})
	if err != nil {
		t.Fatalf("PrunePendingApprovalsOutside: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 orphaned row deleted, got %d", deleted)
	}

	all, err := s.LoadPendingApprovals()
	if err != nil {
		t.Fatalf("LoadPendingApprovals: %v", err)
	}
	if len(all) != 1 || all[0].ID != "tg-exp2" {
		t.Errorf("expected only the known-channel row to remain, got %+v", all)
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
