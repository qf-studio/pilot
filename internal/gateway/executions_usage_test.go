package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecutionsUsageEndpoint_RowCarriesAllFields is the GH-5676 endpoint pin:
// auth is required, `since` filters, and a returned row carries every field
// the hosted console meters on.
func TestExecutionsUsageEndpoint_RowCarriesAllFields(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	beforeRun := time.Now().UTC().Add(-2 * time.Second)
	completed := time.Now().UTC()
	oldCompleted := time.Now().UTC().Add(-72 * time.Hour)
	reported := 0.4321

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-usage-1", TaskID: "GH-5676", ProjectPath: "/tmp/usage-proj", Status: "completed",
		PRUrl: "https://github.com/acme/widgets/pull/9", KeySource: "platform",
		CreatedAt: beforeRun,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	// Drive the row through the production transitions: running stamps
	// started_at, completed stamps completed_at (both CURRENT_TIMESTAMP).
	for _, st := range []string{"running", "completed"} {
		if err := store.UpdateExecutionStatus("exec-usage-1", st); err != nil {
			t.Fatalf("UpdateExecutionStatus(%s): %v", st, err)
		}
	}
	if err := store.SaveExecutionMetrics(&memory.ExecutionMetrics{
		ExecutionID: "exec-usage-1", TokensInput: 1000, TokensOutput: 250, TokensTotal: 1250,
		TokensCacheRead: 400, TokensCacheWrite: 60, EstimatedCostUSD: 0.5, ReportedCostUSD: &reported,
		ModelName: "claude-sonnet-4-6",
	}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}
	// Completed before `since`: must not appear.
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-usage-old", TaskID: "GH-1", ProjectPath: "/tmp/usage-proj", Status: "completed",
		CreatedAt: oldCompleted, CompletedAt: &oldCompleted,
	}); err != nil {
		t.Fatalf("SaveExecution(old): %v", err)
	}
	// Canary: must not appear.
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-usage-canary", TaskID: "GH-2", ProjectPath: "/tmp/canary", Status: "completed",
		CreatedAt: completed, CompletedAt: &completed, IsCanary: true,
	}); err != nil {
		t.Fatalf("SaveExecution(canary): %v", err)
	}

	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: 19099}, &AuthConfig{Type: AuthTypeAPIToken, Token: "usage-secret"})
	server.SetDashboardStore(store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	url := "http://127.0.0.1:19099/api/v1/executions/usage?since=" + since

	// Same auth as the metrics endpoint: no token -> 401.
	noAuth, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := client.Do(noAuth)
	if err != nil {
		t.Fatalf("GET (no auth): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer usage-secret")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Decode to a generic map so the test pins the wire field names too.
	var body struct {
		Executions []map[string]any `json:"executions"`
		NextSince  string           `json:"nextSince"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Executions) != 1 {
		t.Fatalf("got %d rows, want 1 (old and canary rows excluded): %v", len(body.Executions), body.Executions)
	}
	row := body.Executions[0]

	wantStr := map[string]string{
		"executionId": "exec-usage-1",
		"taskId":      "GH-5676",
		"projectPath": "/tmp/usage-proj",
		"status":      "completed",
		"model":       "claude-sonnet-4-6",
		"keySource":   "platform",
		"prUrl":       "https://github.com/acme/widgets/pull/9",
	}
	for k, want := range wantStr {
		if got, _ := row[k].(string); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	wantNum := map[string]float64{
		"tokensInput": 1000, "tokensOutput": 250, "tokensCacheRead": 400, "tokensCacheWrite": 60,
		"estimatedCostUSD": 0.5, "reportedCostUSD": 0.4321,
	}
	for k, want := range wantNum {
		got, ok := row[k].(float64)
		if !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", k, row[k], want)
		}
	}
	for _, k := range []string{"startedAt", "completedAt"} {
		str, _ := row[k].(string)
		ts, err := time.Parse(time.RFC3339Nano, str)
		if err != nil {
			t.Errorf("%s = %v, want an RFC3339 timestamp (%v)", k, row[k], err)
		} else if ts.Before(beforeRun) || ts.After(time.Now().Add(time.Second)) {
			t.Errorf("%s = %v, want a time within this test run", k, ts)
		}
	}
	if body.NextSince != row["completedAt"] {
		t.Errorf("nextSince = %q, want the newest completedAt %q", body.NextSince, row["completedAt"])
	}
	if body.HasMore {
		t.Error("hasMore = true, want false")
	}
}

func TestExecutionsUsageEndpoint_BadSince(t *testing.T) {
	s := newTestServerWithDashboard(&mockDashboardStore{})
	for _, q := range []string{"", "?since=yesterday"} {
		req := httpTestRequest(t, http.MethodGet, "/api/v1/executions/usage"+q, nil)
		w := newTestResponseRecorder()
		s.handleExecutionsUsage(w, req)
		if w.status != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, w.status)
		}
	}
}
