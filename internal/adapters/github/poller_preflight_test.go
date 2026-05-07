package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockPreFlightJudger is a test double for PreFlightJudger.
type mockPreFlightJudger struct {
	verdict Verdict
	err     error
	calls   int32
}

func (m *mockPreFlightJudger) JudgeIssue(_ context.Context, _, _, _ string) (Verdict, error) {
	atomic.AddInt32(&m.calls, 1)
	return m.verdict, m.err
}

// mockExecutionSaver records SaveDeclinedExecution calls.
type mockExecutionSaver struct {
	mu   sync.Mutex
	rows []savedRow
}

type savedRow struct {
	taskID      string
	projectPath string
	status      string
	reason      string
}

func (m *mockExecutionSaver) SaveDeclinedExecution(taskID, projectPath, status, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, savedRow{taskID: taskID, projectPath: projectPath, status: status, reason: reason})
	return nil
}

// newTestPollerServer returns a minimal httptest.Server serving a single issue
// and captures labels/comments added to it.
type pollerTestServer struct {
	server        *httptest.Server
	mu            sync.Mutex
	labelsAdded   []string
	commentsAdded []string
}

func newPollerTestServer(t *testing.T, issues []*Issue) *pollerTestServer {
	t.Helper()
	pts := &pollerTestServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/test/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprint(w, "[")
			for i, iss := range issues {
				if i > 0 {
					_, _ = fmt.Fprint(w, ",")
				}
				_ = json.NewEncoder(w).Encode(iss)
			}
			_, _ = fmt.Fprint(w, "]")
		}
	})

	for _, iss := range issues {
		num := iss.Number
		path := fmt.Sprintf("/repos/test/repo/issues/%d", num)
		mux.HandleFunc(path+"/labels", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				var body struct {
					Labels []string `json:"labels"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				pts.mu.Lock()
				pts.labelsAdded = append(pts.labelsAdded, body.Labels...)
				pts.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, "[]")
			}
		})
		mux.HandleFunc(path+"/comments", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				var body struct {
					Body string `json:"body"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				pts.mu.Lock()
				pts.commentsAdded = append(pts.commentsAdded, body.Body)
				pts.mu.Unlock()
				_, _ = fmt.Fprint(w, `{"id":1,"body":"ok"}`)
			}
		})
		// Single issue GET (label refresh)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			for _, iss2 := range issues {
				if iss2.Number == num {
					_ = json.NewEncoder(w).Encode(iss2)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		})
	}

	pts.server = httptest.NewServer(mux)
	return pts
}

func (pts *pollerTestServer) Close() { pts.server.Close() }

