package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// gh3270ProcessedStore is a minimal ProcessedStore for GH-3270 tests that
// tracks Mark/Unmark call counts so we can assert durable-marker retention.
type gh3270ProcessedStore struct {
	mu          sync.Mutex
	marked      map[string]bool
	unmarkCalls int
}

func newGH3270Store() *gh3270ProcessedStore {
	return &gh3270ProcessedStore{marked: make(map[string]bool)}
}

func (s *gh3270ProcessedStore) Mark(source, repo, issueID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked[issueID] = true
	return nil
}

func (s *gh3270ProcessedStore) Unmark(source, repo, issueID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.marked, issueID)
	s.unmarkCalls++
	return nil
}

func (s *gh3270ProcessedStore) IsProcessed(source, repo, issueID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marked[issueID], nil
}

func (s *gh3270ProcessedStore) Load(source, repo string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]time.Time)
	for k := range s.marked {
		result[k] = time.Now()
	}
	return result, nil
}

// TestPoller_UnmarkProcessed_PermanentFailureRetainsMarker covers GH-3270:
// when onIssueWithResult returns a permanent failure (Success=false, PRNumber=0,
// Error contains a permanent-failure pattern), the durable adapter_processed row
// must NOT be deleted so a daemon restart cannot re-dispatch the issue.
// A retriable failure (same shape but non-permanent error) MUST still unmark.
func TestPoller_UnmarkProcessed_PermanentFailureRetainsMarker(t *testing.T) {
	tests := []struct {
		name         string
		resultErr    error  // error returned by onIssueWithResult
		wantMarked   bool   // should the durable row still be present after dispatch?
		wantUnmarks  int    // expected Unmark() call count
	}{
		{
			name:        "permanent failure (no new commit produced) retains marker",
			resultErr:   errors.New("no new commit produced — worktree HEAD matches base branch parent"),
			wantMarked:  true,
			wantUnmarks: 0,
		},
		{
			name:        "retriable failure unmarks for retry",
			resultErr:   errors.New("tests failed unexpectedly"),
			wantMarked:  false,
			wantUnmarks: 1,
		},
		{
			name:        "nil error (no-commit, no error string) unmarks",
			resultErr:   nil,
			wantMarked:  false,
			wantUnmarks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Number:    42,
				Title:     "GH-42 fix something",
				State:     "open",
				Labels:    []Label{{Name: "pilot"}},
				CreatedAt: time.Now().Add(-1 * time.Hour),
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/search/issues":
					_, _ = fmt.Fprintf(w, `{"total_count":0}`)
				default:
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
				}
			}))
			defer server.Close()

			store := newGH3270Store()
			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			dispatched := make(chan struct{})
			poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
					defer close(dispatched)
					return &IssueResult{
						Success:  false,
						PRNumber: 0,
						Error:    tt.resultErr,
					}, nil
				}),
				WithProcessedStore(store),
				WithMaxConcurrent(1),
			)
			if err != nil {
				t.Fatalf("NewPoller() error = %v", err)
			}

			poller.checkForNewIssues(context.Background())

			// Wait for the dispatched goroutine to finish (callback + unmark).
			select {
			case <-dispatched:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for dispatch goroutine")
			}
			poller.activeWg.Wait()

			store.mu.Lock()
			gotMarked := store.marked["42"]
			gotUnmarks := store.unmarkCalls
			store.mu.Unlock()

			if gotMarked != tt.wantMarked {
				t.Errorf("store.marked[42] = %v, want %v", gotMarked, tt.wantMarked)
			}
			if gotUnmarks != tt.wantUnmarks {
				t.Errorf("Unmark() calls = %d, want %d", gotUnmarks, tt.wantUnmarks)
			}
		})
	}
}
