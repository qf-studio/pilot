package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// childFixture describes one child issue of a fixture epic-parent, along with
// the PRs the mock server reports for it when queried by SearchPRsForIssue.
type childFixture struct {
	number int
	closed bool
	prs    []prFixture
}

type prFixture struct {
	number int
	merged bool
	url    string
}

// newEpicCloseTestServer builds an httptest server that fakes the GitHub REST
// + GraphQL surface closeEpicParentIfChildrenShipped depends on for a single
// parent issue number (100): GetIssue/GetIssueNodeID, the native sub-issues
// GraphQL query, SearchOpenPRsForIssue, SearchPRsForIssue, and the
// label/comment/close mutations. Returns the server plus recorders the test
// can assert against.
func newEpicCloseTestServer(t *testing.T, parentClosed bool, openParentPRs int, children []childFixture) (*httptest.Server, *bool, *string) {
	t.Helper()

	closeCalled := false
	var commentBody string

	childByNumber := map[int]childFixture{}
	for _, c := range children {
		childByNumber[c.number] = c
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/100":
			state := "open"
			if parentClosed {
				state = "closed"
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"number":100,"node_id":"node_100","state":%q}`, state)

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "is:open"):
			// SearchOpenPRsForIssue for the parent itself.
			items := make([]map[string]interface{}, openParentPRs)
			for i := range items {
				items[i] = map[string]interface{}{
					"id": i + 1, "number": 900 + i, "title": "manual PR", "state": "open",
					"html_url": fmt.Sprintf("https://github.com/owner/repo/pull/%d", 900+i),
				}
			}
			resp := map[string]interface{}{"total_count": len(items), "items": items}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			// SearchPRsForIssue for a child issue: "repo:owner/repo is:pr #<num>"
			var childNum int
			_, _ = fmt.Sscanf(r.URL.Query().Get("q"), "repo:owner/repo is:pr #%d", &childNum)
			cand := childByNumber[childNum]
			items := make([]map[string]interface{}, 0, len(cand.prs))
			for _, pr := range cand.prs {
				mergedAt := ""
				if pr.merged {
					mergedAt = "2026-01-01T00:00:00Z"
				}
				items = append(items, map[string]interface{}{
					"id": pr.number, "number": pr.number, "title": "fix: work", "state": "closed",
					"html_url":     pr.url,
					"pull_request": map[string]interface{}{"merged_at": mergedAt},
				})
			}
			resp := map[string]interface{}{"total_count": len(items), "items": items}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			// allSubIssueStates native sub-issues query.
			nodes := make([]map[string]interface{}, 0, len(children))
			for _, c := range children {
				state := "OPEN"
				if c.closed {
					state = "CLOSED"
				}
				nodes = append(nodes, map[string]interface{}{"number": c.number, "state": state})
			}
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"subIssues": map[string]interface{}{
							"totalCount": len(nodes),
							"nodes":      nodes,
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			commentBody = body.Body
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))

		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/100":
			closeCalled = true
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))

	return server, &closeCalled, &commentBody
}

// TestCloseEpicParentIfChildrenShipped covers the GH-3939 poll-cycle epic
// close check's core scenarios:
//
//	(a) all children closed and merged -> parent closes in one call
//	(b) one child still open -> parent stays open, no WARN-level veto log
//	(c) a closed child with no merged PR -> vetoed, WARN log names the reason
//	(d) parent already closed -> no-op
func TestCloseEpicParentIfChildrenShipped(t *testing.T) {
	tests := []struct {
		name            string
		parentClosed    bool
		openParentPRs   int
		children        []childFixture
		wantClosed      bool
		wantCommentSubs []string // substrings the summary comment must contain
		wantWarnLog     bool
		wantWarnSubstr  string
		wantNoWarnLog   bool
	}{
		{
			name: "all children merged closes parent within one poll cycle",
			children: []childFixture{
				{number: 101, closed: true, prs: []prFixture{{number: 201, merged: true, url: "https://github.com/owner/repo/pull/201"}}},
				{number: 102, closed: true, prs: []prFixture{{number: 202, merged: true, url: "https://github.com/owner/repo/pull/202"}}},
			},
			wantClosed:      true,
			wantCommentSubs: []string{"pull/201", "pull/202"},
		},
		{
			name: "one child still open leaves parent open without veto log spam",
			children: []childFixture{
				{number: 101, closed: false},
				{number: 102, closed: true, prs: []prFixture{{number: 202, merged: true, url: "https://github.com/owner/repo/pull/202"}}},
			},
			wantClosed:    false,
			wantNoWarnLog: true,
		},
		{
			name: "child closed but PR unmerged vetoes with explicit reason",
			children: []childFixture{
				{number: 101, closed: true, prs: []prFixture{{number: 201, merged: false, url: "https://github.com/owner/repo/pull/201"}}},
			},
			wantClosed:     false,
			wantWarnLog:    true,
			wantWarnSubstr: "child closed without a merged PR",
		},
		{
			name:         "parent already closed is a no-op",
			parentClosed: true,
			children: []childFixture{
				{number: 101, closed: true, prs: []prFixture{{number: 201, merged: true, url: "https://github.com/owner/repo/pull/201"}}},
			},
			wantClosed: false,
		},
		{
			name:          "open PR referencing parent vetoes the close",
			openParentPRs: 1,
			children: []childFixture{
				{number: 101, closed: true, prs: []prFixture{{number: 201, merged: true, url: "https://github.com/owner/repo/pull/201"}}},
			},
			wantClosed:     false,
			wantWarnLog:    true,
			wantWarnSubstr: "open PR references parent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, closeCalled, commentBody := newEpicCloseTestServer(t, tt.parentClosed, tt.openParentPRs, tt.children)
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			logger, buf := newCapturingLogger()
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
			c.log = logger

			c.closeEpicParentIfChildrenShipped(context.Background(), 100)

			if *closeCalled != tt.wantClosed {
				t.Errorf("parent closed = %v, want %v (log: %s)", *closeCalled, tt.wantClosed, buf.String())
			}

			for _, sub := range tt.wantCommentSubs {
				if !strings.Contains(*commentBody, sub) {
					t.Errorf("summary comment %q missing substring %q", *commentBody, sub)
				}
			}

			out := buf.String()
			if tt.wantNoWarnLog && strings.Contains(out, "level=WARN") {
				t.Errorf("expected no WARN-level veto log, got: %s", out)
			}
			if tt.wantWarnLog {
				if !strings.Contains(out, "level=WARN") {
					t.Errorf("expected a WARN-level veto log, got: %s", out)
				}
				if tt.wantWarnSubstr != "" && !strings.Contains(out, tt.wantWarnSubstr) {
					t.Errorf("expected veto log to contain %q, got: %s", tt.wantWarnSubstr, out)
				}
			}
		})
	}
}

// TestPollCloseEpicParents_SearchWiring verifies pollCloseEpicParents fetches
// candidates via SearchOpenPilotIssuesWithSubIssues and drives each one
// through closeEpicParentIfChildrenShipped, and that a search failure is a
// logged no-op rather than a panic.
func TestPollCloseEpicParents_SearchWiring(t *testing.T) {
	t.Run("closes a candidate returned by search", func(t *testing.T) {
		closeCalled := false
		children := []childFixture{
			{number: 101, closed: true, prs: []prFixture{{number: 201, merged: true, url: "https://github.com/owner/repo/pull/201"}}},
		}
		childByNumber := map[int]childFixture{101: children[0]}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/graphql":
				var body map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&body)
				query, _ := body["query"].(string)

				if strings.Contains(query, "subIssuesSummary") {
					// SearchOpenPilotIssuesWithSubIssues: parent #100 is the sole candidate.
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"repository": map[string]interface{}{
								"issues": map[string]interface{}{
									"nodes": []map[string]interface{}{
										{"number": 100, "subIssuesSummary": map[string]int{"total": 1, "completed": 1}},
									},
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)
					return
				}

				// allSubIssueStates: parent #100 has one closed child, #101.
				nodes := make([]map[string]interface{}, 0, len(children))
				for _, c := range children {
					state := "OPEN"
					if c.closed {
						state = "CLOSED"
					}
					nodes = append(nodes, map[string]interface{}{"number": c.number, "state": state})
				}
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"node": map[string]interface{}{
							"subIssues": map[string]interface{}{"totalCount": len(nodes), "nodes": nodes},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/100":
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"number":100,"node_id":"node_100","state":"open"}`)

			case r.Method == http.MethodGet && r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "is:open"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))

			case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
				var childNum int
				_, _ = fmt.Sscanf(r.URL.Query().Get("q"), "repo:owner/repo is:pr #%d", &childNum)
				cand := childByNumber[childNum]
				items := make([]map[string]interface{}, 0, len(cand.prs))
				for _, pr := range cand.prs {
					items = append(items, map[string]interface{}{
						"id": pr.number, "number": pr.number, "title": "fix: work", "state": "closed",
						"html_url":     pr.url,
						"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
					})
				}
				resp := map[string]interface{}{"total_count": len(items), "items": items}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("[]"))

			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
				w.WriteHeader(http.StatusOK)

			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":1}`))

			case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/100":
				closeCalled = true
				w.WriteHeader(http.StatusOK)

			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

		c.pollCloseEpicParents(context.Background())

		if !closeCalled {
			t.Error("expected candidate parent #100 to be closed")
		}
	})

	t.Run("search failure is a logged no-op", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		logger, buf := newCapturingLogger()
		c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
		c.log = logger

		c.pollCloseEpicParents(context.Background())

		if !strings.Contains(buf.String(), "search for epic-parent candidates failed") {
			t.Errorf("expected search-failure log, got: %s", buf.String())
		}
	})
}
