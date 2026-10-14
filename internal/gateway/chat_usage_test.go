package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// chatBackend is a scripted fake backend: every turn reports the same usage.
type chatBackend struct{ usage executor.BackendResult }

func (chatBackend) Name() string      { return "chat-usage-fake" }
func (chatBackend) IsAvailable() bool { return true }
func (b chatBackend) Execute(_ context.Context, _ executor.ExecuteOptions) (*executor.BackendResult, error) {
	res := b.usage
	res.Success, res.Output, res.SessionID = true, "ok", "sess-1"
	return &res, nil
}

type nopMessenger struct{}

func (nopMessenger) SendText(context.Context, string, string, string) error { return nil }
func (nopMessenger) SendConfirmation(context.Context, string, string, string, string, string) (string, error) {
	return "", nil
}
func (nopMessenger) SendProgress(context.Context, string, string, string, string, int, string) (string, error) {
	return "", nil
}
func (nopMessenger) SendResult(context.Context, string, string, string, bool, string, string) error {
	return nil
}
func (nopMessenger) SendChunked(context.Context, string, string, string, string) error { return nil }
func (nopMessenger) AcknowledgeCallback(context.Context, string) error                 { return nil }
func (nopMessenger) MaxMessageLength() int                                             { return 4000 }

func newChatGatewayStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// runChatTurns drives n real web chat turns through the comms handler so the
// rows are written by the production path.
func runChatTurns(t *testing.T, store *memory.Store, n int) {
	t.Helper()
	h := comms.NewHandler(&comms.HandlerConfig{
		Messenger: nopMessenger{}, ProjectPath: "/work/proj", Store: store,
		Runner: executor.NewRunnerWithBackend(chatBackend{usage: executor.BackendResult{
			TokensInput: 1000, TokensOutput: 400, CacheReadInputTokens: 3000, CacheCreationInputTokens: 200,
			Model: "claude-sonnet-4-6",
		}}),
	})
	for i := 0; i < n; i++ {
		h.HandleMessage(context.Background(), &comms.IncomingMessage{
			ContextID: "web:conv-1", Platform: "web", Text: "where is the router?", SenderID: "u1",
		})
	}
}

func saveChatRow(t *testing.T, store *memory.Store, id, keySource string, cost float64, in, out int64, completed time.Time) {
	t.Helper()
	if err := store.SaveChatTurn(&memory.ChatTurn{
		ID: id, ConversationID: "web:c", ProjectPath: "/work/proj", Model: "claude-sonnet-4-6",
		TokensInput: in, TokensOutput: out, EstimatedCostUSD: cost, KeySource: keySource,
		Status: memory.ChatTurnCompleted, StartedAt: completed.Add(-time.Second), CompletedAt: completed,
	}); err != nil {
		t.Fatalf("SaveChatTurn(%s): %v", id, err)
	}
}

