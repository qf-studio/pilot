package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// fakeProjectSourceServer returns an httptest.Server that serves a fixed sequence of
// GraphQL responses. Each call to the /graphql endpoint pops the next response.
func fakeProjectSourceServer(t *testing.T, responses []string) *httptest.Server {
	t.Helper()
	idx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if idx >= len(responses) {
			t.Errorf("unexpected GraphQL request #%d (only %d responses configured)", idx+1, len(responses))
			http.Error(w, "no more responses", http.StatusInternalServerError)
			return
		}
		resp := responses[idx]
		idx++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
}

// orgProjectResp returns a project-by-org GraphQL response with the given project ID.
func orgProjectResp(projectID string) string {
	return `{"data":{"organization":{"projectV2":{"id":"` + projectID + `"}}}}`
}

// itemsResp builds a single-page project items response for the given nodes.
// hasNextPage controls pagination; endCursor is only meaningful when hasNextPage=true.
func itemsResp(nodes []map[string]interface{}, hasNextPage bool, endCursor string) string {
	data := map[string]interface{}{
		"node": map[string]interface{}{
			"items": map[string]interface{}{
				"pageInfo": map[string]interface{}{
					"hasNextPage": hasNextPage,
					"endCursor":   endCursor,
				},
				"nodes": nodes,
			},
		},
	}
	b, _ := json.Marshal(map[string]interface{}{"data": data})
	return string(b)
}

// issueNode builds a projectBoardItemNode JSON representation.
func issueNode(number int, nodeID, title, body, state, repo, status string, labelNames ...string) map[string]interface{} {
	labelNodes := make([]map[string]interface{}, len(labelNames))
	for i, l := range labelNames {
		labelNodes[i] = map[string]interface{}{"name": l}
	}
	return map[string]interface{}{
		"content": map[string]interface{}{
			"number": number,
			"id":     nodeID,
			"title":  title,
			"body":   body,
			"state":  state,
			"labels": map[string]interface{}{"nodes": labelNodes},
			"repository": map[string]interface{}{
				"nameWithOwner": repo,
			},
		},
		"fieldValueByName": map[string]interface{}{
			"name": status,
		},
	}
}

// draftNode builds a non-issue item (number=0, no content fields set).
func draftNode() map[string]interface{} {
	return map[string]interface{}{
		"content":          map[string]interface{}{},
		"fieldValueByName": map[string]interface{}{"name": "Todo"},
	}
}

func TestFindIssuesFromProject_SinglePage(t *testing.T) {
	server := fakeProjectSourceServer(t, []string{
		orgProjectResp("PVT_proj1"),
		itemsResp([]map[string]interface{}{
			issueNode(10, "I_10", "Fix bug", "body10", "OPEN", "myorg/myrepo", "Todo", "pilot"),
			issueNode(11, "I_11", "Add feature", "body11", "OPEN", "myorg/myrepo", "In Progress"),
		}, false, ""),
	})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "myorg", "myrepo")

	issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (only Todo), got %d", len(issues))
	}
	if issues[0].Number != 10 {
		t.Errorf("expected issue #10, got #%d", issues[0].Number)
	}
	if issues[0].Title != "Fix bug" {
		t.Errorf("expected title 'Fix bug', got %q", issues[0].Title)
	}
	if issues[0].Body != "body10" {
		t.Errorf("expected body 'body10', got %q", issues[0].Body)
	}
	if issues[0].State != "open" {
		t.Errorf("expected state 'open', got %q", issues[0].State)
	}
	if len(issues[0].Labels) != 1 || issues[0].Labels[0].Name != "pilot" {
		t.Errorf("expected label 'pilot', got %v", issues[0].Labels)
	}
}

func TestFindIssuesFromProject_Pagination(t *testing.T) {
	page1Node := issueNode(1, "I_1", "Issue 1", "", "OPEN", "org/repo", "Todo")
	page2Node := issueNode(2, "I_2", "Issue 2", "", "OPEN", "org/repo", "Todo")

	server := fakeProjectSourceServer(t, []string{
		orgProjectResp("PVT_paged"),
		itemsResp([]map[string]interface{}{page1Node}, true, "cursor-abc"),
		itemsResp([]map[string]interface{}{page2Node}, false, ""),
	})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 2,
		StatusField:   "Status",
	}, "org", "repo")

	issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues across 2 pages, got %d", len(issues))
	}
	if issues[0].Number != 1 || issues[1].Number != 2 {
		t.Errorf("unexpected issue numbers: %v, %v", issues[0].Number, issues[1].Number)
	}
}

func TestFindIssuesFromProject_CrossRepoExclusion(t *testing.T) {
	server := fakeProjectSourceServer(t, []string{
		orgProjectResp("PVT_crossrepo"),
		itemsResp([]map[string]interface{}{
			issueNode(5, "I_5", "Same repo", "", "OPEN", "org/repo", "Todo"),
			issueNode(6, "I_6", "Other repo", "", "OPEN", "org/other-repo", "Todo"),
			issueNode(7, "I_7", "Different org", "", "OPEN", "other-org/repo", "Todo"),
		}, false, ""),
	})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 3,
		StatusField:   "Status",
	}, "org", "repo")

	issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (cross-repo filtered), got %d: %v", len(issues), issues)
	}
	if issues[0].Number != 5 {
		t.Errorf("expected issue #5 to survive filter, got #%d", issues[0].Number)
	}
}

