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
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
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

// TestReconcileEpicParent_EnqueuesScopeRelease verifies that once
// closeParentNow actually closes a fully-shipped epic and Trigger
// "on_scope_close" is active, reconcileEpicParent enqueues a durable scope
// release row naming the merged child PRs (GH-3990).
func TestReconcileEpicParent_EnqueuesScopeRelease(t *testing.T) {
	const parentNum = 300

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"title":"Checkout epic","state":"open"}`, parentNum)

		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"subIssues": map[string]interface{}{
							"totalCount": 1,
							"nodes":      []map[string]interface{}{{"number": 1, "state": "CLOSED"}},
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			items := []map[string]interface{}{{
				"id": 1, "number": 901, "title": "fix: child",
				"state":        "closed",
				"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
			}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

		case r.Method == http.MethodPatch && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	stateStore := newTestStateStore(t)
	c.SetStateStore(stateStore)

	c.reconcileEpicParent(context.Background(), parentNum)

	row, err := stateStore.GetScopeRelease("owner/repo", fmt.Sprintf("epic:%d", parentNum))
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row == nil {
		t.Fatal("expected a scope release row enqueued after the epic closed")
	}
	if row.ScopeTitle != "Checkout epic" {
		t.Errorf("ScopeTitle = %q, want %q", row.ScopeTitle, "Checkout epic")
	}
	if len(row.MemberPRs) != 1 || row.MemberPRs[0] != 901 {
		t.Errorf("MemberPRs = %v, want [901]", row.MemberPRs)
	}
}

// TestReconcileClosedEpicScopes covers the lookback sweep (GH-3990): a closed,
// pilot+pilot-done epic with sub-issues and no scope row gets one enqueued;
// a second sweep after the row exists makes zero further release API calls
// (idempotence).
func TestReconcileClosedEpicScopes(t *testing.T) {
	const parentNum = 400

	var graphqlCalls, searchCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			query, _ := body["query"].(string)

			if strings.Contains(query, "repository(owner:") {
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"repository": map[string]interface{}{
							"issues": map[string]interface{}{
								"nodes": []map[string]interface{}{{
									"number":           parentNum,
									"updatedAt":        time.Now().UTC().Format(time.RFC3339),
									"subIssuesSummary": map[string]int{"total": 1},
								}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			atomic.AddInt64(&graphqlCalls, 1)
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"subIssues": map[string]interface{}{
							"totalCount": 1,
							"nodes":      []map[string]interface{}{{"number": 1, "state": "CLOSED"}},
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			atomic.AddInt64(&searchCalls, 1)
			items := []map[string]interface{}{{
				"id": 1, "number": 902, "title": "fix: child",
				"state":        "closed",
				"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
			}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"I_parent400","number":%d,"title":"Closed while daemon was down","state":"closed"}`, parentNum)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLookback: 24 * time.Hour}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	stateStore := newTestStateStore(t)
	c.SetStateStore(stateStore)

	c.reconcileClosedEpicScopes(context.Background())

	row, err := stateStore.GetScopeRelease("owner/repo", fmt.Sprintf("epic:%d", parentNum))
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row == nil {
		t.Fatal("expected reconcileClosedEpicScopes to enqueue a scope release for the closed epic")
	}
	if len(row.MemberPRs) != 1 || row.MemberPRs[0] != 902 {
		t.Errorf("MemberPRs = %v, want [902]", row.MemberPRs)
	}

	firstGraphQL := atomic.LoadInt64(&graphqlCalls)
	firstSearch := atomic.LoadInt64(&searchCalls)
	if firstGraphQL == 0 || firstSearch == 0 {
		t.Fatalf("expected verification API calls on first sweep, got graphql=%d search=%d", firstGraphQL, firstSearch)
	}

	// Second sweep: the scope row already exists — GetScopeRelease must
	// short-circuit before any verification API calls.
	c.reconcileClosedEpicScopes(context.Background())

	if got := atomic.LoadInt64(&graphqlCalls); got != firstGraphQL {
		t.Errorf("getAllSubIssueNumbers graphql calls after second sweep = %d, want unchanged at %d (idempotent)", got, firstGraphQL)
	}
	if got := atomic.LoadInt64(&searchCalls); got != firstSearch {
		t.Errorf("verifyChildrenShippedForClose search calls after second sweep = %d, want unchanged at %d (idempotent)", got, firstSearch)
	}
}

