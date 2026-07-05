package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestPoller_PendingDependencySkip_ParallelMode is the D6 reproduction
// (GH-3789/GH-3759) for parallel/auto mode: issue #1's "Blocked by: #100"
// blocker is open and present in the SAME fetched candidate list, so #1 must
// be skipped with skipreason.ReasonPendingDependency recorded, while #2
// (no dependency) dispatches normally.
func TestPoller_PendingDependencySkip_ParallelMode(t *testing.T) {
	pilot := Label{Name: "pilot"}
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "blocked", Body: "Blocked by: #100", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, Title: "clear", Labels: []Label{pilot}, CreatedAt: now},
		// Open blocker — present in the fetch (kept off the dispatch path via in-progress).
		{Number: 100, Title: "blocker", Labels: []Label{pilot, {Name: LabelInProgress}}, CreatedAt: now.Add(-2 * time.Hour)},
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
			if lastSegment == "100" {
				t.Errorf("unexpected per-blocker API call to %s — gate must resolve in-memory (GH-3789)", r.URL.Path)
			}
			// Fresh-label refresh for the dispatched candidate (#2).
			_ = json.NewEncoder(w).Encode(issues[1])
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()

	var dispatched []int
	var mu sync.Mutex
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			dispatched = append(dispatched, issue.Number)
			mu.Unlock()
			return nil
		}),
		WithPollerMetrics(m),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := dispatched
	mu.Unlock()
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("dispatched = %v, want [2] (blocked #1 skipped, #100 held in-progress)", got)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.skipped[skipreason.ReasonPendingDependency] != 1 {
		t.Errorf("skipped[%q] = %d, want 1", skipreason.ReasonPendingDependency, m.skipped[skipreason.ReasonPendingDependency])
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
