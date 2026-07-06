package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

var searchChildRe = regexp.MustCompile(`#(\d+)`)

// TestReconcileEpicParent covers GH-3939's poll-cycle epic-parent auto-close
// check end to end: (a) all children merged closes within one call, (b) one
// child still open is a quiet no-op, (c) a child closed without a merged PR
// vetoes the close with an explicit, logged reason, and (d) an already-closed
// parent is a no-op even when every child looks done.
func TestReconcileEpicParent(t *testing.T) {
	const parentNum = 100

	type child struct {
		num    int
		closed bool
		merged bool // only consulted when closed
	}

	tests := []struct {
		name          string
		parentState   string
		children      []child
		execStatuses  map[string]string
		wantClosed    bool
		wantVetoChild int // 0 if no veto expected
	}{
		{
			name:        "a: all children merged - parent closes within one poll cycle",
			parentState: "open",
			children: []child{
				{num: 1, closed: true, merged: true},
				{num: 2, closed: true, merged: true},
			},
			wantClosed: true,
		},
		{
			name:        "b: one child still open - parent stays open, no veto",
			parentState: "open",
			children: []child{
				{num: 1, closed: true, merged: true},
				{num: 2, closed: false},
			},
			wantClosed: false,
		},
		{
			name:        "c: child closed but PR unmerged - vetoed with explicit reason",
			parentState: "open",
			children: []child{
				{num: 1, closed: true, merged: true},
				{num: 2, closed: true, merged: false},
			},
			wantClosed:    false,
			wantVetoChild: 2,
		},
		{
			name:        "d: parent already closed - no-op",
			parentState: "closed",
			children: []child{
				{num: 1, closed: true, merged: true},
				{num: 2, closed: true, merged: true},
			},
			wantClosed: false,
		},
		{
			name:        "closed child with no_op ledger status counts as shipped",
			parentState: "open",
			children: []child{
				{num: 1, closed: true, merged: true},
				{num: 2, closed: true, merged: false},
			},
			execStatuses: map[string]string{"GH-2": "no_op"},
			wantClosed:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				closeCalled     bool
				addLabelsCalled bool
				commentBody     string
			)
			mergedByChild := map[int]bool{}
			for _, ch := range tt.children {
				mergedByChild[ch.num] = ch.merged
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
					state := tt.parentState
					if state == "" {
						state = "open"
					}
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"state":%q}`, parentNum, state)

				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					nodes := make([]map[string]interface{}, len(tt.children))
					for i, ch := range tt.children {
						state := "OPEN"
						if ch.closed {
							state = "CLOSED"
						}
						nodes[i] = map[string]interface{}{"number": ch.num, "state": state}
					}
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"node": map[string]interface{}{
								"subIssues": map[string]interface{}{
									"totalCount": len(tt.children),
									"nodes":      nodes,
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
					q, _ := url.QueryUnescape(r.URL.Query().Get("q"))
					m := searchChildRe.FindStringSubmatch(q)
					var childNum int
					if len(m) == 2 {
						_, _ = fmt.Sscanf(m[1], "%d", &childNum)
					}
					items := []map[string]interface{}{}
					if mergedByChild[childNum] {
						items = append(items, map[string]interface{}{
							"id":     1,
							"number": 900 + childNum,
							"title":  fmt.Sprintf("fix: child %d", childNum),
							"state":  "closed",
							"pull_request": map[string]interface{}{
								"merged_at": "2026-01-01T00:00:00Z",
							},
						})
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

				case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d/labels", parentNum):
					addLabelsCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, fmt.Sprintf("/repos/owner/repo/issues/%d/labels/", parentNum)):
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d/comments", parentNum):
					b, _ := json.Marshal(map[string]interface{}{"id": 1})
					var payload struct {
						Body string `json:"body"`
					}
					_ = json.NewDecoder(r.Body).Decode(&payload)
					commentBody = payload.Body
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(b)

				case r.Method == http.MethodPatch && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
					closeCalled = true
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			var logBuf bytes.Buffer
			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			c.log = slog.New(slog.NewTextHandler(&logBuf, nil))
			if tt.execStatuses != nil {
				c.SetEvalStore(&mockEvalStore{execStatusByTaskID: tt.execStatuses})
			}

			c.reconcileEpicParent(context.Background(), parentNum)

			if closeCalled != tt.wantClosed {
				t.Errorf("parent closed = %v, want %v", closeCalled, tt.wantClosed)
			}
			if tt.wantClosed != addLabelsCalled {
				t.Errorf("pilot-done label added = %v, want %v", addLabelsCalled, tt.wantClosed)
			}

			logs := logBuf.String()
			if tt.wantVetoChild != 0 {
				if !strings.Contains(logs, "close vetoed") {
					t.Errorf("expected a veto log line, got logs:\n%s", logs)
				}
				if !strings.Contains(logs, fmt.Sprintf("child=%d", tt.wantVetoChild)) {
					t.Errorf("expected veto log to name child %d, got logs:\n%s", tt.wantVetoChild, logs)
				}
				if !strings.Contains(logs, "veto_reason=") {
					t.Errorf("expected veto log to carry an explicit veto_reason, got logs:\n%s", logs)
				}
			} else if strings.Contains(logs, "close vetoed") {
				t.Errorf("did not expect a veto log line (no spam for routine no-op), got logs:\n%s", logs)
			}

			if tt.wantClosed && len(mergedByChild) > 0 {
				hasMerged := false
				for _, m := range mergedByChild {
					if m {
						hasMerged = true
					}
				}
				if hasMerged && !strings.Contains(commentBody, "Merged PRs:") {
					t.Errorf("expected closing comment to name merged PRs, got: %q", commentBody)
				}
			}
		})
	}
}

// TestReconcileEpicParents_Wrapper verifies the search-driven entry point
// dispatches each open, sub-issue-bearing candidate through reconcileEpicParent.
func TestReconcileEpicParents_Wrapper(t *testing.T) {
	const parentNum = 200
	var closeCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			query, _ := body["query"].(string)

			if strings.Contains(query, "subIssuesSummary") {
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"repository": map[string]interface{}{
							"issues": map[string]interface{}{
								"nodes": []map[string]interface{}{
									{
										"number":           parentNum,
										"subIssuesSummary": map[string]int{"total": 1, "completed": 1},
									},
								},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// getAllSubIssueNumbers: one closed, merged child.
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"subIssues": map[string]interface{}{
							"totalCount": 1,
							"nodes": []map[string]interface{}{
								{"number": 1, "state": "CLOSED"},
							},
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"state":"open"}`, parentNum)

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			items := []map[string]interface{}{
				{
					"id": 1, "number": 901, "title": "fix: child",
					"state":        "closed",
					"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))

		case r.Method == http.MethodPatch && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			closeCalled = true
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.reconcileEpicParents(context.Background())

	if !closeCalled {
		t.Error("expected reconcileEpicParents to close the fully-shipped parent via the search-driven sweep")
	}
}
