package memory

import (
	"math"
	"testing"
	"time"
)

func newChatStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func chatRow(id, project, keySource string, cost float64, in, out int64, completed time.Time) *ChatTurn {
	return &ChatTurn{
		ID: id, ConversationID: "web:c1", ProjectPath: project, Model: "claude-sonnet-4-6",
		TokensInput: in, TokensOutput: out, EstimatedCostUSD: cost, KeySource: keySource,
		Status: ChatTurnCompleted, DurationMs: 1500, ToolCalls: 3,
		StartedAt: completed.Add(-time.Second), CompletedAt: completed,
	}
}

func TestChatTurns_SaveListRoundTrip(t *testing.T) {
	s := newChatStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	reported := 0.25
	in := chatRow("t1", "/p", "platform", 0.2, 100, 50, now)
	in.ReportedCostUSD = &reported
	in.TokensCacheRead, in.TokensCacheWrite, in.Partial = 7, 9, true
	if err := s.SaveChatTurn(in); err != nil {
		t.Fatalf("SaveChatTurn: %v", err)
	}
	if err := s.SaveChatTurn(chatRow("t2", "/p", "", 0.1, 1, 1, now.Add(time.Second))); err != nil {
		t.Fatalf("SaveChatTurn: %v", err)
	}

	rows, err := s.ListChatTurnsSince(now.Add(-time.Minute), "", 0)
	if err != nil {
		t.Fatalf("ListChatTurnsSince: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "t1" || rows[1].ID != "t2" {
		t.Fatalf("rows = %v, want t1,t2 oldest first", rows)
	}
	got := rows[0]
	if got.TokensInput != 100 || got.TokensOutput != 50 || got.TokensCacheRead != 7 || got.TokensCacheWrite != 9 ||
		got.EstimatedCostUSD != 0.2 || got.ReportedCostUSD == nil || *got.ReportedCostUSD != reported ||
		got.KeySource != "platform" || !got.Partial || got.ToolCalls != 3 || got.DurationMs != 1500 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.CompletedAt.Equal(now) || !got.StartedAt.Equal(now.Add(-time.Second)) {
		t.Errorf("times = %v / %v, want %v / %v", got.StartedAt, got.CompletedAt, now.Add(-time.Second), now)
	}
	if rows[1].ReportedCostUSD != nil || rows[1].KeySource != "unknown" {
		t.Errorf("unreported row = reported %v key %q, want nil/unknown", rows[1].ReportedCostUSD, rows[1].KeySource)
	}
}

func TestChatTurns_SinceAndProjectFilters(t *testing.T) {
	s := newChatStore(t)
	now := time.Now().UTC()
	for _, r := range []*ChatTurn{
		chatRow("old", "/p", "platform", 1, 1, 1, now.Add(-48*time.Hour)),
		chatRow("a", "/p", "platform", 1, 1, 1, now.Add(-time.Hour)),
		chatRow("b", "/q", "platform", 1, 1, 1, now.Add(-time.Hour)),
	} {
		if err := s.SaveChatTurn(r); err != nil {
			t.Fatalf("SaveChatTurn: %v", err)
		}
	}
	rows, _ := s.ListChatTurnsSince(now.Add(-24*time.Hour), "/p", 0)
	if len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("filtered rows = %v, want only a", rows)
	}
	tot, err := s.GetChatTotalsSince(now.Add(-24*time.Hour), "")
	if err != nil || tot.Turns != 2 {
		t.Fatalf("totals = %+v err %v, want 2 turns in window", tot, err)
	}
}

