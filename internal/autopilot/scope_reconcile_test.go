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
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// labelMember describes one issue carrying a scope label for the fake GraphQL
// server below.
type labelMember struct {
	num      int
	closed   bool
	merged   bool // only consulted when closed
	closedAt string
	updated  string
}

// labelScopeServer builds a fake GitHub server serving both label-scope
// GraphQL queries (candidate discovery and per-label membership, distinguished
// by the "OPEN, CLOSED" states literal only the membership query carries) plus
// /search/issues for verifyChildrenShippedForClose's PR lookups.
func labelScopeServer(t *testing.T, labelName string, members []labelMember) *httptest.Server {
	t.Helper()
	mergedByChild := map[int]bool{}
	for _, m := range members {
		mergedByChild[m.num] = m.merged
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			query, _ := body["query"].(string)

			if strings.Contains(query, "OPEN, CLOSED") {
				nodes := make([]map[string]interface{}, len(members))
				for i, m := range members {
					state := "OPEN"
					if m.closed {
						state = "CLOSED"
					}
					var closedAt interface{}
					if m.closedAt != "" {
						closedAt = m.closedAt
					}
					nodes[i] = map[string]interface{}{
						"number":    m.num,
						"state":     state,
						"closedAt":  closedAt,
						"updatedAt": m.updated,
					}
				}
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"repository": map[string]interface{}{
							"labels": map[string]interface{}{
								"nodes": []map[string]interface{}{{
									"name": labelName,
									"issues": map[string]interface{}{
										"totalCount": len(members),
										"nodes":      nodes,
									},
								}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// Candidate discovery query.
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"repository": map[string]interface{}{
						"labels": map[string]interface{}{
							"nodes": []map[string]interface{}{{"name": labelName}},
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

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestReconcileLabelScope covers GH-3991's per-label completion gate:
// all-closed-and-shipped enqueues, one open member is a quiet no-op, a closed
// member without a merged PR vetoes, and a scope that finished before the
// lookback window never enqueues.
func TestReconcileLabelScope(t *testing.T) {
	const labelName = "scope:checkout"
	now := time.Now().UTC()
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	old := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name          string
		members       []labelMember
		lookback      time.Duration
		wantEnqueued  bool
		wantVetoChild int
	}{
		{
			name: "all closed and shipped - enqueued",
			members: []labelMember{
				{num: 1, closed: true, merged: true, closedAt: recent, updated: recent},
				{num: 2, closed: true, merged: true, closedAt: recent, updated: recent},
			},
			wantEnqueued: true,
		},
		{
			name: "one open member - skip, no veto",
			members: []labelMember{
				{num: 1, closed: true, merged: true, closedAt: recent, updated: recent},
				{num: 2, closed: false, updated: recent},
			},
			wantEnqueued: false,
		},
		{
			name: "closed member without merged PR - vetoed",
			members: []labelMember{
				{num: 1, closed: true, merged: true, closedAt: recent, updated: recent},
				{num: 2, closed: true, merged: false, closedAt: recent, updated: recent},
			},
			wantEnqueued:  false,
			wantVetoChild: 2,
		},
		{
			name: "completed before lookback window - never enqueues",
			members: []labelMember{
				{num: 1, closed: true, merged: true, closedAt: old, updated: old},
			},
			lookback:     time.Hour,
			wantEnqueued: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := labelScopeServer(t, labelName, tt.members)
			defer server.Close()

			var logBuf bytes.Buffer
			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", ScopeLookback: tt.lookback}
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			c.log = slog.New(slog.NewTextHandler(&logBuf, nil))
			stateStore := newTestStateStore(t)
			c.SetStateStore(stateStore)

			c.reconcileLabelScope(context.Background(), "scope:", labelName)

			row, err := stateStore.GetScopeRelease("owner/repo", "label:checkout")
			if err != nil {
				t.Fatalf("GetScopeRelease failed: %v", err)
			}
			gotEnqueued := row != nil
			if gotEnqueued != tt.wantEnqueued {
				t.Errorf("enqueued = %v, want %v (row=%+v)", gotEnqueued, tt.wantEnqueued, row)
			}

			logs := logBuf.String()
			if tt.wantVetoChild != 0 {
				if !strings.Contains(logs, "scope vetoed") {
					t.Errorf("expected a veto log line, got logs:\n%s", logs)
				}
				if !strings.Contains(logs, fmt.Sprintf("child=%d", tt.wantVetoChild)) {
					t.Errorf("expected veto log to name child %d, got logs:\n%s", tt.wantVetoChild, logs)
				}
			} else if strings.Contains(logs, "scope vetoed") {
				t.Errorf("did not expect a veto log line, got logs:\n%s", logs)
			}
		})
	}
}

// TestReconcileLabelScopes_Wrapper verifies the search-driven entry point
// discovers a scope:<name> label candidate and enqueues its release once
// every member has closed and shipped.
func TestReconcileLabelScopes_Wrapper(t *testing.T) {
	const labelName = "scope:billing"
	now := time.Now().UTC().Format(time.RFC3339)

	server := labelScopeServer(t, labelName, []labelMember{
		{num: 10, closed: true, merged: true, closedAt: now, updated: now},
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	stateStore := newTestStateStore(t)
	c.SetStateStore(stateStore)

	c.reconcileLabelScopes(context.Background())

	row, err := stateStore.GetScopeRelease("owner/repo", "label:billing")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row == nil {
		t.Fatal("expected reconcileLabelScopes to enqueue a scope release for the completed label")
	}
	if len(row.MemberPRs) != 1 || row.MemberPRs[0] != 910 {
		t.Errorf("MemberPRs = %v, want [910]", row.MemberPRs)
	}
	if row.ScopeTitle != labelName {
		t.Errorf("ScopeTitle = %q, want %q", row.ScopeTitle, labelName)
	}
}

// TestReconcileLabelScopes_DoneRowSkipsMembershipFetch verifies a scope
// already in a terminal state (done/failed) makes zero further API calls
// beyond the candidate-discovery label query — idempotent re-ticks never
// re-fetch membership for work that's already finished (GH-3991).
func TestReconcileLabelScopes_DoneRowSkipsMembershipFetch(t *testing.T) {
	const labelName = "scope:shipped"
	var membershipCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			query, _ := body["query"].(string)

			if strings.Contains(query, "OPEN, CLOSED") {
				membershipCalls++
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{}}`))
				return
			}

			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"repository": map[string]interface{}{
						"labels": map[string]interface{}{
							"nodes": []map[string]interface{}{{"name": labelName}},
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	stateStore := newTestStateStore(t)
	c.SetStateStore(stateStore)

	if err := stateStore.EnqueueScopeRelease("owner/repo", "label:shipped", labelName, []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if err := stateStore.MarkScopeReleaseDone("owner/repo", "label:shipped", "v1.2.3", "deadbeef"); err != nil {
		t.Fatalf("MarkScopeReleaseDone failed: %v", err)
	}

	c.reconcileLabelScopes(context.Background())

	if membershipCalls != 0 {
		t.Errorf("membership GraphQL calls = %d, want 0 (done row must short-circuit before any further API call)", membershipCalls)
	}
}

// TestReconcileLabelScope_StaleAlert verifies that a label scope with at least
// one shipped member and at least one open member untouched past
// ScopeStaleAfter fires a scope_stale alert exactly once across repeated
// reconcile calls (deduped), and never fires when nothing has shipped yet.
func TestReconcileLabelScope_StaleAlert(t *testing.T) {
	const labelName = "scope:abandoned"
	now := time.Now().UTC()
	recent := now.Add(-time.Minute).Format(time.RFC3339)
	stale := now.Add(-200 * time.Hour).Format(time.RFC3339)

	t.Run("shipped member + stale open member alerts once", func(t *testing.T) {
		server := labelScopeServer(t, labelName, []labelMember{
			{num: 1, closed: true, merged: true, closedAt: recent, updated: recent},
			{num: 2, closed: false, updated: stale},
		})
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", ScopeStaleAfter: time.Hour}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		c.SetStateStore(newTestStateStore(t))

		sink := &fakeAlertSink{}
		c.SetAlertsEngine(sink)

		c.reconcileLabelScope(context.Background(), "scope:", labelName)
		c.reconcileLabelScope(context.Background(), "scope:", labelName)

		var staleEvents int
		for _, e := range sink.events {
			if string(e.Type) == "scope_stale" {
				staleEvents++
			}
		}
		if staleEvents != 1 {
			t.Errorf("scope_stale alerts = %d, want 1 (deduped across repeated ticks)", staleEvents)
		}
	})

	t.Run("no shipped member yet - no alert", func(t *testing.T) {
		server := labelScopeServer(t, labelName, []labelMember{
			{num: 1, closed: false, updated: stale},
		})
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", ScopeStaleAfter: time.Hour}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		c.SetStateStore(newTestStateStore(t))

		sink := &fakeAlertSink{}
		c.SetAlertsEngine(sink)

		c.reconcileLabelScope(context.Background(), "scope:", labelName)

		if len(sink.events) != 0 {
			t.Errorf("expected no alerts when nothing has shipped, got: %+v", sink.events)
		}
	})
}
