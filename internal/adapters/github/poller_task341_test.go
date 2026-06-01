package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// task341Server builds a mock GitHub API for the open-PR-awaiting-merge guard.
// hasMergedWork (Search + merged-branch lookup) always reports "no merged work",
// so dispatch turns purely on whether an OPEN pilot PR exists on the branch.
//
// Both FindMergedPRByBranch (state=closed) and FindOpenPRByBranch (state=open)
// hit /repos/.../pulls — they are differentiated by the state query param.
// labeled flips true if any pilot-done/label POST occurs, asserting the guard is
// read-only (unlike hasMergedWork, it must NOT label the issue done).
func task341Server(t *testing.T, issue *Issue, hasOpenPR bool, labeled *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/issues":
			// hasMergedWork → SearchMergedPRsForIssue: no merged work.
			_, _ = w.Write([]byte(`{"total_count":0}`))
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			switch r.URL.Query().Get("state") {
			case "open":
				// FindOpenPRByBranch (hasOpenPRAwaitingMerge).
				if hasOpenPR {
					_, _ = w.Write([]byte(`[{"number":99,"state":"open","head":{"ref":"pilot/GH-55"}}]`))
				} else {
					_, _ = w.Write([]byte(`[]`))
				}
			default:
				// FindMergedPRByBranch (state=closed): no merged PR.
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			labeled.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			// ListIssues snapshot (and the parallel Phase-3 single-issue refresh).
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		}
	}))
}

// TestPoller_Sequential_OpenPRAwaitingMergeGuard is the TASK-341 sequential-mode
// regression: a RE-dispatch candidate (already processed, grace elapsed) whose
// pilot PR is open and awaiting merge must be skipped (pilot-done/close are
// deferred to merge time per GH-3139/TASK-301). The guard lives in the
// processed-retry path because a never-dispatched issue has no PR yet. It
// re-marks the issue (so the grace window throttles re-checks) but never labels
// it — the merge flow owns it.
func TestPoller_Sequential_OpenPRAwaitingMergeGuard(t *testing.T) {
	tests := []struct {
		name      string
		hasOpenPR bool
		wantNil   bool // findOldestUnprocessedIssue returns nil (skipped)?
	}{
		{name: "open PR awaiting merge is skipped", hasOpenPR: true, wantNil: true},
		{name: "no open PR re-dispatches normally", hasOpenPR: false, wantNil: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Number:    55,
				Title:     "GH-55 feature",
				State:     "open",
				Labels:    []Label{{Name: "pilot"}},
				CreatedAt: time.Now().Add(-1 * time.Hour),
			}
			var labeled atomic.Bool
			server := task341Server(t, issue, tt.hasOpenPR, &labeled)
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			// grace=0 so the processed issue proceeds straight to the retry path.
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithRetryGracePeriod(0))
			poller.markProcessed(55) // simulate a prior dispatch (PR already created)

			got, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			if tt.wantNil && got != nil {
				t.Errorf("got issue #%d, want nil (open PR should be skipped)", got.Number)
			}
			if !tt.wantNil {
				if got == nil {
					t.Fatal("got nil, want issue #55 (no open PR should re-dispatch)")
				}
				if got.Number != 55 {
					t.Errorf("got issue #%d, want #55", got.Number)
				}
			}

			if tt.hasOpenPR {
				// Re-marked (grace throttle) but never labeled.
				if !poller.IsProcessed(55) {
					t.Error("issue should be re-marked processed when skipped for an open PR (grace throttle)")
				}
				if labeled.Load() {
					t.Error("issue must NOT be labeled when skipped for an open PR (merge flow owns it)")
				}
			}
		})
	}
}

// TestPoller_Parallel_OpenPRAwaitingMergeGuard mirrors the sequential test for
// checkForNewIssues (the production default for concurrency>1).
func TestPoller_Parallel_OpenPRAwaitingMergeGuard(t *testing.T) {
	tests := []struct {
		name         string
		hasOpenPR    bool
		wantDispatch bool
	}{
		{name: "open PR awaiting merge is NOT dispatched", hasOpenPR: true, wantDispatch: false},
		{name: "no open PR re-dispatches normally", hasOpenPR: false, wantDispatch: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Number:    55,
				Title:     "GH-55 feature",
				State:     "open",
				Labels:    []Label{{Name: "pilot"}},
				CreatedAt: time.Now().Add(-1 * time.Hour),
			}
			var labeled atomic.Bool
			server := task341Server(t, issue, tt.hasOpenPR, &labeled)
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			var dispatched55 atomic.Bool
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithRetryGracePeriod(0),
				WithOnIssue(func(ctx context.Context, iss *Issue) error {
					if iss.Number == 55 {
						dispatched55.Store(true)
					}
					return nil
				}),
			)
			poller.markProcessed(55) // simulate a prior dispatch (PR already created)

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			if got := dispatched55.Load(); got != tt.wantDispatch {
				t.Errorf("dispatched #55 = %v, want %v (open-PR guard must skip awaiting-merge candidates in parallel mode)", got, tt.wantDispatch)
			}
			if tt.hasOpenPR && !poller.IsProcessed(55) {
				t.Error("issue should be re-marked processed when skipped for an open PR (grace throttle)")
			}
		})
	}
}

// TestFindOpenPRByBranch verifies the strongly-consistent open-PR lookup queries
// state=open and reports presence by array length.
func TestFindOpenPRByBranch(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{name: "open PR present", response: `[{"number":7,"state":"open","head":{"ref":"pilot/GH-7"}}]`, want: true},
		{name: "no open PR", response: `[]`, want: false},
		{name: "PR on a different branch is ignored", response: `[{"number":7,"state":"open","head":{"ref":"other/branch"}}]`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sawOpenState atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("state") == "open" {
					sawOpenState.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			got, err := client.FindOpenPRByBranch(context.Background(), "owner", "repo", "pilot/GH-7")
			if err != nil {
				t.Fatalf("FindOpenPRByBranch() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("FindOpenPRByBranch() = %v, want %v", got, tt.want)
			}
			if !sawOpenState.Load() {
				t.Error("FindOpenPRByBranch must query state=open")
			}
		})
	}
}
