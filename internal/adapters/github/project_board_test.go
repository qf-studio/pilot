package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewProjectBoardSync(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	tests := []struct {
		name   string
		config *ProjectBoardConfig
		wantNil bool
	}{
		{
			name:    "nil config returns nil",
			config:  nil,
			wantNil: true,
		},
		{
			name:    "disabled config returns nil",
			config:  &ProjectBoardConfig{Enabled: false},
			wantNil: true,
		},
		{
			name: "enabled config returns instance",
			config: &ProjectBoardConfig{
				Enabled:       true,
				ProjectNumber: 1,
				StatusField:   "Status",
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewProjectBoardSync(client, tt.config, "testorg")
			if (result == nil) != tt.wantNil {
				t.Errorf("NewProjectBoardSync() nil = %v, wantNil %v", result == nil, tt.wantNil)
			}
		})
	}
}

func TestUpdateProjectItemStatus_EmptyStatus(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	pbs := &ProjectBoardSync{
		client: client,
		config: &ProjectBoardConfig{Enabled: true, ProjectNumber: 1, StatusField: "Status"},
		owner:  "testorg",
	}

	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_123", "")
	if err != nil {
		t.Errorf("expected nil for empty status, got %v", err)
	}
}

func TestUpdateProjectItemStatus_FullFlow(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_org123"}}}}`
			requestCount++
		case strings.Contains(req.Query, "field(name:"):
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_field1","options":[{"id":"OPT_todo","name":"Todo"},{"id":"OPT_indev","name":"In Dev"},{"id":"OPT_done","name":"Done"}]}}}}`
			requestCount++
		case strings.Contains(req.Query, "projectItems"):
			resp = `{"data":{"node":{"projectItems":{"nodes":[{"id":"PVTI_item1","project":{"id":"PVT_org123"}}]}}}}`
			requestCount++
		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			resp = `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_item1"}}}}`
			requestCount++
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 5,
		StatusField:   "Status",
	}, "testorg")

	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_node1", "In Dev")
	if err != nil {
		t.Fatalf("UpdateProjectItemStatus() error = %v", err)
	}

	if requestCount != 4 {
		t.Errorf("expected 4 GraphQL requests, got %d", requestCount)
	}
}

func TestUpdateProjectItemStatus_CachesIDs(t *testing.T) {
	var resolveCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resolveCount.Add(1)
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_cached"}}}}`
		case strings.Contains(req.Query, "field(name:"):
			resolveCount.Add(1)
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_done","name":"Done"}]}}}}`
		case strings.Contains(req.Query, "projectItems"):
			resp = `{"data":{"node":{"projectItems":{"nodes":[{"id":"PVTI_i1","project":{"id":"PVT_cached"}}]}}}}`
		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			resp = `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_i1"}}}}`
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "testorg")

	// Call twice — resolution queries should only happen once.
	for i := 0; i < 2; i++ {
		err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_1", "Done")
		if err != nil {
			t.Fatalf("call %d: UpdateProjectItemStatus() error = %v", i+1, err)
		}
	}

	if resolveCount.Load() != 2 { // 1 for org project, 1 for field+options
		t.Errorf("expected 2 resolve requests (cached), got %d", resolveCount.Load())
	}
}

func TestResolveProjectID_OrgFirstUserFallback(t *testing.T) {
	tests := []struct {
		name        string
		orgResp     string
		orgStatus   int
		userResp    string
		userStatus  int
		wantID      string
		wantErr     bool
	}{
		{
			name:      "org found",
			orgResp:   `{"data":{"organization":{"projectV2":{"id":"PVT_org"}}}}`,
			orgStatus: http.StatusOK,
			wantID:    "PVT_org",
		},
		{
			name:       "org empty, user found",
			orgResp:    `{"data":{"organization":{"projectV2":{"id":""}}}}`,
			orgStatus:  http.StatusOK,
			userResp:   `{"data":{"user":{"projectV2":{"id":"PVT_user"}}}}`,
			userStatus: http.StatusOK,
			wantID:     "PVT_user",
		},
		{
			name:       "org error, user found",
			orgResp:    `{"data":null,"errors":[{"message":"not an org"}]}`,
			orgStatus:  http.StatusOK,
			userResp:   `{"data":{"user":{"projectV2":{"id":"PVT_user2"}}}}`,
			userStatus: http.StatusOK,
			wantID:     "PVT_user2",
		},
		{
			name:       "both fail",
			orgResp:    `{"data":null,"errors":[{"message":"not an org"}]}`,
			orgStatus:  http.StatusOK,
			userResp:   `{"data":null,"errors":[{"message":"not found"}]}`,
			userStatus: http.StatusOK,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var callNum int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req GraphQLRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode: %v", err)
				}

				callNum++
				if strings.Contains(req.Query, "organization") {
					w.WriteHeader(tt.orgStatus)
					_, _ = w.Write([]byte(tt.orgResp))
				} else if strings.Contains(req.Query, "user") {
					w.WriteHeader(tt.userStatus)
					_, _ = w.Write([]byte(tt.userResp))
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			pbs := &ProjectBoardSync{
				client: client,
				config: &ProjectBoardConfig{ProjectNumber: 3},
				owner:  "testowner",
			}

			id, err := pbs.resolveProjectID(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveProjectID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && id != tt.wantID {
				t.Errorf("resolveProjectID() = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestUpdateProjectItemStatus_IssueNotInProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_p1"}}}}`
		case strings.Contains(req.Query, "field(name:"):
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_done","name":"Done"}]}}}}`
		case strings.Contains(req.Query, "projectItems"):
			// Issue not in this project — different project ID.
			resp = `{"data":{"node":{"projectItems":{"nodes":[{"id":"PVTI_other","project":{"id":"PVT_different"}}]}}}}`
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "testorg")

	// Should return nil (not error) when issue isn't in project.
	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_orphan", "Done")
	if err != nil {
		t.Errorf("expected nil for issue not in project, got %v", err)
	}
}

