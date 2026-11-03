package memory

import (
	"testing"
	"time"
)

func TestBillingModeNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"subscription": "subscription", "api_key": "api_key", "": "unknown", "key": "unknown", "API_KEY": "unknown",
	} {
		if got := NormalizeBillingMode(in); got != want {
			t.Errorf("NormalizeBillingMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBillingModeExecutionRoundTrip: billing_mode is written at insert, junk
// and empty values read back unknown, and key_source is untouched (GH-6016).
func TestBillingModeExecutionRoundTrip(t *testing.T) {
	s := newChatStore(t)
	for _, e := range []*Execution{
		{ID: "api-1", TaskID: "t1", ProjectPath: "/p", Status: "running", KeySource: "own", BillingMode: "api_key"},
		{ID: "sub-1", TaskID: "t2", ProjectPath: "/p", Status: "running", BillingMode: "subscription"},
		{ID: "junk-1", TaskID: "t3", ProjectPath: "/p", Status: "running", BillingMode: "sk-whatever"},
		{ID: "none-1", TaskID: "t4", ProjectPath: "/p", Status: "running"},
	} {
		if err := s.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}
	for id, want := range map[string]string{"api-1": "api_key", "sub-1": "subscription", "junk-1": "unknown", "none-1": "unknown"} {
		got, err := s.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if got.BillingMode != want {
			t.Errorf("%s BillingMode = %q, want %q", id, got.BillingMode, want)
		}
	}
	if got, _ := s.GetExecution("api-1"); got.KeySource != "own" {
		t.Errorf("api-1 KeySource = %q, want own", got.KeySource)
	}
}

// TestBillingModeLegacyRowReadsUnknown: a row inserted without the column (as
// before this change) reads back unknown.
func TestBillingModeLegacyRowReadsUnknown(t *testing.T) {
	s := newChatStore(t)
	if err := s.SaveExecution(&Execution{ID: "old-1", TaskID: "t", ProjectPath: "/p", Status: "completed", BillingMode: "api_key"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	// A row written before the column existed holds NULL.
	if _, err := s.db.Exec(`UPDATE executions SET billing_mode = NULL WHERE id = 'old-1'`); err != nil {
		t.Fatalf("null out billing_mode: %v", err)
	}
	got, err := s.GetExecution("old-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.BillingMode != "unknown" {
		t.Errorf("legacy BillingMode = %q, want unknown", got.BillingMode)
	}
}

func TestBillingModeChatTurnRoundTrip(t *testing.T) {
	s := newChatStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	api := chatRow("c1", "/p", "own", 0.2, 10, 5, now)
	api.BillingMode = "api_key"
	if err := s.SaveChatTurn(api); err != nil {
		t.Fatalf("SaveChatTurn: %v", err)
	}
	if err := s.SaveChatTurn(chatRow("c2", "/p", "own", 0.2, 10, 5, now.Add(time.Second))); err != nil {
		t.Fatalf("SaveChatTurn: %v", err)
	}
	rows, err := s.ListChatTurnsSince(now.Add(-time.Hour), "", 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListChatTurnsSince = %d rows, err %v", len(rows), err)
	}
	if rows[0].BillingMode != "api_key" || rows[1].BillingMode != "unknown" {
		t.Errorf("chat billing modes = %q, %q; want api_key, unknown", rows[0].BillingMode, rows[1].BillingMode)
	}
	usage, _, err := s.GetUsageSince(now.Add(-time.Hour), "")
	if err != nil || len(usage) != 2 {
		t.Fatalf("GetUsageSince = %d rows, err %v", len(usage), err)
	}
	if usage[0].BillingMode != "api_key" {
		t.Errorf("merged usage row BillingMode = %q, want api_key", usage[0].BillingMode)
	}
}

// TestBillingModeUsageEventsCopiedFromExecution: RecordTaskUsage copies the
// execution's mode onto each per-task event.
func TestBillingModeUsageEventsCopiedFromExecution(t *testing.T) {
	s := newChatStore(t)
	if err := s.SaveExecution(&Execution{ID: "e1", TaskID: "t1", ProjectPath: "/p", Status: "completed", BillingMode: "api_key"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := s.RecordTaskUsage("e1", "u", "/p", 120000, 1000, 500); err != nil {
		t.Fatalf("RecordTaskUsage: %v", err)
	}
	if err := s.RecordTaskUsage("missing", "u", "/p", 0, 0, 0); err != nil {
		t.Fatalf("RecordTaskUsage(missing): %v", err)
	}
	events, err := s.GetUsageEvents(UsageQuery{Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)}, 100)
	if err != nil {
		t.Fatalf("GetUsageEvents: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 (task+token+compute for e1, task for missing)", len(events))
	}
	for _, e := range events {
		want := "api_key"
		if e.ExecutionID == "missing" {
			want = "unknown"
		}
		if e.BillingMode != want {
			t.Errorf("event %s BillingMode = %q, want %q", e.ID, e.BillingMode, want)
		}
	}
}

func TestBillingModeLifetimeBaselineSplit(t *testing.T) {
	s := newChatStore(t)
	for _, e := range []*Execution{
		{ID: "a", KeySource: "own", BillingMode: "api_key"},
		{ID: "b", KeySource: "own", BillingMode: "subscription"},
		{ID: "c", KeySource: "own"},
	} {
		e.TaskID, e.ProjectPath, e.Status = e.ID, "/p", "completed"
		if err := s.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE executions SET model_name='m', tokens_total=10, estimated_cost_usd=1 WHERE id=?`, e.ID); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	b, err := s.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines: %v", err)
	}
	for _, mode := range []string{"api_key", "subscription", "unknown"} {
		k := ModelKeySourceKey{Model: "m", KeySource: "own", BillingMode: mode}
		if got := b.CostByModelKeySource[k]; got != 1 {
			t.Errorf("CostByModelKeySource[%+v] = %v, want 1", k, got)
		}
	}
}
