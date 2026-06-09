package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newSpecTestServer creates an httptest server that routes GitHub API calls for
// issue 42 in owner/repo.  labelsAdded accumulates all labels posted to the
// issue. commentsBody is returned for every GET /comments request.
func newSpecTestServer(t *testing.T, commentsBody string, labelsAdded *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/42/comments"):
			_, _ = w.Write([]byte(commentsBody))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/comments"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"ok"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			*labelsAdded = append(*labelsAdded, body.Labels...)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	return srv
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// TestApplySpecGuard_FirstStrike: no existing marker comment → adds pilot-spec-incomplete.
func TestApplySpecGuard_FirstStrike(t *testing.T) {
	var labelsAdded []string
	srv := newSpecTestServer(t, `[]`, &labelsAdded)
	defer srv.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	issue := &github.Issue{Number: 42}
	reasons := []string{"body too short (10 chars, need 100)"}

	skipped := applySpecGuard(context.Background(), client, "owner", "repo", issue, reasons, nil, "")
	if !skipped {
		t.Fatal("expected applySpecGuard to return true (skip dispatch)")
	}
	if !hasLabel(labelsAdded, github.LabelSpecIncomplete) {
		t.Errorf("expected %s to be added, got %v", github.LabelSpecIncomplete, labelsAdded)
	}
	if hasLabel(labelsAdded, github.LabelBlocked) {
		t.Errorf("expected %s NOT to be added on first strike, got %v", github.LabelBlocked, labelsAdded)
	}
}

// TestApplySpecGuard_SecondStrike: marker comment present → adds pilot-blocked.
func TestApplySpecGuard_SecondStrike(t *testing.T) {
	markerComment := []map[string]interface{}{
		{"id": 1, "body": github.SpecCommentMarker + "\n\nsome content"},
	}
	commentsJSON, _ := json.Marshal(markerComment)

	var labelsAdded []string
	srv := newSpecTestServer(t, string(commentsJSON), &labelsAdded)
	defer srv.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	issue := &github.Issue{Number: 42}
	reasons := []string{"body too short (10 chars, need 100)"}

	skipped := applySpecGuard(context.Background(), client, "owner", "repo", issue, reasons, nil, "")
	if !skipped {
		t.Fatal("expected applySpecGuard to return true (skip dispatch)")
	}
	if !hasLabel(labelsAdded, github.LabelBlocked) {
		t.Errorf("expected %s to be added on second strike, got %v", github.LabelBlocked, labelsAdded)
	}
}

// TestApplySpecGuard_NoExecRow: guard returns true, so the caller must not proceed to
// execution.  We verify that applySpecGuard itself never writes an executions row —
// it is the caller's responsibility to return early, which the handler does.
func TestApplySpecGuard_SkippedWhenCommentListFails(t *testing.T) {
	// Server returns 500 for comment listing → guard should not block dispatch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	issue := &github.Issue{Number: 42}
	reasons := []string{"body too short"}

	skipped := applySpecGuard(context.Background(), client, "owner", "repo", issue, reasons, nil, "")
	if skipped {
		t.Error("expected applySpecGuard to return false (don't block) when comment listing fails")
	}
}
