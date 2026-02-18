package asana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient(testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	if client == nil {
		t.Fatal("NewClient returned nil")
		return
	}
	if client.baseURL != BaseURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, BaseURL)
	}
	if client.workspaceID != testutil.FakeAsanaWorkspaceID {
		t.Errorf("client.workspaceID = %s, want %s", client.workspaceID, testutil.FakeAsanaWorkspaceID)
	}
}

func TestNewClientWithBaseURL(t *testing.T) {
	customURL := "https://custom.asana.test"
	client := NewClientWithBaseURL(customURL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	if client.baseURL != customURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, customURL)
	}
}

func TestNewClientWithBaseURL_TrimsTrailingSlash(t *testing.T) {
	client := NewClientWithBaseURL("https://custom.asana.test/", testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	if client.baseURL != "https://custom.asana.test" {
		t.Errorf("client.baseURL = %s, want no trailing slash", client.baseURL)
	}
}

func TestGetTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.URL.Path != "/tasks/123456" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testutil.FakeAsanaAccessToken {
			t.Error("missing or incorrect Authorization header")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing Accept header")
		}

		resp := APIResponse[Task]{
			Data: Task{
				GID:   "123456",
				Name:  "Test Task",
				Notes: "Task description",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	task, err := client.GetTask(context.Background(), "123456")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if task.GID != "123456" {
		t.Errorf("task.GID = %s, want 123456", task.GID)
	}
	if task.Name != "Test Task" {
		t.Errorf("task.Name = %s, want Test Task", task.Name)
	}
}

func TestGetTaskWithFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify fields parameter
		fields := r.URL.Query().Get("opt_fields")
		if fields == "" {
			t.Error("expected opt_fields query parameter")
		}

		resp := APIResponse[Task]{
			Data: Task{
				GID:  "123456",
				Name: "Test Task",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	task, err := client.GetTaskWithFields(context.Background(), "123456", []string{"gid", "name", "notes"})
	if err != nil {
		t.Fatalf("GetTaskWithFields failed: %v", err)
	}
	if task.GID != "123456" {
		t.Errorf("task.GID = %s, want 123456", task.GID)
	}
}

func TestUpdateTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data, ok := body["data"].(map[string]interface{})
		if !ok {
			t.Error("expected data wrapper in request body")
		}
		if data["completed"] != true {
			t.Error("expected completed to be true")
		}

		resp := APIResponse[Task]{
			Data: Task{
				GID:       "123456",
				Completed: true,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	task, err := client.UpdateTask(context.Background(), "123456", map[string]interface{}{
		"completed": true,
	})
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}
	if !task.Completed {
		t.Error("expected task to be completed")
	}
}

func TestCompleteTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse[Task]{
			Data: Task{
				GID:       "123456",
				Completed: true,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	task, err := client.CompleteTask(context.Background(), "123456")
	if err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}
	if !task.Completed {
		t.Error("expected task to be completed")
	}
}

func TestAddComment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/tasks/123456/stories" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data, ok := body["data"].(map[string]interface{})
		if !ok {
			t.Error("expected data wrapper in request body")
		}
		if data["text"] != "Test comment" {
			t.Errorf("unexpected comment text: %v", data["text"])
		}

		resp := APIResponse[Story]{
			Data: Story{
				GID:  "789",
				Text: "Test comment",
				Type: "comment",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	story, err := client.AddComment(context.Background(), "123456", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
	if story.Text != "Test comment" {
		t.Errorf("story.Text = %s, want Test comment", story.Text)
	}
}

func TestAddHTMLComment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data, ok := body["data"].(map[string]interface{})
		if !ok {
			t.Error("expected data wrapper in request body")
		}
		if data["html_text"] != "<b>Bold</b> text" {
			t.Errorf("unexpected html_text: %v", data["html_text"])
		}

		resp := APIResponse[Story]{
			Data: Story{
				GID:      "789",
				HTMLText: "<b>Bold</b> text",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	_, err := client.AddHTMLComment(context.Background(), "123456", "<b>Bold</b> text")
	if err != nil {
		t.Fatalf("AddHTMLComment failed: %v", err)
	}
}

func TestAddTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/tasks/123456/addTag" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data := body["data"].(map[string]interface{})
		if data["tag"] != "999" {
			t.Errorf("unexpected tag GID: %v", data["tag"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	err := client.AddTag(context.Background(), "123456", "999")
	if err != nil {
		t.Fatalf("AddTag failed: %v", err)
	}
}

func TestRemoveTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/123456/removeTag" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	err := client.RemoveTag(context.Background(), "123456", "999")
	if err != nil {
		t.Fatalf("RemoveTag failed: %v", err)
	}
}

func TestGetWorkspaceTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/workspaces/" + testutil.FakeAsanaWorkspaceID + "/tags"
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected path: %s, want %s", r.URL.Path, expectedPath)
		}

		resp := PagedResponse[Tag]{
			Data: []Tag{
				{GID: "1", Name: "pilot"},
				{GID: "2", Name: "urgent"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	tags, err := client.GetWorkspaceTags(context.Background())
	if err != nil {
		t.Fatalf("GetWorkspaceTags failed: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}
}

func TestFindTagByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := PagedResponse[Tag]{
			Data: []Tag{
				{GID: "1", Name: "pilot"},
				{GID: "2", Name: "urgent"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)

	// Test case-insensitive match
	tag, err := client.FindTagByName(context.Background(), "PILOT")
	if err != nil {
		t.Fatalf("FindTagByName failed: %v", err)
	}
	if tag == nil {
		t.Fatal("expected to find tag")
		return
	}
	if tag.GID != "1" {
		t.Errorf("tag.GID = %s, want 1", tag.GID)
	}
}

func TestFindTagByName_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := PagedResponse[Tag]{
			Data: []Tag{
				{GID: "1", Name: "other"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	tag, err := client.FindTagByName(context.Background(), "pilot")
	if err != nil {
		t.Fatalf("FindTagByName failed: %v", err)
	}
	if tag != nil {
		t.Error("expected nil when tag not found")
	}
}

func TestCreateTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data := body["data"].(map[string]interface{})
		if data["name"] != "pilot" {
			t.Errorf("unexpected tag name: %v", data["name"])
		}
		if data["workspace"] != testutil.FakeAsanaWorkspaceID {
			t.Errorf("unexpected workspace: %v", data["workspace"])
		}

		resp := APIResponse[Tag]{
			Data: Tag{
				GID:  "999",
				Name: "pilot",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	tag, err := client.CreateTag(context.Background(), "pilot")
	if err != nil {
		t.Fatalf("CreateTag failed: %v", err)
	}
	if tag.Name != "pilot" {
		t.Errorf("tag.Name = %s, want pilot", tag.Name)
	}
}

func TestAddAttachment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/tasks/123456/attachments" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data := body["data"].(map[string]interface{})
		if data["resource_subtype"] != "external" {
			t.Errorf("unexpected resource_subtype: %v", data["resource_subtype"])
		}
		if data["url"] != "https://github.com/owner/repo/pull/123" {
			t.Errorf("unexpected url: %v", data["url"])
		}

		resp := APIResponse[Attachment]{
			Data: Attachment{
				GID:     "attach-1",
				Name:    "PR #123",
				ViewURL: "https://github.com/owner/repo/pull/123",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	attachment, err := client.AddAttachment(context.Background(), "123456", "https://github.com/owner/repo/pull/123", "PR #123")
	if err != nil {
		t.Fatalf("AddAttachment failed: %v", err)
	}
	if attachment.Name != "PR #123" {
		t.Errorf("attachment.Name = %s, want PR #123", attachment.Name)
	}
}

func TestGetWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/workspaces/" + testutil.FakeAsanaWorkspaceID
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected path: %s, want %s", r.URL.Path, expectedPath)
		}

		resp := APIResponse[Workspace]{
			Data: Workspace{
				GID:  testutil.FakeAsanaWorkspaceID,
				Name: "Test Workspace",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	workspace, err := client.GetWorkspace(context.Background())
	if err != nil {
		t.Fatalf("GetWorkspace failed: %v", err)
	}
	if workspace.Name != "Test Workspace" {
		t.Errorf("workspace.Name = %s, want Test Workspace", workspace.Name)
	}
}

func TestPing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse[Workspace]{
			Data: Workspace{
				GID:  testutil.FakeAsanaWorkspaceID,
				Name: "Test Workspace",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

func TestDoRequest_ErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   `{"data": {"gid": "1"}}`,
			wantErr:    false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   `{"errors": [{"message": "Not found"}]}`,
			wantErr:    true,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   `{"errors": [{"message": "Not Authorized"}]}`,
			wantErr:    true,
		},
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			response:   `{"errors": [{"message": "Rate limit exceeded"}]}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
			_, err := client.GetTask(context.Background(), "123")

			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled != false {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}
	if cfg.PilotTag != "pilot" {
		t.Errorf("default PilotTag = %s, want 'pilot'", cfg.PilotTag)
	}
}

func TestPriorityFromTags(t *testing.T) {
	tests := []struct {
		name string
		tags []Tag
		want Priority
	}{
		{
			name: "urgent tag",
			tags: []Tag{{GID: "1", Name: "urgent"}},
			want: PriorityUrgent,
		},
		{
			name: "high tag",
			tags: []Tag{{GID: "1", Name: "High"}},
			want: PriorityHigh,
		},
		{
			name: "medium tag",
			tags: []Tag{{GID: "1", Name: "MEDIUM"}},
			want: PriorityMedium,
		},
		{
			name: "low tag",
			tags: []Tag{{GID: "1", Name: "low"}},
			want: PriorityLow,
		},
		{
			name: "critical tag",
			tags: []Tag{{GID: "1", Name: "Critical"}},
			want: PriorityUrgent,
		},
		{
			name: "no priority tags",
			tags: []Tag{{GID: "1", Name: "bug"}, {GID: "2", Name: "feature"}},
			want: PriorityNone,
		},
		{
			name: "empty tags",
			tags: []Tag{},
			want: PriorityNone,
		},
		{
			name: "priority in mixed tags",
			tags: []Tag{{GID: "1", Name: "bug"}, {GID: "2", Name: "urgent"}, {GID: "3", Name: "feature"}},
			want: PriorityUrgent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriorityFromTags(tt.tags)
			if got != tt.want {
				t.Errorf("PriorityFromTags() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPriorityName(t *testing.T) {
	tests := []struct {
		priority Priority
		want     string
	}{
		{PriorityUrgent, "Urgent"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
		{PriorityNone, "No Priority"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := PriorityName(tt.priority)
			if got != tt.want {
				t.Errorf("PriorityName(%d) = %s, want %s", tt.priority, got, tt.want)
			}
		})
	}
}

func TestConvertToTaskInfo(t *testing.T) {
	task := &Task{
		GID:   "123456",
		Name:  "Implement feature",
		Notes: "Feature description here",
		Tags: []Tag{
			{GID: "1", Name: "pilot"},
			{GID: "2", Name: "urgent"},
		},
		Projects: []Project{
			{GID: "proj-1", Name: "Main Project"},
		},
		Permalink: "https://app.asana.com/0/0/123456",
	}

	info := ConvertToTaskInfo(task)

	if info.ID != "ASANA-123456" {
		t.Errorf("info.ID = %s, want ASANA-123456", info.ID)
	}
	if info.Title != "Implement feature" {
		t.Errorf("info.Title = %s, want Implement feature", info.Title)
	}
	if info.Description != "Feature description here" {
		t.Errorf("info.Description = %s, want Feature description here", info.Description)
	}
	if info.Priority != PriorityUrgent {
		t.Errorf("info.Priority = %d, want %d", info.Priority, PriorityUrgent)
	}
	if len(info.Labels) != 2 {
		t.Errorf("len(info.Labels) = %d, want 2", len(info.Labels))
	}
	if info.TaskGID != "123456" {
		t.Errorf("info.TaskGID = %s, want 123456", info.TaskGID)
	}
	if info.TaskURL != "https://app.asana.com/0/0/123456" {
		t.Errorf("info.TaskURL = %s, want https://app.asana.com/0/0/123456", info.TaskURL)
	}
	if info.ProjectName != "Main Project" {
		t.Errorf("info.ProjectName = %s, want Main Project", info.ProjectName)
	}
}

func TestConvertToTaskInfo_NoPermalink(t *testing.T) {
	task := &Task{
		GID:  "123456",
		Name: "Task without permalink",
	}

	info := ConvertToTaskInfo(task)

	expectedURL := "https://app.asana.com/0/0/123456"
	if info.TaskURL != expectedURL {
		t.Errorf("info.TaskURL = %s, want %s", info.TaskURL, expectedURL)
	}
}

// Integration test helper - verifies client method signatures compile
func TestClientMethodSignatures(t *testing.T) {
	client := NewClient(testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	ctx := context.Background()

	// These won't actually work without a real API, but verify the signatures compile
	var err error

	_, err = client.GetTask(ctx, "123")
	_ = err

	_, err = client.GetTaskWithFields(ctx, "123", []string{"gid", "name"})
	_ = err

	_, err = client.UpdateTask(ctx, "123", map[string]interface{}{"completed": true})
	_ = err

	_, err = client.CompleteTask(ctx, "123")
	_ = err

	_, err = client.AddComment(ctx, "123", "comment")
	_ = err

	_, err = client.AddHTMLComment(ctx, "123", "<b>html</b>")
	_ = err

	_, err = client.GetTaskStories(ctx, "123")
	_ = err

	err = client.AddTag(ctx, "123", "456")
	_ = err

	err = client.RemoveTag(ctx, "123", "456")
	_ = err

	_, err = client.GetWorkspaceTags(ctx)
	_ = err

	_, err = client.FindTagByName(ctx, "pilot")
	_ = err

	_, err = client.CreateTag(ctx, "newtag")
	_ = err

	_, err = client.AddAttachment(ctx, "123", "https://example.com", "Link")
	_ = err

	_, err = client.GetProject(ctx, "proj-1")
	_ = err

	_, err = client.GetWorkspace(ctx)
	_ = err

	_, err = client.SearchTasks(ctx, "query")
	_ = err

	_, err = client.GetTasksByTag(ctx, "tag-1")
	_ = err

	err = client.Ping(ctx)
	_ = err
}
