package plane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://api.plane.so/", testutil.FakePlaneAPIKey)
	if c.baseURL != "https://api.plane.so" {
		t.Errorf("expected trailing slash stripped, got %s", c.baseURL)
	}
	if c.apiKey != testutil.FakePlaneAPIKey {
		t.Errorf("expected apiKey %s, got %s", testutil.FakePlaneAPIKey, c.apiKey)
	}
}

func TestNewClientWithOption(t *testing.T) {
	custom := &http.Client{}
	c := NewClient("https://plane.example.com", testutil.FakePlaneAPIKey, WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("expected custom HTTP client to be set")
	}
}

func TestListWorkItems(t *testing.T) {
	tests := []struct {
		name      string
		labelID   string
		wantPath  string
		items     []WorkItem
		wantCount int
	}{
		{
			name:     "without label filter",
			labelID:  "",
			wantPath: "/api/v1/workspaces/ws/projects/proj-1/work-items/",
			items: []WorkItem{
				{ID: "wi-1", Name: "First"},
				{ID: "wi-2", Name: "Second"},
			},
			wantCount: 2,
		},
		{
			name:     "with label filter",
			labelID:  "lbl-abc",
			wantPath: "/api/v1/workspaces/ws/projects/proj-1/work-items/?label=lbl-abc",
			items: []WorkItem{
				{ID: "wi-3", Name: "Filtered"},
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-API-Key") != testutil.FakePlaneAPIKey {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if r.URL.RequestURI() != tt.wantPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.RequestURI(), tt.wantPath)
				}
				resp := paginatedResponse{Results: tt.items, TotalCount: len(tt.items)}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
			items, err := c.ListWorkItems(context.Background(), "ws", "proj-1", tt.labelID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(items) != tt.wantCount {
				t.Errorf("got %d items, want %d", len(items), tt.wantCount)
			}
		})
	}
}

func TestGetWorkItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		wantPath := "/api/v1/workspaces/ws/projects/proj-1/work-items/wi-42/"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
		}
		item := WorkItem{ID: "wi-42", Name: "Test Item", Priority: PriorityHigh}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	item, err := c.GetWorkItem(context.Background(), "ws", "proj-1", "wi-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.ID != "wi-42" {
		t.Errorf("expected ID wi-42, got %s", item.ID)
	}
	if item.Priority != PriorityHigh {
		t.Errorf("expected priority %d, got %d", PriorityHigh, item.Priority)
	}
}