func TestFindIssuesFromProject_LabelHydration(t *testing.T) {
	server := fakeProjectSourceServer(t, []string{
		orgProjectResp("PVT_labels"),
		itemsResp([]map[string]interface{}{
			issueNode(20, "I_20", "Labeled", "", "OPEN", "org/repo", "Todo", "pilot", "priority:high", "bug"),
		}, false, ""),
	})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 4,
		StatusField:   "Status",
	}, "org", "repo")

	issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	gotLabels := make(map[string]bool)
	for _, l := range issues[0].Labels {
		gotLabels[l.Name] = true
	}
	for _, want := range []string{"pilot", "priority:high", "bug"} {
		if !gotLabels[want] {
			t.Errorf("expected label %q, not found in %v", want, issues[0].Labels)
		}
	}
}

func TestFindIssuesFromProject_ClosedIssueExclusion(t *testing.T) {
	cases := []struct {
		name        string
		state       string
		wantInclude bool
	}{
		{"open uppercase", "OPEN", true},
		{"open lowercase", "open", true},
		{"closed", "CLOSED", false},
		{"closed lowercase", "closed", false},
		{"merged", "MERGED", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := fakeProjectSourceServer(t, []string{
				orgProjectResp("PVT_closed"),
				itemsResp([]map[string]interface{}{
					issueNode(42, "I_42", "Some issue", "", tc.state, "org/repo", "Todo"),
				}, false, ""),
			})
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			src := NewProjectBoardSource(client, &ProjectBoardConfig{
				ProjectNumber: 99,
				StatusField:   "Status",
			}, "org", "repo")

			issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
			if err != nil {
				t.Fatalf("FindIssuesFromProject() error = %v", err)
			}
			if tc.wantInclude && len(issues) != 1 {
				t.Errorf("state=%q: expected issue to be included, got %d issues", tc.state, len(issues))
			}
			if !tc.wantInclude && len(issues) != 0 {
				t.Errorf("state=%q: expected issue to be excluded, got %d issues", tc.state, len(issues))
			}
		})
	}
}

func TestFindIssuesFromProject_DraftIssuesSkipped(t *testing.T) {
	server := fakeProjectSourceServer(t, []string{
		orgProjectResp("PVT_draft"),
		itemsResp([]map[string]interface{}{
			draftNode(),
			issueNode(30, "I_30", "Real issue", "", "OPEN", "org/repo", "Todo"),
		}, false, ""),
	})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 5,
		StatusField:   "Status",
	}, "org", "repo")

	issues, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 30 {
		t.Errorf("expected only issue #30, got %v", issues)
	}
}

func TestFindIssuesFromProject_DefaultStatusField(t *testing.T) {
	var capturedVars map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(req.Query, "organization") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(orgProjectResp("PVT_default")))
			return
		}
		capturedVars = req.Variables
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(itemsResp(nil, false, "")))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	// StatusField intentionally empty — should default to "Status".
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 6,
	}, "org", "repo")

	_, err := src.FindIssuesFromProject(context.Background(), "Todo")
	if err != nil {
		t.Fatalf("FindIssuesFromProject() error = %v", err)
	}
	if capturedVars["statusField"] != "Status" {
		t.Errorf("expected statusField='Status', got %v", capturedVars["statusField"])
	}
}

func TestFindIssuesFromProject_CachesProjectID(t *testing.T) {
	resolveCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if strings.Contains(req.Query, "organization") {
			resolveCount++
			_, _ = w.Write([]byte(orgProjectResp("PVT_cached")))
			return
		}
		_, _ = w.Write([]byte(itemsResp(nil, false, "")))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 7,
		StatusField:   "Status",
	}, "org", "repo")

	for i := 0; i < 3; i++ {
		if _, err := src.FindIssuesFromProject(context.Background(), "Todo"); err != nil {
			t.Fatalf("call %d: error = %v", i, err)
		}
	}
	if resolveCount != 1 {
		t.Errorf("expected project ID resolved once, got %d", resolveCount)
	}
}

// TestSourceEnabledFalse_UsesLabelPath is a regression guard: when source_enabled=false
// (i.e. projectBoardSource is nil), findOldestUnprocessedIssue must reach the
// label-based REST path (ListIssues), not the board GraphQL path.
func TestSourceEnabledFalse_UsesLabelPath(t *testing.T) {
	restCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			restCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request to %s (board GraphQL called despite source_enabled=false)", r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, err := NewPoller(client, "org/repo", "pilot", 0)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	// projectBoardSource is nil → must use label path
	if poller.projectBoardSource != nil {
		t.Fatal("expected projectBoardSource to be nil when not configured")
	}

	_, err = poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if !restCalled {
		t.Error("expected REST /repos/... to be called for label path, but it wasn't")
	}
}
