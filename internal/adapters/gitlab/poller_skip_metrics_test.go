package gitlab

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

func TestGitLabPoller_SkipMetric_StatusLabel(t *testing.T) {
	tests := []struct {
		name           string
		labels         []string
		wantSkip       int
		wantDispatched int
	}{
		{
			name:     "in_progress label → status_label skip",
			labels:   []string{LabelInProgress},
			wantSkip: 1,
		},
		{
			name:     "done label → status_label skip",
			labels:   []string{LabelDone},
			wantSkip: 1,
		},
		{
			name:     "failed label → status_label skip",
			labels:   []string{LabelFailed},
			wantSkip: 1,
		},
		{
			name:           "no status label → dispatched",
			labels:         []string{},
			wantDispatched: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				IID:       1,
				Title:     "test issue",
				Labels:    tt.labels,
				CreatedAt: time.Now(),
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]*Issue{issue})
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, "namespace/project", server.URL)
			m := newFakePollerMetrics()

			poller := NewPoller(client, "pilot", 30*time.Second,
				WithOnIssue(func(ctx context.Context, issue *Issue) error { return nil }),
				WithPollerMetrics(m),
				WithPollerRepoKey("namespace/project"),
			)

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			m.mu.Lock()
			defer m.mu.Unlock()

			if tt.wantSkip > 0 {
				got := m.skipped[skipreason.ReasonStatusLabel]
				if got != tt.wantSkip {
					t.Errorf("skipped[status_label] = %d, want %d", got, tt.wantSkip)
				}
			}
			if tt.wantDispatched > 0 && m.dispatched != tt.wantDispatched {
				t.Errorf("dispatched = %d, want %d", m.dispatched, tt.wantDispatched)
			}
		})
	}
}

func TestGitLabPoller_NoMetrics_NoPanic(t *testing.T) {
	issue := &Issue{IID: 1, Title: "t", Labels: []string{LabelInProgress}, CreatedAt: time.Now()}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{issue})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "namespace/project", server.URL)
	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error { return nil }),
		// no WithPollerMetrics
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()
}