// TestPoller_PreFlight_AcceptVerdictDispatches verifies that an accept verdict dispatches as today.
func TestPoller_PreFlight_AcceptVerdictDispatches(t *testing.T) {
	issue := &Issue{
		Number:    42,
		Title:     "feat: add login button",
		Body:      "Add a login button to the nav header.",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	pts := newPollerTestServer(t, []*Issue{issue})
	defer pts.Close()

	judger := &mockPreFlightJudger{verdict: Verdict{Accepted: true, Decision: "accept", Confidence: 0.9}}
	var dispatched int32
	poller, err := NewPoller(
		NewClientWithBaseURL("test-token", pts.server.URL),
		"test/repo", "pilot", 50*time.Millisecond,
		WithOnIssue(func(_ context.Context, iss *Issue) error {
			atomic.AddInt32(&dispatched, 1)
			return nil
		}),
		WithPreFlightJudge(judger),
		WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go poller.Start(ctx)
	<-ctx.Done()

	if atomic.LoadInt32(&dispatched) == 0 {
		t.Error("expected issue to be dispatched when verdict is accept")
	}
	pts.mu.Lock()
	defer pts.mu.Unlock()
	if len(pts.labelsAdded) > 0 {
		t.Errorf("expected no labels added on accept, got %v", pts.labelsAdded)
	}
}

// TestPoller_PreFlight_RejectVerdictAddsLabelAndComment verifies that a reject verdict:
// - adds pilot-needs-clarification label
// - posts a comment
// - does NOT dispatch the issue
func TestPoller_PreFlight_RejectVerdictAddsLabelAndComment(t *testing.T) {
	issue := &Issue{
		Number:    43,
		Title:     "??",
		Body:      "",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	pts := newPollerTestServer(t, []*Issue{issue})
	defer pts.Close()

	judger := &mockPreFlightJudger{verdict: Verdict{
		Accepted:   false,
		Decision:   "reject_vague",
		Reason:     "Issue title is too vague.",
		Confidence: 0.92,
	}}
	saver := &mockExecutionSaver{}
	var dispatched int32

	poller, err := NewPoller(
		NewClientWithBaseURL("test-token", pts.server.URL),
		"test/repo", "pilot", 50*time.Millisecond,
		WithOnIssue(func(_ context.Context, iss *Issue) error {
			atomic.AddInt32(&dispatched, 1)
			return nil
		}),
		WithPreFlightJudge(judger),
		WithExecutionSaver(saver),
		WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go poller.Start(ctx)
	<-ctx.Done()

	if atomic.LoadInt32(&dispatched) > 0 {
		t.Error("expected issue NOT to be dispatched on reject verdict")
	}

	pts.mu.Lock()
	labelsAdded := pts.labelsAdded
	commentsAdded := pts.commentsAdded
	pts.mu.Unlock()

	hasLabel := false
	for _, l := range labelsAdded {
		if l == LabelNeedsClarification {
			hasLabel = true
		}
	}
	if !hasLabel {
		t.Errorf("expected %q label to be added, got: %v", LabelNeedsClarification, labelsAdded)
	}
	if len(commentsAdded) == 0 {
		t.Error("expected a comment to be posted")
	}

	saver.mu.Lock()
	defer saver.mu.Unlock()
	if len(saver.rows) == 0 {
		t.Error("expected an execution row to be saved")
	}
	if saver.rows[0].status != "declined-preflight" {
		t.Errorf("expected status 'declined-preflight', got %q", saver.rows[0].status)
	}
}

// TestPoller_PreFlight_JudgeError_FailOpen verifies that a judge error does not block dispatch.
func TestPoller_PreFlight_JudgeError_FailOpen(t *testing.T) {
	issue := &Issue{
		Number:    44,
		Title:     "feat: add feature",
		Body:      "Add a feature.",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	pts := newPollerTestServer(t, []*Issue{issue})
	defer pts.Close()

	judger := &mockPreFlightJudger{err: fmt.Errorf("judge API unavailable")}
	var dispatched int32

	poller, err := NewPoller(
		NewClientWithBaseURL("test-token", pts.server.URL),
		"test/repo", "pilot", 50*time.Millisecond,
		WithOnIssue(func(_ context.Context, iss *Issue) error {
			atomic.AddInt32(&dispatched, 1)
			return nil
		}),
		WithPreFlightJudge(judger),
		WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go poller.Start(ctx)
	<-ctx.Done()

	if atomic.LoadInt32(&dispatched) == 0 {
		t.Error("expected issue to be dispatched (fail-open) when judge returns error")
	}
}

// TestPoller_PreFlight_NilJudge_ExistingBehavior verifies that nil judge preserves existing behavior.
func TestPoller_PreFlight_NilJudge_ExistingBehavior(t *testing.T) {
	issue := &Issue{
		Number:    45,
		Title:     "feat: add feature",
		Body:      "Add a feature.",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	pts := newPollerTestServer(t, []*Issue{issue})
	defer pts.Close()

	var dispatched int32

	// No WithPreFlightJudge — nil judge
	poller, err := NewPoller(
		NewClientWithBaseURL("test-token", pts.server.URL),
		"test/repo", "pilot", 50*time.Millisecond,
		WithOnIssue(func(_ context.Context, iss *Issue) error {
			atomic.AddInt32(&dispatched, 1)
			return nil
		}),
		WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go poller.Start(ctx)
	<-ctx.Done()

	if atomic.LoadInt32(&dispatched) == 0 {
		t.Error("expected issue to be dispatched when pre-flight judge is disabled (nil)")
	}
}

// TestPoller_PreFlight_RejectSkipsMarkProcessed verifies that label removal allows re-dispatch.
// After rejection the issue should NOT be in the processed map, so removing
// LabelNeedsClarification allows the next poll to dispatch it.
func TestPoller_PreFlight_RejectSkipsMarkProcessed(t *testing.T) {
	issue := &Issue{
		Number:    46,
		Title:     "??",
		Body:      "",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	pts := newPollerTestServer(t, []*Issue{issue})
	defer pts.Close()

	callCount := int32(0)
	judger := &mockPreFlightJudger{verdict: Verdict{
		Accepted: false,
		Decision: "reject_vague",
		Reason:   "Vague.",
	}}

	poller, err := NewPoller(
		NewClientWithBaseURL("test-token", pts.server.URL),
		"test/repo", "pilot", 50*time.Millisecond,
		WithOnIssue(func(_ context.Context, iss *Issue) error { return nil }),
		WithPreFlightJudge(judger),
		WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go poller.Start(ctx)
	<-ctx.Done()

	calls := atomic.LoadInt32(&judger.calls)
	atomic.StoreInt32(&callCount, calls)

	// Issue should NOT be in the processed map (so removal of label allows retry)
	poller.mu.RLock()
	_, inProcessed := poller.processed[issue.Number]
	poller.mu.RUnlock()

	if inProcessed {
		t.Error("rejected issue should NOT be in the processed map (so label removal re-triggers)")
	}
	if callCount == 0 {
		t.Error("expected pre-flight judge to be called at least once")
	}
}
