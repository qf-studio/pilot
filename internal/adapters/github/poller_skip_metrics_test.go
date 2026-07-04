package github

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestPoller_SequentialMode_PendingDependency_RecordsSkipMetric covers GH-3789:
// findOldestUnprocessedIssue used to skip dependency-gated issues silently
// (log only, no metric), which made the gate invisible in poller metrics.
func TestPoller_SequentialMode_PendingDependency_RecordsSkipMetric(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		depStates  map[int]string // dependency issue number -> state
		wantSkip   int
		wantNumber int // issue number expected to be returned, 0 if none
	}{
		{
			name:       "open blocker",
			body:       "Blocked by: #100",
			depStates:  map[int]string{100: "open"},
			wantSkip:   1,
			wantNumber: 0,
		},
		{
			name:       "closed blocker",
			body:       "Blocked by: #100",
			depStates:  map[int]string{100: "closed"},
			wantSkip:   0,
			wantNumber: 1,
		},
		{
			name:       "missing blocker reference",
			body:       "Regular issue, no dependency",
			depStates:  nil,
			wantSkip:   0,
			wantNumber: 1,
		},
		{
			name:       "multiple blockers, one still open",
			body:       "Depends on: #100\nBlocked by: #101",
			depStates:  map[int]string{100: "closed", 101: "open"},
			wantSkip:   1,
			wantNumber: 0,
		},
		{
			name:       "multiple blockers, all closed",
			body:       "Depends on: #100\nBlocked by: #101",
			depStates:  map[int]string{100: "closed", 101: "closed"},
			wantSkip:   0,
			wantNumber: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Number: 1, Title: "gated", Body: tt.body, Labels: []Label{{Name: "pilot"}}, CreatedAt: time.Now()}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				if r.URL.Path == "/repos/owner/repo/issues" {
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
					return
				}
				var num int
				_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &num)
				if state, ok := tt.depStates[num]; ok {
					_ = json.NewEncoder(w).Encode(&Issue{Number: num, State: state})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			m := newFakePollerMetrics()
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithPollerMetrics(m))

			got, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			if tt.wantNumber == 0 && got != nil {
				t.Errorf("got issue #%d, want nil", got.Number)
			}
			if tt.wantNumber != 0 && (got == nil || got.Number != tt.wantNumber) {
				t.Errorf("got issue = %+v, want #%d", got, tt.wantNumber)
			}

			m.mu.Lock()
			defer m.mu.Unlock()
			if got := m.skipped[skipreason.ReasonPendingDependency]; got != tt.wantSkip {
				t.Errorf("skipped[pending_dependency] = %d, want %d", got, tt.wantSkip)
			}
		})
	}
}

// TestPoller_CheckForNewIssues_PendingDependency_RecordsSkipMetric covers the
// parallel/auto dispatch path's Phase 1 filter (GH-3789).
func TestPoller_CheckForNewIssues_PendingDependency_RecordsSkipMetric(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		depStates      map[int]string
		wantSkip       int
		wantDispatched int
	}{
		{
			name:      "open blocker",
			body:      "Blocked by: #100",
			depStates: map[int]string{100: "open"},
			wantSkip:  1,
		},
		{
			name:           "closed blocker",
			body:           "Blocked by: #100",
			depStates:      map[int]string{100: "closed"},
			wantDispatched: 1,
		},
		{
			name:      "multiple blockers, one still open",
			body:      "Depends on: #100\nBlocked by: #101",
			depStates: map[int]string{100: "closed", 101: "open"},
			wantSkip:  1,
		},
		{
			name:           "multiple blockers, all closed",
			body:           "Depends on: #100\nBlocked by: #101",
			depStates:      map[int]string{100: "closed", 101: "closed"},
			wantDispatched: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Number: 1, Title: "gated", Body: tt.body, Labels: []Label{{Name: "pilot"}}, CreatedAt: time.Now()}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/repos/owner/repo/issues" {
					_ = json.NewEncoder(w).Encode([]*Issue{issue})
					return
				}
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				var num int
				_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &num)
				if num == issue.Number {
					// fresh-label GET before dispatch
					_ = json.NewEncoder(w).Encode(issue)
					return
				}
				if state, ok := tt.depStates[num]; ok {
					_ = json.NewEncoder(w).Encode(&Issue{Number: num, State: state})
					return
				}
				w.WriteHeader(http.StatusNotFound)
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
			if got := m.skipped[skipreason.ReasonPendingDependency]; got != tt.wantSkip {
				t.Errorf("skipped[pending_dependency] = %d, want %d", got, tt.wantSkip)
			}
			if m.dispatched != tt.wantDispatched {
				t.Errorf("dispatched = %d, want %d", m.dispatched, tt.wantDispatched)
			}
		})
	}
}