// TestEpicCloseVetoBreaker covers GH-4006's loop breaker: a parent whose
// close-veto never changes (a ghost-closed child that can never produce its
// own merged PR, e.g. #3927/#3952) must stop being silently re-vetoed forever
// — after epicCloseVetoBreakerThreshold consecutive reconcile passes it adds
// LabelNeedsClarification (which already excludes the issue from dispatch),
// posts exactly ONE explanatory comment, and fires exactly ONE
// epic_close_vetoed alert, even as further passes keep observing the same
// veto. A veto that resolves before the threshold (the child's PR merges
// late) must close the parent normally with no escalation at all.
func TestEpicCloseVetoBreaker(t *testing.T) {
	const parentNum = 500
	const childNum = 1

	t.Run("permanent veto escalates once and never repeats", func(t *testing.T) {
		var (
			addLabelsCalls []string
			commentBodies  []string
			closeCalled    bool
		)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"state":"open"}`, parentNum)

			case r.Method == http.MethodPost && r.URL.Path == "/graphql":
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"node": map[string]interface{}{
							"subIssues": map[string]interface{}{
								"totalCount": 1,
								"nodes":      []map[string]interface{}{{"number": childNum, "state": "CLOSED"}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
				// Ghost-closed child: closed, but never produces a merged PR
				// under its own issue number (its work shipped via a different
				// issue's branch/PR — the class of bug this task does not fix).
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": []map[string]interface{}{}})

			case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d/labels", parentNum):
				var payload struct {
					Labels []string `json:"labels"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				addLabelsCalls = append(addLabelsCalls, payload.Labels...)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("[]"))

			case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d/comments", parentNum):
				var payload struct {
					Body string `json:"body"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				commentBodies = append(commentBodies, payload.Body)
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
		sink := &fakeAlertSink{}
		c.SetAlertsEngine(sink)

		for i := 0; i < epicCloseVetoBreakerThreshold; i++ {
			c.reconcileEpicParent(context.Background(), parentNum)
		}

		if closeCalled {
			t.Error("parent should never close while permanently vetoed")
		}
		if len(commentBodies) != 1 {
			t.Fatalf("expected exactly one escalation comment after %d passes, got %d: %v",
				epicCloseVetoBreakerThreshold, len(commentBodies), commentBodies)
		}
		if !strings.Contains(commentBodies[0], fmt.Sprintf("child #%d", childNum)) {
			t.Errorf("comment should name the blocking child, got: %q", commentBodies[0])
		}
		if !strings.Contains(commentBodies[0], "no merged PR") {
			t.Errorf("comment should explain why the child fails the shipped-check, got: %q", commentBodies[0])
		}
		foundLabel := false
		for _, l := range addLabelsCalls {
			if l == github.LabelNeedsClarification {
				foundLabel = true
			}
		}
		if !foundLabel {
			t.Errorf("expected %s label to be added, got labels: %v", github.LabelNeedsClarification, addLabelsCalls)
		}
		if len(sink.events) != 1 {
			t.Fatalf("expected exactly one alert fired, got %d", len(sink.events))
		}
		if sink.events[0].Type != alerts.EventType("epic_close_vetoed") {
			t.Errorf("alert type = %q, want epic_close_vetoed", sink.events[0].Type)
		}

		// Further passes past the threshold must not repeat the comment or alert.
		c.reconcileEpicParent(context.Background(), parentNum)
		if len(commentBodies) != 1 {
			t.Errorf("expected no additional comment beyond the escalation, got %d: %v", len(commentBodies), commentBodies)
		}
		if len(sink.events) != 1 {
			t.Errorf("expected no additional alert beyond the escalation, got %d", len(sink.events))
		}
	})

	t.Run("veto resolves before threshold — counter resets and parent closes normally", func(t *testing.T) {
		pass := 0
		var closeCalled bool
		var commentBodies []string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"state":"open"}`, parentNum)

			case r.Method == http.MethodPost && r.URL.Path == "/graphql":
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"node": map[string]interface{}{
							"subIssues": map[string]interface{}{
								"totalCount": 1,
								"nodes":      []map[string]interface{}{{"number": childNum, "state": "CLOSED"}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
				items := []map[string]interface{}{}
				if pass >= 2 {
					// Third pass: the child's PR finally merges — the veto resolves
					// before it ever reaches epicCloseVetoBreakerThreshold.
					items = append(items, map[string]interface{}{
						"id": 1, "number": 900 + childNum, "title": "fix: child",
						"state":        "closed",
						"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
					})
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("[]"))

			case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d/comments", parentNum):
				var payload struct {
					Body string `json:"body"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				commentBodies = append(commentBodies, payload.Body)
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
		sink := &fakeAlertSink{}
		c.SetAlertsEngine(sink)

		// Two vetoed passes (below threshold), then the child's PR merges.
		for ; pass < 2; pass++ {
			c.reconcileEpicParent(context.Background(), parentNum)
		}
		if closeCalled {
			t.Fatal("parent should not close while still vetoed")
		}

		c.reconcileEpicParent(context.Background(), parentNum)

		if !closeCalled {
			t.Error("expected parent to close once the child's PR merges")
		}
		for _, body := range commentBodies {
			if strings.Contains(body, "permanently blocked") {
				t.Errorf("did not expect an escalation comment once the veto resolved, got: %q", body)
			}
		}
		if len(sink.events) != 0 {
			t.Errorf("expected no epic_close_vetoed alert once the veto resolved, got %d", len(sink.events))
		}
	})
}
