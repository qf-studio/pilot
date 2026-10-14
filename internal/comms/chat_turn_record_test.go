package comms

import (
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

func newRecordingWebChat(t *testing.T, b *webChatBackend) *Handler {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(&HandlerConfig{
		Messenger: &webChatMessenger{}, Runner: executor.NewRunnerWithBackend(b),
		ProjectPath: "/work/proj", Store: store,
	})
}

func chatRows(t *testing.T, h *Handler) []*memory.ChatTurn {
	t.Helper()
	rows, err := h.store.ListChatTurnsSince(time.Now().Add(-time.Hour), "", 0)
	if err != nil {
		t.Fatalf("ListChatTurnsSince: %v", err)
	}
	return rows
}

// TestWebChat_RecordsTurnRow is the GH-5677 row pin: one scripted backend turn
// leaves exactly one chat_turns row with those counts, a cost above zero and
// key_source platform when the env var says platform.
func TestWebChat_RecordsTurnRow(t *testing.T) {
	t.Setenv("PILOT_ANTHROPIC_KEY_SOURCE", "platform")
	reported := 0.31
	h := newRecordingWebChat(t, &webChatBackend{
		events: []executor.BackendEvent{{Type: executor.EventTypeToolUse, ToolName: "Read"}, {Type: executor.EventTypeToolUse, ToolName: "Grep"}},
		usage: &executor.BackendResult{
			TokensInput: 1200, TokensOutput: 340, CacheReadInputTokens: 5000, CacheCreationInputTokens: 700,
			ReportedCostUSD: &reported, Model: "claude-sonnet-4-6",
		},
	})
	send(h, "web", "where is the router?")

	rows := chatRows(t, h)
	if len(rows) != 1 {
		t.Fatalf("chat_turns rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.TokensInput != 1200 || r.TokensOutput != 340 || r.TokensCacheRead != 5000 || r.TokensCacheWrite != 700 {
		t.Errorf("tokens = %d/%d/%d/%d, want 1200/340/5000/700", r.TokensInput, r.TokensOutput, r.TokensCacheRead, r.TokensCacheWrite)
	}
	if r.EstimatedCostUSD <= 0 {
		t.Errorf("EstimatedCostUSD = %v, want > 0", r.EstimatedCostUSD)
	}
	if r.ReportedCostUSD == nil || *r.ReportedCostUSD != reported {
		t.Errorf("ReportedCostUSD = %v, want %v", r.ReportedCostUSD, reported)
	}
	if r.KeySource != "platform" {
		t.Errorf("KeySource = %q, want platform", r.KeySource)
	}
	if r.Model != "claude-sonnet-4-6" || r.ConversationID != "web:conv-1" || r.ProjectPath != "/work/proj" {
		t.Errorf("model/conversation/project = %q/%q/%q", r.Model, r.ConversationID, r.ProjectPath)
	}
	if r.Status != memory.ChatTurnCompleted || r.ToolCalls != 2 || r.Partial {
		t.Errorf("status/toolCalls/partial = %q/%d/%v, want completed/2/false", r.Status, r.ToolCalls, r.Partial)
	}
	if r.CompletedAt.Before(r.StartedAt) || r.CompletedAt.IsZero() {
		t.Errorf("started %v completed %v", r.StartedAt, r.CompletedAt)
	}
}

func TestWebChat_RecordsFailedTurn(t *testing.T) {
	t.Setenv("PILOT_ANTHROPIC_KEY_SOURCE", "own")
	h := newRecordingWebChat(t, &webChatBackend{err: errors.New("backend exploded")})
	send(h, "web", "hello")

	rows := chatRows(t, h)
	if len(rows) != 1 {
		t.Fatalf("chat_turns rows = %d, want 1 for a failed turn", len(rows))
	}
	if rows[0].Status != memory.ChatTurnFailed || rows[0].Error == "" {
		t.Errorf("status/error = %q/%q, want failed with the error text", rows[0].Status, rows[0].Error)
	}
	if rows[0].KeySource != "own" {
		t.Errorf("KeySource = %q, want own", rows[0].KeySource)
	}
}

func TestWebChat_UnsetKeySourceRecordsUnknown(t *testing.T) {
	t.Setenv("PILOT_ANTHROPIC_KEY_SOURCE", "")
	h := newRecordingWebChat(t, &webChatBackend{})
	send(h, "web", "hello")
	rows := chatRows(t, h)
	if len(rows) != 1 || rows[0].KeySource != "unknown" {
		t.Fatalf("rows = %+v, want one row with key_source unknown", rows)
	}
}

// TestChatTurn_CapRejectedTurnWritesNoRow pins the order: recordChatTurn runs
// only for a turn reserveDailyTurn admitted, so a cap-rejected turn leaves no
// chat_turns row (GH-5690).
func TestChatTurn_CapRejectedTurnWritesNoRow(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	b := &webChatBackend{}
	msgr := &webChatMessenger{}
	h := NewHandler(&HandlerConfig{
		Messenger: msgr, Runner: executor.NewRunnerWithBackend(b), ProjectPath: "/work/proj",
		Store: store, DailyTurnCap: 1, Now: func() time.Time { return clock },
	})

	sendTo(h, "a", "first question")
	sendTo(h, "b", "second question")

	if got := msgr.last(); got != DailyChatLimitMessage {
		t.Fatalf("last message = %q, want the daily limit message", got)
	}
	rows := chatRows(t, h)
	if len(rows) != 1 {
		t.Fatalf("chat_turns rows = %d, want 1: only the admitted turn is recorded", len(rows))
	}
	if rows[0].ConversationID != "web:a" {
		t.Errorf("recorded conversation = %q, want web:a", rows[0].ConversationID)
	}
}
