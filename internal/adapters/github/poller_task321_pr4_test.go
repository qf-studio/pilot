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

// TestPoller_Parallel_FreshCandidate_MergedWorkGuard is the TASK-321 PR-4 regression
// guard. GH-3269 added the fresh-candidate hasMergedWork guard to the sequential path
// (findOldestUnprocessedIssue) but not to the parallel path (checkForNewIssues), where
// hasMergedWork was only consulted inside the `if processed {}` retry block. A fresh,
// never-processed issue whose PR already merged would therefore be dispatched in
// parallel mode (the production default for concurrency>1), producing the phantom
// "no new commit produced" failure TASK-321 set out to eliminate.
//
// This mirrors TestPoller_Sequential_FreshCandidate_MergedWorkGuard for checkForNewIssues.
func TestPoller_Parallel_FreshCandidate_MergedWorkGuard(t *testing.T) {
	tests := []struct {
		name         string
		searchCount  int  // total_count returned by /search/issues
		wantDispatch bool // should checkForNewIssues dispatch the fresh issue?
	}{
		{
			name:         "fresh candidate with merged PR is NOT dispatched",
			searchCount:  1,
			wantDispatch: false,
		},
		{
			name:         "fresh candidate with no merged PR dispatches normally",
			searchCount:  0,
			wantDispatch: true,
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

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/search/issues":
					// hasMergedWork → SearchMergedPRsForIssue
					_, _ = fmt.Fprintf(w, `{"total_count":%d}`, tt.searchCount)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
					// hasMergedWork → FindMergedPRByBranch (no merged branch)
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/55/labels"):
					// hasMergedWork → AddLabels(pilot-done) on the merged path
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodDelete:
					// pilot-failed cleanup inside hasMergedWork
					w.WriteHeader(http.StatusOK)
				default:
					// ListIssues snapshot (and the Phase-3 single-issue refresh, which
					// tolerates a decode miss and proceeds with the snapshot).
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			var dispatched55 atomic.Bool
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithOnIssue(func(ctx context.Context, iss *Issue) error {
					if iss.Number == 55 {
						dispatched55.Store(true)
					}
					return nil
				}),
			)

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			if got := dispatched55.Load(); got != tt.wantDispatch {
				t.Errorf("dispatched #55 = %v, want %v (merged-work guard must skip fresh already-merged candidates in parallel mode)", got, tt.wantDispatch)
			}
		})
	}
}