func TestUpdateProjectItemStatus_StatusNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_p1"}}}}`
		case strings.Contains(req.Query, "field(name:"):
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_todo","name":"Todo"}]}}}}`
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "testorg")

	// "Nonexistent" isn't in the options — should return nil.
	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_1", "Nonexistent")
	if err != nil {
		t.Errorf("expected nil for unknown status, got %v", err)
	}
}

func TestUpdateProjectItemStatus_CaseInsensitiveMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_p1"}}}}`
		case strings.Contains(req.Query, "field(name:"):
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_indev","name":"In Dev"}]}}}}`
		case strings.Contains(req.Query, "projectItems"):
			resp = `{"data":{"node":{"projectItems":{"nodes":[{"id":"PVTI_i1","project":{"id":"PVT_p1"}}]}}}}`
		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			// Verify the correct option ID was resolved.
			vars := req.Variables
			if vars["optionID"] != "OPT_indev" {
				t.Errorf("expected optionID OPT_indev, got %v", vars["optionID"])
			}
			resp = `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_i1"}}}}`
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "testorg")

	// "in dev" should match "In Dev" (case insensitive).
	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_1", "in dev")
	if err != nil {
		t.Fatalf("UpdateProjectItemStatus() error = %v", err)
	}
}

func TestUpdateProjectItemStatus_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"insufficient permissions"}]}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "testorg")

	err := pbs.UpdateProjectItemStatus(context.Background(), "ISSUE_1", "Done")
	if err == nil {
		t.Fatal("expected error for GraphQL failure")
	}
	if !strings.Contains(err.Error(), "insufficient permissions") {
		t.Errorf("error should mention permissions, got: %v", err)
	}
}

func TestEnsureResolved_ConcurrentAccess(t *testing.T) {
	var resolveCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}

		var resp string
		switch {
		case strings.Contains(req.Query, "organization"):
			resolveCount.Add(1)
			resp = `{"data":{"organization":{"projectV2":{"id":"PVT_conc"}}}}`
		case strings.Contains(req.Query, "field(name:"):
			resolveCount.Add(1)
			resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_done","name":"Done"}]}}}}`
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := &ProjectBoardSync{
		client: client,
		config: &ProjectBoardConfig{ProjectNumber: 1, StatusField: "Status"},
		owner:  "testorg",
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pbs.ensureResolved(context.Background())
		}()
	}
	wg.Wait()

	// Should resolve at most 2 times (project + field), not 20.
	if resolveCount.Load() > 2 {
		t.Errorf("expected at most 2 resolve calls (cached), got %d", resolveCount.Load())
	}
}

func TestExecuteGraphQL_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"test":"value"}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var result map[string]string
	err := client.ExecuteGraphQL(context.Background(), `{ test }`, nil, &result)
	if err != nil {
		t.Fatalf("ExecuteGraphQL() error = %v", err)
	}
	if result["test"] != "value" {
		t.Errorf("expected test=value, got %v", result)
	}
}

func TestExecuteGraphQL_GraphQLErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"not found"}]}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	err := client.ExecuteGraphQL(context.Background(), `{ test }`, nil, nil)
	if err == nil {
		t.Fatal("expected error for GraphQL errors response")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

func TestExecuteGraphQL_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	err := client.ExecuteGraphQL(context.Background(), `{ test }`, nil, nil)
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status code 401, got: %v", err)
	}
}

func TestResolveFieldAndOptions_DefaultFieldName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Verify the default field name "Status" is used.
		if req.Variables["fieldName"] != "Status" {
			t.Errorf("expected fieldName=Status, got %v", req.Variables["fieldName"])
		}

		resp := `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_1","name":"Todo"}]}}}}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	pbs := &ProjectBoardSync{
		client:    client,
		config:    &ProjectBoardConfig{StatusField: ""}, // empty — should default to "Status"
		owner:     "testorg",
		projectID: "PVT_test",
	}

	fieldID, opts, err := pbs.resolveFieldAndOptions(context.Background())
	if err != nil {
		t.Fatalf("resolveFieldAndOptions() error = %v", err)
	}
	if fieldID != "PVTSSF_f1" {
		t.Errorf("fieldID = %q, want PVTSSF_f1", fieldID)
	}
	if opts["todo"] != "OPT_1" {
		t.Errorf("expected option todo=OPT_1, got %v", opts)
	}
}
