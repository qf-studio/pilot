package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startBriefsServer starts an authed gateway backed by a fresh store and
// returns its base URL and the store.
func startBriefsServer(t *testing.T) (string, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	port := freePort(t)
	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: port}, &AuthConfig{Type: AuthTypeAPIToken, Token: "briefs-secret"})
	server.SetDashboardStore(store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)
	return fmt.Sprintf("http://127.0.0.1:%d", port), store
}

func briefsGet(t *testing.T, url string, auth bool) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer briefs-secret")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, buf
}

func saveBrief(t *testing.T, store *memory.Store, at time.Time, marker string) {
	t.Helper()
	saveBriefKind(t, store, at, marker, "")
}

func saveBriefKind(t *testing.T, store *memory.Store, at time.Time, marker, kind string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"schema": 1, "marker": marker})
	if err := store.SaveBriefPayload(&memory.BriefPayload{GeneratedAt: at, Schema: 1, Kind: kind, Payload: payload}); err != nil {
		t.Fatalf("SaveBriefPayload: %v", err)
	}
}

func TestBriefsEndpoints_Auth(t *testing.T) {
	base, _ := startBriefsServer(t)
	for _, path := range []string{"/api/v1/briefs/latest", "/api/v1/briefs"} {
		if code, _ := briefsGet(t, base+path, false); code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, code)
		}
	}
}

func TestBriefsEndpoints_Empty(t *testing.T) {
	base, _ := startBriefsServer(t)

	if code, _ := briefsGet(t, base+"/api/v1/briefs/latest", true); code != http.StatusNotFound {
		t.Errorf("latest on empty store = %d, want 404", code)
	}

	code, body := briefsGet(t, base+"/api/v1/briefs", true)
	if code != http.StatusOK {
		t.Fatalf("list on empty store = %d, want 200", code)
	}
	var got struct {
		Briefs []json.RawMessage `json:"briefs"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.Briefs == nil || len(got.Briefs) != 0 {
		t.Errorf("briefs = %v, want empty non-nil array (%s)", got.Briefs, body)
	}
}

func TestBriefsEndpoints_LatestAndLimit(t *testing.T) {
	base, store := startBriefsServer(t)
	now := time.Now()
	for i, m := range []string{"oldest", "middle", "newest"} {
		saveBrief(t, store, now.Add(time.Duration(i)*time.Hour), m)
	}

	code, body := briefsGet(t, base+"/api/v1/briefs/latest", true)
	if code != http.StatusOK {
		t.Fatalf("latest = %d, want 200", code)
	}
	var latest map[string]any
	if err := json.Unmarshal(body, &latest); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if latest["marker"] != "newest" {
		t.Errorf("latest marker = %v, want newest", latest["marker"])
	}

	tests := []struct {
		name     string
		query    string
		wantCode int
		want     []string
	}{
		{"default returns all, newest first", "", http.StatusOK, []string{"newest", "middle", "oldest"}},
		{"limit 2", "?limit=2", http.StatusOK, []string{"newest", "middle"}},
		{"limit 1", "?limit=1", http.StatusOK, []string{"newest"}},
		{"limit above max is clamped", "?limit=100000", http.StatusOK, []string{"newest", "middle", "oldest"}},
		{"limit zero rejected", "?limit=0", http.StatusBadRequest, nil},
		{"limit negative rejected", "?limit=-3", http.StatusBadRequest, nil},
		{"limit non-numeric rejected", "?limit=abc", http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := briefsGet(t, base+"/api/v1/briefs"+tt.query, true)
			if code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", code, tt.wantCode, body)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got struct {
				Briefs []map[string]any `json:"briefs"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Briefs) != len(tt.want) {
				t.Fatalf("got %d briefs, want %d", len(got.Briefs), len(tt.want))
			}
			for i, m := range tt.want {
				if got.Briefs[i]["marker"] != m {
					t.Errorf("briefs[%d].marker = %v, want %s", i, got.Briefs[i]["marker"], m)
				}
			}
		})
	}
}

// seedKindBriefs stores a weekly row older than two daily rows, so the newest
// row overall is a daily one.
func seedKindBriefs(t *testing.T, store *memory.Store) {
	t.Helper()
	now := time.Now()
	saveBriefKind(t, store, now.Add(-48*time.Hour), "weekly-old", "weekly")
	saveBriefKind(t, store, now.Add(-2*time.Hour), "daily-mid", "daily")
	saveBriefKind(t, store, now, "daily-new", "daily")
}

func TestBriefsLatest_Kind(t *testing.T) {
	base, store := startBriefsServer(t)
	seedKindBriefs(t, store)

	tests := []struct {
		name       string
		query      string
		wantCode   int
		wantMarker string
	}{
		{"no param is unfiltered", "", http.StatusOK, "daily-new"},
		{"empty kind is unfiltered", "?kind=", http.StatusOK, "daily-new"},
		{"weekly returns weekly despite later daily", "?kind=weekly", http.StatusOK, "weekly-old"},
		{"daily returns newest daily", "?kind=daily", http.StatusOK, "daily-new"},
		{"bogus rejected", "?kind=bogus", http.StatusBadRequest, ""},
		{"wrong case rejected", "?kind=Weekly", http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := briefsGet(t, base+"/api/v1/briefs/latest"+tt.query, true)
			if code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", code, tt.wantCode, body)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got["marker"] != tt.wantMarker {
				t.Errorf("marker = %v, want %s", got["marker"], tt.wantMarker)
			}
		})
	}
}

func TestBriefsLatest_KindWithNoMatchingRow(t *testing.T) {
	base, store := startBriefsServer(t)
	saveBriefKind(t, store, time.Now(), "only-daily", "daily")

	if code, body := briefsGet(t, base+"/api/v1/briefs/latest?kind=weekly", true); code != http.StatusNotFound {
		t.Errorf("weekly with only daily rows = %d, want 404 (%s)", code, body)
	}
}

func TestBriefsList_Kind(t *testing.T) {
	base, store := startBriefsServer(t)
	seedKindBriefs(t, store)

	tests := []struct {
		name     string
		query    string
		wantCode int
		want     []string
	}{
		{"no param is unfiltered", "", http.StatusOK, []string{"daily-new", "daily-mid", "weekly-old"}},
		{"empty kind is unfiltered", "?kind=", http.StatusOK, []string{"daily-new", "daily-mid", "weekly-old"}},
		{"weekly only", "?kind=weekly", http.StatusOK, []string{"weekly-old"}},
		{"daily only", "?kind=daily", http.StatusOK, []string{"daily-new", "daily-mid"}},
		{"daily with limit", "?kind=daily&limit=1", http.StatusOK, []string{"daily-new"}},
		{"bogus rejected", "?kind=bogus", http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := briefsGet(t, base+"/api/v1/briefs"+tt.query, true)
			if code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", code, tt.wantCode, body)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got struct {
				Briefs []map[string]any `json:"briefs"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Briefs) != len(tt.want) {
				t.Fatalf("got %d briefs, want %d (%s)", len(got.Briefs), len(tt.want), body)
			}
			for i, m := range tt.want {
				if got.Briefs[i]["marker"] != m {
					t.Errorf("briefs[%d].marker = %v, want %s", i, got.Briefs[i]["marker"], m)
				}
			}
		})
	}
}
