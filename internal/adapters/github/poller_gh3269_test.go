package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestPoller_Sequential_FreshCandidate_MergedWorkGuard covers GH-3269 fix 1:
// hasMergedWork must be consulted for fresh (never-processed) candidates in
// sequential mode, not only inside the retry/restart branches.
func TestPoller_Sequential_FreshCandidate_MergedWorkGuard(t *testing.T) {
	tests := []struct {
		name        string
		searchCount int  // total_count returned by /search/issues
		wantNil     bool // should findOldestUnprocessedIssue return nil?
		wantLabeled bool // should pilot-done label be added?
		wantMarked  bool // should issue be marked processed?
	}{
		{
			name:        "fresh candidate with merged PR is skipped",
			searchCount: 1,
			wantNil:     true,
			wantLabeled: true,
			wantMarked:  true,
		},
		{
			name:        "fresh candidate with no merged PR dispatches normally",
			searchCount: 0,
			wantNil:     false,
			wantLabeled: false,
			wantMarked:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Number:    55,
				Title:     "GH-55 fresh feature",
				State:     "open",
				Labels:    []Label{{Name: "pilot"}},
				CreatedAt: time.Now().Add(-1 * time.Hour),
			}

			var labeled atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/search/issues":
					_, _ = fmt.Fprintf(w, `{"total_count":%d}`, tt.searchCount)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/55/labels"):
					labeled.Store(true)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodDelete:
					// pilot-failed cleanup inside hasMergedWork
					w.WriteHeader(http.StatusOK)
				default:
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

			got, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("got issue #%d, want nil (should be skipped due to merged PR)", got.Number)
				}
			} else {
				if got == nil {
					t.Fatal("got nil, want issue (should dispatch fresh candidate with no merged PR)")
				}
				if got.Number != 55 {
					t.Errorf("got issue #%d, want #55", got.Number)
				}
			}

			if labeled.Load() != tt.wantLabeled {
				t.Errorf("pilot-done label added = %v, want %v", labeled.Load(), tt.wantLabeled)
			}

			if tt.wantMarked && !poller.IsProcessed(55) {
				t.Error("issue should be marked processed when merged work found")
			}
			if !tt.wantMarked && poller.IsProcessed(55) {
				t.Error("issue should not be marked processed when no merged work")
			}
		})
	}
}

// TestPoller_Sequential_FreshCandidate_CompletedExecutionGuard covers GH-3269 fix 2:
// HasCompletedExecution must be checked in sequential mode (findOldestUnprocessedIssue),
// mirroring the guard that already exists in parallel mode (checkForNewIssues).
func TestPoller_Sequential_FreshCandidate_CompletedExecutionGuard(t *testing.T) {
	tests := []struct {
		name       string
		completed  bool // does execChecker report completed execution?
		wantNil    bool // should findOldestUnprocessedIssue return nil?
		wantMarked bool
	}{
		{
			name:       "completed execution blocks dispatch",
			completed:  true,
			wantNil:    true,
			wantMarked: true,
		},
		{
			name:       "no completed execution allows dispatch",
			completed:  false,
			wantNil:    false,
			wantMarked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Number:    77,
				Title:     "GH-77 fresh issue",
				State:     "open",
				Labels:    []Label{{Name: "pilot"}},
				CreatedAt: time.Now().Add(-1 * time.Hour),
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/search/issues":
					// No merged PRs — let execChecker guard run.
					_, _ = w.Write([]byte(`{"total_count":0}`))
				case "/repos/owner/repo/pulls":
					_, _ = w.Write([]byte(`[]`))
				default:
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			execChecker := &mockExecutionChecker{
				completed: map[string]bool{
					"GH-77:/project": tt.completed,
				},
			}

			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithExecutionChecker(execChecker, "/project"),
			)

			got, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("got issue #%d, want nil (completed execution should block dispatch)", got.Number)
				}
			} else {
				if got == nil {
					t.Fatal("got nil, want issue (no completed execution should allow dispatch)")
				}
				if got.Number != 77 {
					t.Errorf("got issue #%d, want #77", got.Number)
				}
			}

			if tt.wantMarked && !poller.IsProcessed(77) {
				t.Error("issue should be marked processed after completed-execution skip")
			}
			if !tt.wantMarked && poller.IsProcessed(77) {
				t.Error("issue should not be marked processed when dispatched normally")
			}
		})
	}
}