func TestUpdateWorkItem(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	err := c.UpdateWorkItem(context.Background(), "ws", "proj-1", "wi-42", map[string]interface{}{
		"state": "state-uuid",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["state"] != "state-uuid" {
		t.Errorf("expected state field in body, got %v", gotBody)
	}
}

func TestCreateWorkItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		item := WorkItem{ID: "wi-new", Name: "New Item"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	item, err := c.CreateWorkItem(context.Background(), "ws", "proj-1", map[string]interface{}{
		"name": "New Item",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.ID != "wi-new" {
		t.Errorf("expected ID wi-new, got %s", item.ID)
	}
}

func TestListStates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/workspaces/ws/projects/proj-1/states/"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
		}
		resp := statesResponse{Results: []State{
			{ID: "s-1", Name: "Backlog", Group: StateGroupBacklog},
			{ID: "s-2", Name: "In Progress", Group: StateGroupStarted},
			{ID: "s-3", Name: "Done", Group: StateGroupCompleted},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	states, err := c.ListStates(context.Background(), "ws", "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(states) != 3 {
		t.Errorf("expected 3 states, got %d", len(states))
	}
	if states[0].Group != StateGroupBacklog {
		t.Errorf("expected first state group backlog, got %s", states[0].Group)
	}
}

func TestListLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/workspaces/ws/projects/proj-1/labels/"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
		}
		resp := labelsResponse{Results: []Label{
			{ID: "lbl-1", Name: "pilot", Color: "#ff0000"},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	labels, err := c.ListLabels(context.Background(), "ws", "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label, got %d", len(labels))
	}
	if labels[0].Name != "pilot" {
		t.Errorf("expected label name pilot, got %s", labels[0].Name)
	}
}

func TestAddComment(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		wantPath := "/api/v1/workspaces/ws/projects/proj-1/work-items/wi-42/comments/"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	err := c.AddComment(context.Background(), "ws", "proj-1", "wi-42", "<p>Hello</p>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["comment_html"] != "<p>Hello</p>" {
		t.Errorf("expected comment_html in body, got %v", gotBody)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	_, err := c.GetWorkItem(context.Background(), "ws", "proj-1", "missing")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestRateLimitRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		item := WorkItem{ID: "wi-1", Name: "Retried"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	item, err := c.GetWorkItem(context.Background(), "ws", "proj-1", "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", calls)
	}
	if item.Name != "Retried" {
		t.Errorf("expected Name 'Retried', got %s", item.Name)
	}
}

func TestPriorityName(t *testing.T) {
	tests := []struct {
		p    Priority
		want string
	}{
		{PriorityNone, "None"},
		{PriorityUrgent, "Urgent"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
	}
	for _, tt := range tests {
		got := PriorityName(tt.p)
		if got != tt.want {
			t.Errorf("PriorityName(%d) = %s, want %s", tt.p, got, tt.want)
		}
	}
}

func TestUpdateIssueState(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	err := c.UpdateIssueState(context.Background(), "ws", "proj-1", "wi-42", "state-started-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["state"] != "state-started-uuid" {
		t.Errorf("expected state field 'state-started-uuid', got %v", gotBody["state"])
	}
}

func TestResolveStateByGroup(t *testing.T) {
	tests := []struct {
		name     string
		group    StateGroup
		states   []State
		wantID   string
		wantEmpty bool
	}{
		{
			name:  "finds started state",
			group: StateGroupStarted,
			states: []State{
				{ID: "s-1", Name: "Backlog", Group: StateGroupBacklog},
				{ID: "s-2", Name: "In Progress", Group: StateGroupStarted},
				{ID: "s-3", Name: "Done", Group: StateGroupCompleted},
			},
			wantID: "s-2",
		},
		{
			name:  "finds completed state",
			group: StateGroupCompleted,
			states: []State{
				{ID: "s-1", Name: "Backlog", Group: StateGroupBacklog},
				{ID: "s-3", Name: "Done", Group: StateGroupCompleted},
			},
			wantID: "s-3",
		},
		{
			name:  "returns first match when multiple in same group",
			group: StateGroupStarted,
			states: []State{
				{ID: "s-2a", Name: "In Progress", Group: StateGroupStarted},
				{ID: "s-2b", Name: "In Review", Group: StateGroupStarted},
			},
			wantID: "s-2a",
		},
		{
			name:  "returns empty when no match",
			group: StateGroupCancelled,
			states: []State{
				{ID: "s-1", Name: "Backlog", Group: StateGroupBacklog},
			},
			wantID:    "",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				resp := statesResponse{Results: tt.states}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
			id, err := c.ResolveStateByGroup(context.Background(), "ws", "proj-1", tt.group)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("got state ID %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestListComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/workspaces/ws/projects/proj-1/work-items/wi-42/comments/"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
		}
		resp := commentsResponse{Results: []Comment{
			{ID: "c-1", CommentHTML: "<p>First</p>", ExternalSource: "pilot", ExternalID: "pilot-pr-1-wi-42"},
			{ID: "c-2", CommentHTML: "<p>Second</p>"},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	comments, err := c.ListComments(context.Background(), "ws", "proj-1", "wi-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].ExternalID != "pilot-pr-1-wi-42" {
		t.Errorf("expected external_id 'pilot-pr-1-wi-42', got %s", comments[0].ExternalID)
	}
}

func TestAddCommentWithTracking(t *testing.T) {
	t.Run("posts new comment when no duplicate exists", func(t *testing.T) {
		calls := 0
		var postedBody map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method == http.MethodGet {
				// ListComments returns empty
				resp := commentsResponse{Results: []Comment{}}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			if r.Method == http.MethodPost {
				_ = json.NewDecoder(r.Body).Decode(&postedBody)
				w.WriteHeader(http.StatusOK)
				return
			}
		}))
		defer srv.Close()

		c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
		err := c.AddCommentWithTracking(
			context.Background(), "ws", "proj-1", "wi-42",
			"<p>PR #5</p>", "pilot", "pilot-pr-5-wi-42",
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if postedBody["comment_html"] != "<p>PR #5</p>" {
			t.Errorf("expected comment_html '<p>PR #5</p>', got %s", postedBody["comment_html"])
		}
		if postedBody["external_source"] != "pilot" {
			t.Errorf("expected external_source 'pilot', got %s", postedBody["external_source"])
		}
		if postedBody["external_id"] != "pilot-pr-5-wi-42" {
			t.Errorf("expected external_id 'pilot-pr-5-wi-42', got %s", postedBody["external_id"])
		}
		if postedBody["access"] != "INTERNAL" {
			t.Errorf("expected access 'INTERNAL', got %s", postedBody["access"])
		}
	})

	t.Run("skips posting when duplicate external_id exists", func(t *testing.T) {
		postCalled := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				// Return existing comment with matching external_id
				resp := commentsResponse{Results: []Comment{
					{ID: "c-existing", CommentHTML: "<p>PR #5</p>", ExternalSource: "pilot", ExternalID: "pilot-pr-5-wi-42"},
				}}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			if r.Method == http.MethodPost {
				postCalled = true
				w.WriteHeader(http.StatusOK)
				return
			}
		}))
		defer srv.Close()

		c := NewClient(srv.URL, testutil.FakePlaneAPIKey)
		err := c.AddCommentWithTracking(
			context.Background(), "ws", "proj-1", "wi-42",
			"<p>PR #5</p>", "pilot", "pilot-pr-5-wi-42",
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if postCalled {
			t.Error("expected POST to be skipped for duplicate external_id")
		}
	})
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("expected default config to be disabled")
	}
	if cfg.BaseURL != "https://api.plane.so" {
		t.Errorf("expected default BaseURL https://api.plane.so, got %s", cfg.BaseURL)
	}
	if cfg.PilotLabel != "pilot" {
		t.Errorf("expected default PilotLabel pilot, got %s", cfg.PilotLabel)
	}
}