// TestDashboardMetrics_ChatFieldsEqualSavedRows is the GH-5677 metrics pin:
// chatTurns, chatCostUSD and chatTokens equal the sum of the saved rows in the
// window, and rows outside the window are left out. Two of the rows come from
// the real comms handler so a zeroed cost in the handler fails it too.
func TestDashboardMetrics_ChatFieldsEqualSavedRows(t *testing.T) {
	t.Setenv("PILOT_ANTHROPIC_KEY_SOURCE", "platform")
	store := newChatGatewayStore(t)
	now := time.Now().UTC()

	runChatTurns(t, store, 2)
	saveChatRow(t, store, "direct-1", "own", 0.5, 2000, 800, now.Add(-2*time.Hour))
	saveChatRow(t, store, "direct-2", "own", 0.25, 100, 50, now.AddDate(0, 0, -5))
	saveChatRow(t, store, "outside-window", "own", 9.99, 7777, 7777, now.AddDate(0, 0, -40))

	inWindow, err := store.ListChatTurnsSince(now.AddDate(0, 0, -30), "", 0)
	if err != nil {
		t.Fatalf("ListChatTurnsSince: %v", err)
	}
	var wantCost float64
	var wantTokens int64
	for _, r := range inWindow {
		wantCost += r.EstimatedCostUSD
		wantTokens += r.TokensInput + r.TokensOutput
	}
	if len(inWindow) != 4 {
		t.Fatalf("rows in window = %d, want 4 (2 handler + 2 direct)", len(inWindow))
	}

	srv := newTestServerWithDashboard(store)
	w := httptest.NewRecorder()
	srv.handleDashboardMetrics(w, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp dashboardMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.ChatTurns != 4 {
		t.Errorf("chatTurns = %d, want 4", resp.ChatTurns)
	}
	if resp.ChatTokens != wantTokens || resp.ChatTokens < 2*1400+2850 {
		t.Errorf("chatTokens = %d, want %d (= 2x1400 handler + 2850 direct)", resp.ChatTokens, wantTokens)
	}
	if math.Abs(resp.ChatCostUSD-wantCost) > 1e-9 {
		t.Errorf("chatCostUSD = %v, want %v (sum of saved rows)", resp.ChatCostUSD, wantCost)
	}
	// Direct rows alone are 0.75; the handler rows must add a positive cost.
	if resp.ChatCostUSD <= 0.75 {
		t.Errorf("chatCostUSD = %v, want > 0.75: handler-written rows carry no cost", resp.ChatCostUSD)
	}

	if len(resp.ChatDaily) != 30 {
		t.Fatalf("chatDaily len = %d, want 30", len(resp.ChatDaily))
	}
	var dayTurns int
	var dayCost float64
	for _, d := range resp.ChatDaily {
		dayTurns += d.Turns
		dayCost += d.CostUSD
	}
	if dayTurns != 4 || math.Abs(dayCost-wantCost) > 1e-9 {
		t.Errorf("chatDaily sums = %d turns / %v cost, want 4 / %v", dayTurns, dayCost, wantCost)
	}
	if last := resp.ChatDaily[29]; last.Date != now.Format("2006-01-02") {
		t.Errorf("chatDaily[29].date = %q, want today %q", last.Date, now.Format("2006-01-02"))
	}
}

func TestDashboardMetrics_NoChatStoreYieldsZeroChat(t *testing.T) {
	srv := newTestServerWithDashboard(&mockDashboardStore{lifetimeTokens: &memory.LifetimeTokens{}, taskCounts: &memory.LifetimeTaskCounts{}})
	w := httptest.NewRecorder()
	srv.handleDashboardMetrics(w, httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil))
	var resp dashboardMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChatTurns != 0 || resp.ChatCostUSD != 0 || len(resp.ChatDaily) != 30 {
		t.Errorf("chat = %d / %v / %d days, want zeros and a 30-day series", resp.ChatTurns, resp.ChatCostUSD, len(resp.ChatDaily))
	}
}

func TestPrometheus_ChatCountersByKeySource(t *testing.T) {
	store := newChatGatewayStore(t)
	now := time.Now().UTC()
	saveChatRow(t, store, "p1", "platform", 0.5, 1, 1, now)
	saveChatRow(t, store, "p2", "platform", 0.25, 1, 1, now)
	saveChatRow(t, store, "o1", "own", 1.0, 1, 1, now)

	exp := NewPrometheusExporter(&mockMetricsSource{})
	exp.SetChatSource(store)
	var sb strings.Builder
	if err := exp.WritePrometheus(&sb); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		`pilot_chat_turns_total{key_source="platform"} 2`,
		`pilot_chat_turns_total{key_source="own"} 1`,
		`pilot_chat_turns_total{key_source="unknown"} 0`,
		`pilot_chat_cost_usd_total{key_source="platform"} 0.75`,
		`pilot_chat_cost_usd_total{key_source="own"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
