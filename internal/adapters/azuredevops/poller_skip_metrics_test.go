package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/testutil"
)

// fakePollerMetrics records calls to PollerMetricsRecorder for test assertions.
type fakePollerMetrics struct {
	mu         sync.Mutex
	skipped    map[string]int
	dispatched int
}

func newFakePollerMetrics() *fakePollerMetrics {
	return &fakePollerMetrics{skipped: make(map[string]int)}
}

func (f *fakePollerMetrics) RecordPollerSkipped(_, reason string) {
	f.mu.Lock()
	f.skipped[reason]++
	f.mu.Unlock()
}

func (f *fakePollerMetrics) RecordPollerDispatched(_ string) {
	f.mu.Lock()
	f.dispatched++
	f.mu.Unlock()
}

func (f *fakePollerMetrics) RecordPollerDeferredScopeOverlap(_ string) {}

// makeAzureServer creates an httptest server returning the given work items via WIQL + batch pattern.
func makeAzureServer(t *testing.T, workItems []*WorkItem) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// WIQL query response
			refs := make([]WIQLWorkItemRef, len(workItems))
			for i, wi := range workItems {
				refs[i] = WIQLWorkItemRef{ID: wi.ID}
			}
			_ = json.NewEncoder(w).Encode(WIQLQueryResult{WorkItems: refs})
		} else {
			// Batch GET response
			_ = json.NewEncoder(w).Encode(struct {
				Count int         `json:"count"`
				Value []*WorkItem `json:"value"`
			}{Count: len(workItems), Value: workItems})
		}
	}))
}

func TestAzurePoller_SkipMetric_StatusTag(t *testing.T) {
	tests := []struct {
		name           string
		tags           string
		wantSkip       int
		wantDispatched int
	}{
		{
			name:     "in_progress tag → status_tag skip",
			tags:     "pilot; " + TagInProgress,
			wantSkip: 1,
		},
		{
			name:     "done tag → status_tag skip",
			tags:     "pilot; " + TagDone,
			wantSkip: 1,
		},
		{
			name:     "failed tag → status_tag skip",
			tags:     "pilot; " + TagFailed,
			wantSkip: 1,
		},
		{
			name:           "no status tag → dispatched",
			tags:           "pilot",
			wantDispatched: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wi := &WorkItem{
				ID: 1,
				Fields: map[string]interface{}{
					"System.Title":       "test item",
					"System.CreatedDate": time.Now().Format(time.RFC3339),
					"System.Tags":        tt.tags,
				},
			}

			server := makeAzureServer(t, []*WorkItem{wi})
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
			m := newFakePollerMetrics()

			poller := NewPoller(client, "pilot", 30*time.Second,
				WithOnWorkItem(func(ctx context.Context, wi *WorkItem) error { return nil }),
				WithPollerMetrics(m),
				WithPollerRepoKey("org/project"),
			)

			poller.checkForNewWorkItems(context.Background())
			poller.WaitForActive()

			m.mu.Lock()
			defer m.mu.Unlock()

			if tt.wantSkip > 0 {
				got := m.skipped[skipreason.ReasonStatusTag]
				if got != tt.wantSkip {
					t.Errorf("skipped[status_tag] = %d, want %d", got, tt.wantSkip)
				}
			}
			if tt.wantDispatched > 0 && m.dispatched != tt.wantDispatched {
				t.Errorf("dispatched = %d, want %d", m.dispatched, tt.wantDispatched)
			}
		})
	}
}

func TestAzurePoller_NoMetrics_NoPanic(t *testing.T) {
	wi := &WorkItem{
		ID: 1,
		Fields: map[string]interface{}{
			"System.Title":       "test item",
			"System.CreatedDate": time.Now().Format(time.RFC3339),
			"System.Tags":        "pilot; " + TagInProgress,
		},
	}
	server := makeAzureServer(t, []*WorkItem{wi})
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	// No WithPollerMetrics — pollerMetrics is nil.
	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnWorkItem(func(ctx context.Context, wi *WorkItem) error { return nil }),
	)

	poller.checkForNewWorkItems(context.Background())
	poller.WaitForActive()
}
