package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// mockDashboardStore implements DashboardStore for testing.
type mockDashboardStore struct {
	lifetimeTokens *memory.LifetimeTokens
	taskCounts     *memory.LifetimeTaskCounts
	dailyMetrics   []*memory.DailyMetrics
	executions     []*memory.Execution
	queuedTasks    []*memory.Execution
	activeExecs    []*memory.Execution
	logEntries     []*memory.LogEntry
}

func (m *mockDashboardStore) GetLifetimeTokens() (*memory.LifetimeTokens, error) {
	return m.lifetimeTokens, nil
}

func (m *mockDashboardStore) GetLifetimeTaskCounts() (*memory.LifetimeTaskCounts, error) {
	return m.taskCounts, nil
}

func (m *mockDashboardStore) GetDailyMetrics(_ memory.MetricsQuery) ([]*memory.DailyMetrics, error) {
	return m.dailyMetrics, nil
}

func (m *mockDashboardStore) GetRecentExecutions(_ int) ([]*memory.Execution, error) {
	return m.executions, nil
}

func (m *mockDashboardStore) GetQueuedTasks(_ int) ([]*memory.Execution, error) {
	return m.queuedTasks, nil
}

func (m *mockDashboardStore) GetActiveExecutions() ([]*memory.Execution, error) {
	return m.activeExecs, nil
}

func (m *mockDashboardStore) GetRecentLogs(_ int) ([]*memory.LogEntry, error) {
	return m.logEntries, nil
}

func newTestServerWithDashboard(store DashboardStore) *Server {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 9090})
	s.dashboardStore = store
	return s
}