// TestPoller_CheckForNewIssues_DispatchTimeRecheck_BlockerReopened covers
// GH-3789's dispatch-time recheck: a candidate that cleared the Phase 1
// dependency check must not be dispatched if the blocker is open again by
// the time Phase 3 actually reaches it (simulating a task that sat queued
// behind a full worker pool while its blocker reopened).
func TestPoller_CheckForNewIssues_DispatchTimeRecheck_BlockerReopened(t *testing.T) {
	issue := &Issue{Number: 1, Title: "gated", Body: "Blocked by: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: time.Now()}

	var blockerCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
			return
		}
		if r.URL.Path == "/repos/owner/repo/issues/1" {
			// fresh-label GET before dispatch
			_ = json.NewEncoder(w).Encode(issue)
			return
		}
		if r.URL.Path == "/repos/owner/repo/issues/100" {
			n := atomic.AddInt32(&blockerCalls, 1)
			state := "closed"
			if n > 1 {
				// Reopened between the Phase 1 filter and the Phase 3 recheck.
				state = "open"
			}
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: state})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			t.Fatal("issue should not have been dispatched — blocker reopened before dispatch")
			return nil
		}),
		WithPollerMetrics(m),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if atomic.LoadInt32(&blockerCalls) < 2 {
		t.Fatalf("blocker state should have been checked at both Phase 1 and Phase 3, got %d calls", blockerCalls)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dispatched != 0 {
		t.Errorf("dispatched = %d, want 0", m.dispatched)
	}
	if got := m.skipped[skipreason.ReasonPendingDependency]; got < 1 {
		t.Errorf("skipped[pending_dependency] = %d, want ≥ 1", got)
	}
}

// TestPoller_StartSequential_DispatchTimeRecheck_BlockerReopened is the
// sequential-mode counterpart: findOldestUnprocessedIssue clears the issue,
// but the dispatch-time recheck in startSequential must catch a blocker that
// reopens before processIssueSequential is invoked.
func TestPoller_StartSequential_DispatchTimeRecheck_BlockerReopened(t *testing.T) {
	issue := &Issue{Number: 1, Title: "gated", Body: "Blocked by: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: time.Now()}

	var blockerCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
			return
		}
		if r.URL.Path == "/repos/owner/repo/issues/100" {
			n := atomic.AddInt32(&blockerCalls, 1)
			state := "closed"
			if n > 1 {
				state = "open"
			}
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: state})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()

	var dispatchedIssue int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 10*time.Millisecond,
		WithExecutionMode(ExecutionModeSequential),
		WithSequentialConfig(false, 10*time.Millisecond, 100*time.Millisecond),
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			atomic.StoreInt32(&dispatchedIssue, int32(issue.Number))
			return &IssueResult{Success: true}, nil
		}),
		WithPollerMetrics(m),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	poller.Start(ctx)

	if got := atomic.LoadInt32(&dispatchedIssue); got != 0 {
		t.Errorf("issue #%d was dispatched, want none — blocker reopened before dispatch-time recheck", got)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.skipped[skipreason.ReasonPendingDependency]; got < 1 {
		t.Errorf("skipped[pending_dependency] = %d, want ≥ 1", got)
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
