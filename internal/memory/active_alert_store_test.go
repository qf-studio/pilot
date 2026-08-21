package memory

import (
	"os"
	"testing"
	"time"
)

func TestUpsertAndLoadActiveAlert(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	a := &ActiveAlert{
		RuleName:    "service_unhealthy",
		Source:      "config:github_token",
		AlertID:     "alert-1",
		AlertType:   "service_unhealthy",
		Title:       "GitHub token invalid",
		Message:     "GitHub token failed validation",
		ProjectPath: "/home/user/projects/pilot",
		Metadata:    map[string]string{"adapter": "github"},
		Channels:    []string{"slack-ops", "telegram-oncall"},
		CreatedAt:   now,
	}

	if err := s.UpsertActiveAlert(a); err != nil {
		t.Fatalf("UpsertActiveAlert: %v", err)
	}

	all, err := s.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
	got := all[0]

	if got.RuleName != a.RuleName {
		t.Errorf("RuleName: got %q, want %q", got.RuleName, a.RuleName)
	}
	if got.Source != a.Source {
		t.Errorf("Source: got %q, want %q", got.Source, a.Source)
	}
	if got.AlertID != a.AlertID {
		t.Errorf("AlertID: got %q, want %q", got.AlertID, a.AlertID)
	}
	if got.AlertType != a.AlertType {
		t.Errorf("AlertType: got %q, want %q", got.AlertType, a.AlertType)
	}
	if got.Title != a.Title {
		t.Errorf("Title: got %q, want %q", got.Title, a.Title)
	}
	if got.Message != a.Message {
		t.Errorf("Message: got %q, want %q", got.Message, a.Message)
	}
	if got.ProjectPath != a.ProjectPath {
		t.Errorf("ProjectPath: got %q, want %q", got.ProjectPath, a.ProjectPath)
	}
	if v := got.Metadata["adapter"]; v != "github" {
		t.Errorf("Metadata[adapter]: got %q, want %q", v, "github")
	}
	if !got.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, a.CreatedAt)
	}
}

// TestUpsertActiveAlert_ChannelSetPreserved is the GH-4890 channel-fidelity
// regression: the exact channel set the original alert was delivered to
// must survive a round-trip unchanged, in order, so a rehydrated resolution
// reaches the same destinations rather than being re-filtered by severity.
func TestUpsertActiveAlert_ChannelSetPreserved(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	channels := []string{"pagerduty-primary", "slack-ops", "telegram-oncall", "email-oncall"}
	a := &ActiveAlert{
		RuleName:  "service_unhealthy",
		Source:    "config:linear_token",
		AlertID:   "alert-2",
		AlertType: "service_unhealthy",
		Title:     "t",
		Message:   "m",
		Channels:  channels,
		CreatedAt: now,
	}
	if err := s.UpsertActiveAlert(a); err != nil {
		t.Fatalf("UpsertActiveAlert: %v", err)
	}

	all, err := s.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
	got := all[0].Channels
	if len(got) != len(channels) {
		t.Fatalf("Channels length: got %v, want %v", got, channels)
	}
	for i := range channels {
		if got[i] != channels[i] {
			t.Errorf("Channels[%d]: got %q, want %q", i, got[i], channels[i])
		}
	}
}

func TestUpsertActiveAlert_Upsert(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	a := &ActiveAlert{
		RuleName: "service_unhealthy", Source: "config:x",
		AlertID: "alert-3", AlertType: "service_unhealthy",
		Title: "original", Message: "m", CreatedAt: now,
	}
	if err := s.UpsertActiveAlert(a); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	a.Title = "updated"
	a.Channels = []string{"slack-ops"}
	if err := s.UpsertActiveAlert(a); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	all, err := s.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record after upsert (same rule_name+source), got %d", len(all))
	}
	if all[0].Title != "updated" {
		t.Errorf("Title after upsert: got %q, want %q", all[0].Title, "updated")
	}
	if len(all[0].Channels) != 1 || all[0].Channels[0] != "slack-ops" {
		t.Errorf("Channels after upsert: got %v, want [slack-ops]", all[0].Channels)
	}
}

func TestDeleteActiveAlert(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now().UTC().Truncate(time.Second)
	for i, src := range []string{"config:a", "config:b"} {
		if err := s.UpsertActiveAlert(&ActiveAlert{
			RuleName: "service_unhealthy", Source: src,
			AlertID: "alert", AlertType: "service_unhealthy",
			Title: "t", Message: "m", CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("UpsertActiveAlert %s: %v", src, err)
		}
	}

	if err := s.DeleteActiveAlert("service_unhealthy", "config:a"); err != nil {
		t.Fatalf("DeleteActiveAlert: %v", err)
	}

	all, err := s.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 record after delete, got %d", len(all))
	}
	if all[0].Source != "config:b" {
		t.Errorf("expected config:b to remain, got %q", all[0].Source)
	}
}

func TestLoadActiveAlerts_Empty(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	all, err := s.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 records, got %d", len(all))
	}
}

// TestActiveAlertsMigration_Idempotent verifies that re-opening the store
// (which re-runs the full migrations list, including `CREATE TABLE IF NOT
// EXISTS active_alerts`) against an existing DB tolerates the already-created
// table and preserves rows written before the reopen.
func TestActiveAlertsMigration_Idempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-active-alerts-migration-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store1.UpsertActiveAlert(&ActiveAlert{
		RuleName: "service_unhealthy", Source: "config:pre-reopen",
		AlertID: "alert-pre", AlertType: "service_unhealthy",
		Title: "t", Message: "m", Channels: []string{"slack-ops"}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertActiveAlert: %v", err)
	}
	_ = store1.Close()

	// Re-open: migration must run CREATE TABLE IF NOT EXISTS idempotently.
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (second open, post-migration): %v", err)
	}
	defer func() { _ = store2.Close() }()

	all, err := store2.LoadActiveAlerts()
	if err != nil {
		t.Fatalf("LoadActiveAlerts after reopen: %v", err)
	}
	if len(all) != 1 || all[0].Source != "config:pre-reopen" {
		t.Fatalf("post-reopen rows = %+v, want 1 row with Source=config:pre-reopen", all)
	}
}
