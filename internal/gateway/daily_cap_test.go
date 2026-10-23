package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// seedDailyCapStore saves one execution row per (taskID, createdAt) pair.
func seedDailyCapStore(t *testing.T, rows map[string]time.Time) *memory.Store {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for task, at := range rows {
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-" + task, TaskID: task, ProjectPath: "/tmp/cap-proj", Status: "completed", CreatedAt: at,
		}); err != nil {
			t.Fatalf("SaveExecution(%s): %v", task, err)
		}
	}
	return store
}

// TestDailyCapEndpoint_ExactJSON pins the GH-5809 wire shape for the reached,
// not-reached and cap=0 cases. "Now" is 22:00 in Europe/Berlin (+02:00) on
// 2026-10-22; GH-2 was created at 23:30Z the day before, which is inside the
// Berlin day but outside the UTC day, so the zone is load-bearing.
func TestDailyCapEndpoint_ExactJSON(t *testing.T) {
	now := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	store := seedDailyCapStore(t, map[string]time.Time{
		"GH-1": time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
		"GH-2": time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC),
		"GH-3": time.Date(2026, 10, 21, 21, 59, 0, 0, time.UTC), // 23:59 Berlin on the 21st: previous day
	})

	tests := []struct {
		name string
		cap  int
		want string
	}{
		{
			name: "reached",
			cap:  2,
			want: `{"cap":2,"timezone":"Europe/Berlin","dispatched_today":2,"reached":true,"resumes_at":"2026-10-23T00:00:00+02:00"}`,
		},
		{
			name: "not reached",
			cap:  3,
			want: `{"cap":3,"timezone":"Europe/Berlin","dispatched_today":2,"reached":false,"resumes_at":null}`,
		},
		{
			name: "cap zero is unlimited",
			cap:  0,
			want: `{"cap":0,"timezone":"Europe/Berlin","dispatched_today":2,"reached":false,"resumes_at":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(store)
			s.SetDailyTicketCap(tt.cap, "Europe/Berlin")
			s.nowFn = func() time.Time { return now }

			w := newTestResponseRecorder()
			s.handleDailyCap(w, httpTestRequest(t, http.MethodGet, "/api/v1/executions/daily-cap", nil))
			if w.status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.status, w.body.String())
			}
			if got := strings.TrimSpace(w.body.String()); got != tt.want {
				t.Errorf("body = %s\nwant   %s", got, tt.want)
			}
		})
	}
}

// TestDailyCapEndpoint_DefaultsToUTC: an empty timezone renders as "UTC" and
// the day is the UTC day (GH-2 at 23:30Z the previous day is excluded).
func TestDailyCapEndpoint_DefaultsToUTC(t *testing.T) {
	store := seedDailyCapStore(t, map[string]time.Time{
		"GH-1": time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
		"GH-2": time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC),
	})
	s := newTestServerWithDashboard(store)
	s.SetDailyTicketCap(1, "")
	s.nowFn = func() time.Time { return time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC) }

	w := newTestResponseRecorder()
	s.handleDailyCap(w, httpTestRequest(t, http.MethodGet, "/api/v1/executions/daily-cap", nil))
	want := `{"cap":1,"timezone":"UTC","dispatched_today":1,"reached":true,"resumes_at":"2026-10-23T00:00:00Z"}`
	if got := strings.TrimSpace(w.body.String()); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

// TestDailyCapEndpoint_RequiresAuth: the route sits behind the same auth as
// /api/v1/executions/usage, so a missing token is rejected identically (401).
func TestDailyCapEndpoint_RequiresAuth(t *testing.T) {
	store := seedDailyCapStore(t, nil)
	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: 19109}, &AuthConfig{Type: AuthTypeAPIToken, Token: "cap-secret"})
	server.SetDashboardStore(store)
	server.SetDailyTicketCap(5, "UTC")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path, token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:19109"+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	usageStatus, usageBody := get("/api/v1/executions/usage?since=2026-01-01T00:00:00Z", "")
	capStatus, capBody := get("/api/v1/executions/daily-cap", "")
	if capStatus != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", capStatus)
	}
	if capStatus != usageStatus || capBody != usageBody {
		t.Errorf("daily-cap rejection (%d %q) differs from usage rejection (%d %q)", capStatus, capBody, usageStatus, usageBody)
	}

	if status, body := get("/api/v1/executions/daily-cap", "cap-secret"); status != http.StatusOK {
		t.Errorf("authed status = %d, want 200: %s", status, body)
	}
}

func TestDailyCapEndpoint_Errors(t *testing.T) {
	s := newTestServerWithDashboard(nil)
	s.dashboardStore = nil
	w := newTestResponseRecorder()
	s.handleDailyCap(w, httpTestRequest(t, http.MethodGet, "/api/v1/executions/daily-cap", nil))
	if w.status != http.StatusServiceUnavailable {
		t.Errorf("nil store: status = %d, want 503", w.status)
	}

	s = newTestServerWithDashboard(&mockDashboardStore{})
	w = newTestResponseRecorder()
	s.handleDailyCap(w, httpTestRequest(t, http.MethodPost, "/api/v1/executions/daily-cap", nil))
	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status = %d, want 405", w.status)
	}
}

// TestPrometheusExporter_DailyTicketCapDispatchedToday: the gauge sits next to
// the queue metrics and counts distinct tickets in the org-local day, whichever
// of SetDashboardStore / SetMetricsSource ran first (GH-5808).
func TestPrometheusExporter_DailyTicketCapDispatchedToday(t *testing.T) {
	now := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	store := seedDailyCapStore(t, map[string]time.Time{
		"GH-1": time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
		"GH-2": time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC), // Berlin day Oct 22
		"GH-3": time.Date(2026, 10, 21, 21, 59, 0, 0, time.UTC), // Berlin day Oct 21
	})

	scrape := func(s *Server) string {
		w := httptest.NewRecorder()
		s.handleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		return w.Body.String()
	}

	t.Run("counts the org-local day", func(t *testing.T) {
		s := newTestServerWithDashboard(store)
		s.SetDailyTicketCap(15, "Europe/Berlin")
		s.nowFn = func() time.Time { return now }
		s.SetMetricsSource(&mockMetricsSource{})
		body := scrape(s)
		for _, want := range []string{
			"# TYPE pilot_daily_ticket_cap_dispatched_today gauge",
			"pilot_daily_ticket_cap_dispatched_today 2\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("metrics missing %q:\n%s", want, body)
			}
		}
		if q, g := strings.Index(body, "pilot_queue_depth"), strings.Index(body, "pilot_daily_ticket_cap_dispatched_today"); q < 0 || g < q {
			t.Errorf("gauge should follow the queue metrics (queue at %d, gauge at %d)", q, g)
		}
	})

	t.Run("reported even with cap 0", func(t *testing.T) {
		s := newTestServerWithDashboard(store)
		s.nowFn = func() time.Time { return now }
		s.SetMetricsSource(&mockMetricsSource{})
		if body := scrape(s); !strings.Contains(body, "pilot_daily_ticket_cap_dispatched_today 1\n") {
			t.Errorf("want the UTC-day count 1 with no cap configured:\n%s", body)
		}
	})

	t.Run("absent without a store", func(t *testing.T) {
		s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
		s.SetMetricsSource(&mockMetricsSource{})
		if body := scrape(s); strings.Contains(body, "pilot_daily_ticket_cap_dispatched_today") {
			t.Errorf("gauge emitted with no dashboard store:\n%s", body)
		}
	})
}
