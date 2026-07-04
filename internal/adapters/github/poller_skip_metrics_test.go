package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/testutil"
)

// fakePollerMetrics records all calls to PollerMetricsRecorder methods for assertions.
type fakePollerMetrics struct {
	mu                  sync.Mutex
	skipped             map[string]int // reason → count
	dispatched          int
	deferredScopeOverlap int
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

func (f *fakePollerMetrics) RecordPollerDeferredScopeOverlap(_ string) {
	f.mu.Lock()
	f.deferredScopeOverlap++
	f.mu.Unlock()
}

func TestPoller_SkipMetric_IncrementsByReason(t *testing.T) {
	// All test issues carry the "pilot" label so that ListIssues's post-fetch label
	// filter passes them through; the skip logic then fires on the secondary labels.
	pilot := Label{Name: "pilot"}
	tests := []struct {
		name           string
		issues         []*Issue
		wantReason     string
		wantSkipCount  int
		wantDispatched int
	}{
		{
			name: "in_progress label",
			issues: []*Issue{
				{Number: 1, Title: "t", Labels: []Label{pilot, {Name: LabelInProgress}}},
			},
			wantReason:    skipreason.ReasonInProgress,
			wantSkipCount: 1,
		},
		{
			name: "blocked label",
			issues: []*Issue{
				{Number: 1, Title: "t", Labels: []Label{pilot, {Name: LabelBlocked}}},
			},
			wantReason:    skipreason.ReasonBlocked,
			wantSkipCount: 1,
		},
		{
			name: "needs_clarification label",
			issues: []*Issue{
				{Number: 1, Title: "t", Labels: []Label{pilot, {Name: LabelNeedsClarification}}},
			},
			wantReason:    skipreason.ReasonNeedsClarification,
			wantSkipCount: 1,
		},
		{
			name: "done label",
			issues: []*Issue{
				{Number: 1, Title: "t", Labels: []Label{pilot, {Name: LabelDone}}},
			},
			wantReason:    skipreason.ReasonDone,
			wantSkipCount: 1,
		},
		{
			name: "dispatch increments counter — pilot label only",
			issues: []*Issue{
				{Number: 1, Title: "t", Labels: []Label{pilot}},
			},
			wantDispatched: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ListIssues returns an array; GetIssue (fresh-label refresh) returns a single object.
			// Distinguish by path depth: /repos/owner/repo/issues vs /repos/owner/repo/issues/N
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// URL ends with a numeric segment → single-issue GET
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				lastSegment := ""
				if len(parts) > 0 {
					lastSegment = parts[len(parts)-1]
				}
				isSingleGet := len(lastSegment) > 0 && lastSegment[0] >= '0' && lastSegment[0] <= '9'
				if isSingleGet && len(tt.issues) > 0 {
					_ = json.NewEncoder(w).Encode(tt.issues[0])
					return
				}
				_ = json.NewEncoder(w).Encode(tt.issues)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			m := newFakePollerMetrics()

			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithOnIssue(func(ctx context.Context, issue *Issue) error { return nil }),
				WithPollerMetrics(m),
			)

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			m.mu.Lock()
			defer m.mu.Unlock()

			if tt.wantReason != "" {
				got := m.skipped[tt.wantReason]
				if got != tt.wantSkipCount {
					t.Errorf("skipped[%q] = %d, want %d", tt.wantReason, got, tt.wantSkipCount)
				}
			}
			if tt.wantDispatched > 0 && m.dispatched != tt.wantDispatched {
				t.Errorf("dispatched = %d, want %d", m.dispatched, tt.wantDispatched)
			}
		})
	}
}