func TestChatTurns_TotalsDailyAndKeySource(t *testing.T) {
	s := newChatStore(t)
	day1 := time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for _, r := range []*ChatTurn{
		chatRow("a", "/p", "platform", 0.5, 100, 10, day1),
		chatRow("b", "/p", "platform", 0.25, 200, 20, day2),
		chatRow("c", "/p", "own", 1.0, 300, 30, day2),
	} {
		if err := s.SaveChatTurn(r); err != nil {
			t.Fatalf("SaveChatTurn: %v", err)
		}
	}
	since := day1.Add(-time.Hour)
	tot, err := s.GetChatTotalsSince(since, "")
	if err != nil {
		t.Fatalf("GetChatTotalsSince: %v", err)
	}
	if tot.Turns != 3 || math.Abs(tot.CostUSD-1.75) > 1e-9 || tot.Tokens != 660 {
		t.Errorf("totals = %+v, want 3 turns / 1.75 / 660 tokens", tot)
	}
	days, err := s.GetChatDailySince(since, "")
	if err != nil {
		t.Fatalf("GetChatDailySince: %v", err)
	}
	if len(days) != 2 || days[0].Date != "2026-10-01" || days[0].Turns != 1 || days[1].Date != "2026-10-02" ||
		days[1].Turns != 2 || math.Abs(days[1].CostUSD-1.25) > 1e-9 {
		t.Errorf("days = %+v, want 10-01 x1 and 10-02 x2 cost 1.25", days)
	}
	ks, err := s.GetChatTotalsByKeySource()
	if err != nil {
		t.Fatalf("GetChatTotalsByKeySource: %v", err)
	}
	if len(ks) != 2 || ks[0].KeySource != "own" || ks[0].Turns != 1 || ks[1].KeySource != "platform" ||
		ks[1].Turns != 2 || math.Abs(ks[1].CostUSD-0.75) > 1e-9 {
		t.Errorf("by key source = %+v", ks)
	}
}

func TestGetUsageSince_MergesOldestFirstWithCap(t *testing.T) {
	s := newChatStore(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

	for _, e := range []struct {
		id  string
		sec int
	}{{"e-2", 2}, {"e-4", 4}} {
		done := at(e.sec)
		if err := s.SaveExecution(&Execution{ID: e.id, TaskID: "GH-" + e.id, ProjectPath: "/p", Status: "completed", CreatedAt: base, CompletedAt: &done}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
	}
	// A canary and a row for another project must not leak in.
	canary := at(3)
	if err := s.SaveExecution(&Execution{ID: "e-canary", TaskID: "GH-c", ProjectPath: "/p", Status: "completed", CreatedAt: base, CompletedAt: &canary, IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	for _, c := range []struct {
		id, project string
		sec         int
	}{{"chat-1", "/p", 1}, {"chat-3", "/p", 3}, {"chat-other", "/other", 3}} {
		if err := s.SaveChatTurn(chatRow(c.id, c.project, "own", 0.1, 10, 5, at(c.sec))); err != nil {
			t.Fatalf("SaveChatTurn: %v", err)
		}
	}

	rows, more, err := s.GetUsageSince(base, "/p")
	if err != nil || more {
		t.Fatalf("GetUsageSince: err=%v more=%v", err, more)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.ExecutionID)
	}
	want := []string{"chat-1", "e-2", "chat-3", "e-4"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	if rows[0].Kind != UsageKindChatTurn || rows[1].Kind != UsageKindExecution {
		t.Errorf("kinds = %s,%s, want chat_turn,execution", rows[0].Kind, rows[1].Kind)
	}
	if r := rows[0]; r.TaskID != "chat-1" || r.ConversationID != "web:c1" || r.StartedAt == nil || r.KeySource != "own" {
		t.Errorf("chat row = %+v", r)
	}

	// since is inclusive: resuming from the newest completedAt repeats it.
	again, _, err := s.GetUsageSince(rows[len(rows)-1].CompletedAt, "/p")
	if err != nil || len(again) != 1 || again[0].ExecutionID != "e-4" {
		t.Fatalf("resume rows = %+v err=%v, want only e-4", again, err)
	}

	// The page cap is shared across both tables; the oldest rows win.
	old := MaxExecutionUsageRows
	MaxExecutionUsageRows = 3
	defer func() { MaxExecutionUsageRows = old }()
	capped, more, err := s.GetUsageSince(base, "/p")
	if err != nil || !more || len(capped) != 3 {
		t.Fatalf("capped: rows=%d more=%v err=%v, want 3 / true", len(capped), more, err)
	}
	if capped[2].ExecutionID != "chat-3" {
		t.Errorf("capped page ends at %s, want chat-3 (the oldest three)", capped[2].ExecutionID)
	}
}
