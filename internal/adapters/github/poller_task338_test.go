package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestPoller_Parallel_UsesBoardSource asserts that checkForNewIssues (the
// parallel/auto dispatch path) sources candidates from the project board when a
// projectBoardSource is configured, instead of falling back to the REST issues
// API. Before TASK-338 only the sequential path consulted the board source, so
// source_enabled + mode:parallel silently reverted to label polling.
func TestPoller_Parallel_UsesBoardSource(t *testing.T) {
	var graphqlHits, restIssuesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			graphqlHits.Add(1)
			// Return an empty board column — enough to prove the board source was
			// queried without dragging in downstream dispatch/API calls.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"node":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues"):
			restIssuesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 1,
		StatusField:   "Status",
		SourceStatus:  "Todo",
		SourceEnabled: true,
	}, "owner", "repo")
	// Pre-seed the project node ID so ensureProjectID skips the resolver query.
	src.projectID = "PVT_test"

	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithProjectBoardSource(src))
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if graphqlHits.Load() == 0 {
		t.Error("checkForNewIssues did not query the project board (GraphQL endpoint never hit)")
	}
	if restIssuesHits.Load() != 0 {
		t.Errorf("checkForNewIssues fell back to the REST issues API (%d hits) instead of the board source", restIssuesHits.Load())
	}
}

// TestPoller_fetchCandidates_LabelModeUnchanged asserts that without a
// projectBoardSource the helper still lists issues by label via the REST API,
// preserving the default (non-board) behavior.
func TestPoller_fetchCandidates_LabelModeUnchanged(t *testing.T) {
	var graphqlHits, restIssuesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			graphqlHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{}}`))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues"):
			restIssuesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	issues, err := poller.fetchCandidates(context.Background())
	if err != nil {
		t.Fatalf("fetchCandidates() error = %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
	if restIssuesHits.Load() == 0 {
		t.Error("fetchCandidates did not use the REST issues API in label mode")
	}
	if graphqlHits.Load() != 0 {
		t.Errorf("fetchCandidates hit the board GraphQL endpoint (%d) in label mode", graphqlHits.Load())
	}
}