func TestPoller_ScopeOverlapDeferral_IncrementsMetric(t *testing.T) {
	// Two issues both referencing internal/auth — scope-overlap guard defers #2.
	// groupByOverlappingScope uses directory extraction from issue bodies, not titles.
	pilot := Label{Name: "pilot"}
	sharedBody := "Modify internal/auth/handler.go"
	issues := []*Issue{
		{Number: 1, Title: "refactor auth", Body: sharedBody, Labels: []Label{pilot}, CreatedAt: time.Now().Add(-2 * time.Hour)},
		{Number: 2, Title: "auth cleanup", Body: sharedBody, Labels: []Label{pilot}, CreatedAt: time.Now().Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		lastSegment := ""
		if len(parts) > 0 {
			lastSegment = parts[len(parts)-1]
		}
		isSingleGet := len(lastSegment) > 0 && lastSegment[0] >= '0' && lastSegment[0] <= '9'
		if isSingleGet {
			// Return oldest issue for fresh-label check.
			_ = json.NewEncoder(w).Encode(issues[0])
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error { return nil }),
		WithPollerMetrics(m),
		WithExecutionMode(ExecutionModeAuto),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deferredScopeOverlap < 1 {
		t.Errorf("deferredScopeOverlap = %d, want ≥ 1", m.deferredScopeOverlap)
	}
	if m.dispatched != 1 {
		t.Errorf("dispatched = %d, want 1", m.dispatched)
	}
}

// GH-3789 (D6): the parallel/auto-mode Phase 1 gate must skip an issue whose
// "Blocked by" dependency is still open, and that skip must be visible via
// pollerMetrics — not just a log line.
func TestPoller_CheckForNewIssues_SkipsPendingDependency_RecordsMetric(t *testing.T) {
	pilot := Label{Name: "pilot"}
	issue := &Issue{Number: 1, Title: "gated task", Body: "Blocked by: #100", Labels: []Label{pilot}, CreatedAt: time.Now()}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		case "/repos/owner/repo/issues/100":
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: "open"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()
	var dispatches int32

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, i *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
		WithPollerMetrics(m),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (blocker still open)", got)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.skipped[skipreason.ReasonPendingDependency]; got != 1 {
		t.Errorf("skipped[pending_dependency] = %d, want 1", got)
	}
}

// GH-3789 (D6): an issue can clear the Phase 1 dependency gate (blocker
// closed) and then sit in the dispatch loop waiting for a worker slot. If
// the blocker reopens during that wait, dispatch must re-check and refuse
// to run — this reproduces the GH-3759 incident where a gated task executed
// while its blocker was still open.
func TestPoller_CheckForNewIssues_DispatchTimeRecheck_BlockerReopened(t *testing.T) {
	pilot := Label{Name: "pilot"}
	issue := &Issue{Number: 1, Title: "gated task", Body: "Blocked by: #100", Labels: []Label{pilot}, CreatedAt: time.Now()}

	var depCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		case "/repos/owner/repo/issues/1":
			// GH-2341 fresh-label refresh before dispatch — same issue, still gated-clean.
			_ = json.NewEncoder(w).Encode(issue)
		case "/repos/owner/repo/issues/100":
			// First call is the Phase 1 candidate-selection check (blocker closed).
			// Second call is the new dispatch-time recheck — blocker reopened.
			n := atomic.AddInt32(&depCalls, 1)
			state := "closed"
			if n > 1 {
				state = "open"
			}
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: state})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()
	var dispatches int32

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, i *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
		WithPollerMetrics(m),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (blocker reopened before dispatch)", got)
	}
	if got := atomic.LoadInt32(&depCalls); got < 2 {
		t.Fatalf("dependency issue fetched %d times, want ≥2 (poll-time + dispatch-time recheck)", got)
	}

	m.mu.Lock()
	skippedPending := m.skipped[skipreason.ReasonPendingDependency]
	m.mu.Unlock()
	if skippedPending != 1 {
		t.Errorf("skipped[pending_dependency] = %d, want 1", skippedPending)
	}

	// The issue must not be left permanently marked processed — it should be
	// reconsidered on the very next poll once the blocker closes again.
	poller.mu.RLock()
	_, processed := poller.processed[1]
	poller.mu.RUnlock()
	if processed {
		t.Error("issue should not remain marked processed after blocker-reopened skip")
	}
}

// GH-3789 (D6): the sequential/FIFO path (findOldestUnprocessedIssue) had no
// pollerMetrics visibility at all for skipped issues — per-project FIFO
// happening to queue the blocker first masked the missing gate. Verify the
// skip is now recorded the same way the parallel path already does.
func TestPoller_FindOldestUnprocessedIssue_RecordsSkipMetric(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "gated", Body: "Blocked by: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/repos/owner/repo/issues/100":
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: "open"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithPollerMetrics(m))

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue != nil {
		t.Errorf("issue should be nil (only candidate has an open blocker), got #%d", issue.Number)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.skipped[skipreason.ReasonPendingDependency]; got != 1 {
		t.Errorf("skipped[pending_dependency] = %d, want 1", got)
	}
}

func TestPoller_NoMetrics_NoPanic(t *testing.T) {
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "t", Labels: []Label{pilot, {Name: LabelInProgress}}},
		{Number: 2, Title: "t2", Labels: []Label{pilot}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	// No WithPollerMetrics — pollerMetrics is nil
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error { return nil }),
	)

	// Must not panic.
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()
}