func TestHandleDashboardMetrics(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		store          DashboardStore
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:           "success with data",
			method:         http.MethodGet,
			store: &mockDashboardStore{
				lifetimeTokens: &memory.LifetimeTokens{
					InputTokens:  1000,
					OutputTokens: 500,
					TotalTokens:  1500,
					TotalCostUSD: 0.25,
				},
				taskCounts: &memory.LifetimeTaskCounts{
					Total:     10,
					Succeeded: 8,
					Failed:    2,
				},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp dashboardMetricsResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.TotalTokens != 1500 {
					t.Errorf("expected TotalTokens=1500, got %d", resp.TotalTokens)
				}
				if resp.InputTokens != 1000 {
					t.Errorf("expected InputTokens=1000, got %d", resp.InputTokens)
				}
				if resp.TotalTasks != 10 {
					t.Errorf("expected TotalTasks=10, got %d", resp.TotalTasks)
				}
				if resp.SucceededTasks != 8 {
					t.Errorf("expected SucceededTasks=8, got %d", resp.SucceededTasks)
				}
				if len(resp.TokenSparkline) != 7 {
					t.Errorf("expected 7 sparkline entries, got %d", len(resp.TokenSparkline))
				}
			},
		},
		{
			name:           "method not allowed",
			method:         http.MethodPost,
			store:          &mockDashboardStore{},
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "no store configured",
			method:         http.MethodGet,
			store:          nil,
			expectedStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(tt.store)
			req := httptest.NewRequest(tt.method, "/api/v1/metrics", nil)
			w := httptest.NewRecorder()

			s.handleDashboardMetrics(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleDashboardQueue(t *testing.T) {
	now := time.Now()
	completedAt := now.Add(-time.Minute)

	tests := []struct {
		name           string
		method         string
		store          DashboardStore
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name:   "success with tasks",
			method: http.MethodGet,
			store: &mockDashboardStore{
				executions: []*memory.Execution{
					{
						ID:          "exec-1",
						TaskID:      "GH-100",
						TaskTitle:   "Add feature X",
						Status:      "running",
						ProjectPath: "/tmp/project",
						CreatedAt:   now,
					},
					{
						ID:          "exec-2",
						TaskID:      "GH-101",
						TaskTitle:   "Fix bug Y",
						Status:      "completed",
						ProjectPath: "/tmp/project",
						PRUrl:       "https://github.com/org/repo/pull/42",
						CreatedAt:   now.Add(-time.Hour),
						CompletedAt: &completedAt,
					},
				},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var tasks []queueTaskResponse
				if err := json.Unmarshal(body, &tasks); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(tasks) != 2 {
					t.Fatalf("expected 2 tasks, got %d", len(tasks))
				}
				if tasks[0].Status != "running" {
					t.Errorf("expected status 'running', got %q", tasks[0].Status)
				}
				if tasks[0].Progress != 0.5 {
					t.Errorf("expected progress 0.5, got %f", tasks[0].Progress)
				}
				if tasks[1].Status != "done" {
					t.Errorf("expected status 'done', got %q", tasks[1].Status)
				}
				if tasks[1].Progress != 1.0 {
					t.Errorf("expected progress 1.0, got %f", tasks[1].Progress)
				}
				if tasks[1].PRURL != "https://github.com/org/repo/pull/42" {
					t.Errorf("expected PR URL, got %q", tasks[1].PRURL)
				}
			},
		},
		{
			name:   "empty queue",
			method: http.MethodGet,
			store: &mockDashboardStore{
				executions: []*memory.Execution{},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var tasks []queueTaskResponse
				if err := json.Unmarshal(body, &tasks); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(tasks) != 0 {
					t.Errorf("expected 0 tasks, got %d", len(tasks))
				}
			},
		},
		{
			name:           "method not allowed",
			method:         http.MethodPost,
			store:          &mockDashboardStore{},
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(tt.store)
			req := httptest.NewRequest(tt.method, "/api/v1/queue", nil)
			w := httptest.NewRecorder()

			s.handleDashboardQueue(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleDashboardHistory(t *testing.T) {
	now := time.Now()
	completedAt := now.Add(-time.Minute)

	tests := []struct {
		name           string
		url            string
		store          DashboardStore
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name: "filters non-terminal statuses",
			url:  "/api/v1/history",
			store: &mockDashboardStore{
				executions: []*memory.Execution{
					{ID: "e1", TaskID: "GH-1", TaskTitle: "Done task", Status: "completed", CompletedAt: &completedAt, DurationMs: 5000},
					{ID: "e2", TaskID: "GH-2", TaskTitle: "Running task", Status: "running"},
					{ID: "e3", TaskID: "GH-3", TaskTitle: "Failed task", Status: "failed", CompletedAt: &completedAt, DurationMs: 3000},
				},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var entries []historyEntryResponse
				if err := json.Unmarshal(body, &entries); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(entries) != 2 {
					t.Fatalf("expected 2 entries (completed+failed), got %d", len(entries))
				}
				if entries[0].Status != "completed" {
					t.Errorf("expected 'completed', got %q", entries[0].Status)
				}
				if entries[1].Status != "failed" {
					t.Errorf("expected 'failed', got %q", entries[1].Status)
				}
				if entries[0].DurationMs != 5000 {
					t.Errorf("expected duration 5000, got %d", entries[0].DurationMs)
				}
			},
		},
		{
			name: "respects limit parameter",
			url:  "/api/v1/history?limit=10",
			store: &mockDashboardStore{
				executions: []*memory.Execution{},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var entries []historyEntryResponse
				if err := json.Unmarshal(body, &entries); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(entries) != 0 {
					t.Errorf("expected 0 entries, got %d", len(entries))
				}
			},
		},
		{
			name:           "no store",
			url:            "/api/v1/history",
			store:          nil,
			expectedStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(tt.store)
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			s.handleDashboardHistory(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleDashboardLogs(t *testing.T) {
	ts := time.Date(2026, 2, 19, 14, 30, 45, 0, time.UTC)

	tests := []struct {
		name           string
		url            string
		store          DashboardStore
		expectedStatus int
		checkBody      func(t *testing.T, body []byte)
	}{
		{
			name: "returns logs in chronological order",
			url:  "/api/v1/logs",
			store: &mockDashboardStore{
				logEntries: []*memory.LogEntry{
					{ID: 3, Timestamp: ts.Add(2 * time.Second), Level: "info", Message: "third", Component: "executor"},
					{ID: 2, Timestamp: ts.Add(time.Second), Level: "warn", Message: "second", Component: "gateway"},
					{ID: 1, Timestamp: ts, Level: "error", Message: "first", Component: "alerts"},
				},
			},
			expectedStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var logs []logEntryResponse
				if err := json.Unmarshal(body, &logs); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(logs) != 3 {
					t.Fatalf("expected 3 logs, got %d", len(logs))
				}
				// Reversed: oldest first
				if logs[0].Message != "first" {
					t.Errorf("expected oldest first, got %q", logs[0].Message)
				}
				if logs[2].Message != "third" {
					t.Errorf("expected newest last, got %q", logs[2].Message)
				}
				if logs[0].Ts != "14:30:45" {
					t.Errorf("expected ts '14:30:45', got %q", logs[0].Ts)
				}
				if logs[0].Component != "alerts" {
					t.Errorf("expected component 'alerts', got %q", logs[0].Component)
				}
			},
		},
		{
			name: "custom limit",
			url:  "/api/v1/logs?limit=5",
			store: &mockDashboardStore{
				logEntries: []*memory.LogEntry{},
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "no store",
			url:            "/api/v1/logs",
			store:          nil,
			expectedStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(tt.store)
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			s.handleDashboardLogs(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}
			if tt.checkBody != nil {
				tt.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestIssueIDFromTaskID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"GH-100", "GH-100"},
		{"repo/GH-100", "GH-100"},
		{"org/repo/GH-100", "repo/GH-100"},
		{"", ""},
	}

	for _, tt := range tests {
		got := issueIDFromTaskID(tt.input)
		if got != tt.expected {
			t.Errorf("issueIDFromTaskID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestNormalizeDashboardStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"completed", "done"},
		{"running", "running"},
		{"queued", "queued"},
		{"pending", "pending"},
		{"failed", "failed"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := normalizeDashboardStatus(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeDashboardStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDashboardIssueURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"GH-100", "https://github.com/anthropics/pilot/issues/100"},
		{"LINEAR-123", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := dashboardIssueURL(tt.input)
		if got != tt.expected {
			t.Errorf("dashboardIssueURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
