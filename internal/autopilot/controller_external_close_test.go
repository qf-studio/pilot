package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// externalCloseServer is a fake GitHub that serves one issue (#10) and records
// the comments and label mutations notifyExternalClose makes (GH-6131).
type externalCloseServer struct {
	*httptest.Server

	mu            sync.Mutex
	issueComments []string // bodies posted on issue #10
	prComments    []string // bodies posted on PR #42
	added         []string // labels POSTed to issue #10
	removed       []string // labels DELETEd from issue #10
}

func newExternalCloseServer(t *testing.T, issue github.Issue) *externalCloseServer {
	t.Helper()
	s := &externalCloseServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(issue)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.added = append(s.added, body.Labels...)
			_, _ = w.Write([]byte("[]"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
			s.removed = append(s.removed, strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/"))
		case r.URL.Path == "/repos/owner/repo/issues/10/comments" && r.Method == http.MethodPost:
			s.issueComments = append(s.issueComments, decodeCommentBody(r))
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			s.prComments = append(s.prComments, decodeCommentBody(r))
			_, _ = w.Write([]byte(`{"id":2}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func decodeCommentBody(r *http.Request) string {
	var body struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.Body
}

func (s *externalCloseServer) controller() *Controller {
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, s.URL)
	return NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
}

func issueWithLabels(state string, labels ...string) github.Issue {
	issue := github.Issue{Number: 10, State: state}
	for _, l := range labels {
		issue.Labels = append(issue.Labels, github.Label{Name: l})
	}
	return issue
}

func TestNotifyExternalClose_ClosedIssue_NoRetryTextAndLabelsRemoved(t *testing.T) {
	srv := newExternalCloseServer(t, issueWithLabels("closed", "pilot", "pilot-in-progress", "pilot-review-rc"))
	c := srv.controller()

	c.notifyExternalClose(context.Background(), &PRState{PRNumber: 42, IssueNumber: 10})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.issueComments) != 1 {
		t.Fatalf("issue comments = %d, want 1: %q", len(srv.issueComments), srv.issueComments)
	}
	comment := srv.issueComments[0]
	if !strings.Contains(comment, "already closed") {
		t.Errorf("comment missing %q: %q", "already closed", comment)
	}
	if strings.Contains(comment, "retried automatically") {
		t.Errorf("comment on a closed issue must not promise a retry: %q", comment)
	}
	if want := []string{"pilot-in-progress", "pilot-review-rc"}; !reflect.DeepEqual(srv.removed, want) {
		t.Errorf("removed labels = %v, want %v", srv.removed, want)
	}
	if len(srv.added) != 0 {
		t.Errorf("no label may be added to a closed issue, got %v", srv.added)
	}
}

func TestNotifyExternalClose_OpenIssue_Unchanged(t *testing.T) {
	srv := newExternalCloseServer(t, issueWithLabels("open", "pilot", "pilot-in-progress"))
	c := srv.controller()

	c.notifyExternalClose(context.Background(), &PRState{PRNumber: 42, IssueNumber: 10})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.issueComments) != 1 {
		t.Fatalf("issue comments = %d, want 1: %q", len(srv.issueComments), srv.issueComments)
	}
	if want := "The issue has been marked pilot-retry-ready and will be retried automatically."; !strings.Contains(srv.issueComments[0], want) {
		t.Errorf("comment missing %q: %q", want, srv.issueComments[0])
	}
	if want := []string{"pilot-retry-ready", "pilot"}; !reflect.DeepEqual(srv.added, want) {
		t.Errorf("added labels = %v, want %v", srv.added, want)
	}
	removed := strings.Join(srv.removed, ",")
	if !strings.Contains(removed, "pilot-in-progress") {
		t.Errorf("removed labels = %v, want pilot-in-progress among them", srv.removed)
	}
}

func TestNotifyExternalClose_RCHeldReason(t *testing.T) {
	srv := newExternalCloseServer(t, issueWithLabels("open", "pilot", "pilot-in-progress", "pilot-review-rc"))
	c := srv.controller()
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{
		PRNumber:         42,
		IssueNumber:      10,
		Stage:            StageAwaitApproval,
		EscalationReason: preReviewReasonPrefix + ` verdict REQUEST-CHANGES, fix outcome "none" (review mode enforce)`,
	}
	c.notifyExternalClose(context.Background(), prState)

	const want = "closed by hand while held"
	if len(evalMock.reclassified) != 1 || !strings.Contains(evalMock.reclassified[0].Reason, want) {
		t.Errorf("execution reason = %+v, want one reclassify carrying %q", evalMock.reclassified, want)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.prComments) != 1 || !strings.Contains(srv.prComments[0], want) {
		t.Errorf("PR comments = %q, want one carrying %q", srv.prComments, want)
	}
	if len(srv.issueComments) != 1 || !strings.Contains(srv.issueComments[0], want) {
		t.Errorf("issue comments = %q, want one carrying %q", srv.issueComments, want)
	}
	for _, got := range append(append([]string{}, srv.prComments...), srv.issueComments...) {
		if strings.Contains(got, "no reason recorded") {
			t.Errorf("RC-held close must not read %q: %q", "no reason recorded", got)
		}
	}
}

// cancelRecordingHandler is an approval.Handler that records withdrawals.
type cancelRecordingHandler struct {
	mu        sync.Mutex
	cancelled []string
}

func (h *cancelRecordingHandler) Name() string { return "telegram" }

func (h *cancelRecordingHandler) SendApprovalRequest(context.Context, *approval.Request) (<-chan *approval.Response, error) {
	return make(chan *approval.Response, 1), nil
}

func (h *cancelRecordingHandler) CancelRequest(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelled = append(h.cancelled, id)
	return nil
}

// countingDecisionWriter counts decisions that reach the persistence layer.
type countingDecisionWriter struct {
	mu    sync.Mutex
	calls int
}

func (w *countingDecisionWriter) SetApprovalDecision(context.Context, string, string, string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return nil
}

func TestNotifyExternalClose_CancelsPendingApproval(t *testing.T) {
	srv := newExternalCloseServer(t, issueWithLabels("open", "pilot", "pilot-in-progress"))

	mgr := asyncApprovalManager()
	handler := &cancelRecordingHandler{}
	mgr.RegisterHandler(handler)
	writer := &countingDecisionWriter{}
	mgr.WithStateWriter(writer)

	const reqID = "pr-42-1793917751"
	if _, err := mgr.SubmitApprovalRequest(context.Background(), &approval.Request{
		ID:        reqID,
		TaskID:    "GH-10",
		Stage:     approval.StagePreMerge,
		Title:     "PR #42",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SubmitApprovalRequest: %v", err)
	}
	if len(mgr.GetPendingRequests()) != 1 {
		t.Fatalf("pending requests = %d, want 1", len(mgr.GetPendingRequests()))
	}

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, mgr, "owner", "repo")
	c.notifyExternalClose(context.Background(), &PRState{
		PRNumber:          42,
		IssueNumber:       10,
		Stage:             StageAwaitApproval,
		ApprovalRequestID: reqID,
	})

	if n := len(mgr.GetPendingRequests()); n != 0 {
		t.Errorf("pending requests after close = %d, want 0", n)
	}
	if !mgr.IsCancelled(reqID) {
		t.Errorf("request %s not marked cancelled", reqID)
	}
	handler.mu.Lock()
	gotCancelled := append([]string(nil), handler.cancelled...)
	handler.mu.Unlock()
	// The manager and the request's own waiter goroutine may both withdraw it;
	// CancelRequest is idempotent, so only the id matters.
	if len(gotCancelled) == 0 {
		t.Error("handler was never asked to withdraw the request")
	}
	for _, id := range gotCancelled {
		if id != reqID {
			t.Errorf("handler withdrew %q, want only %q", id, reqID)
		}
	}

	// A late click on the live card must be ignored, not persisted as a decision.
	err := mgr.RecordDecision(context.Background(), reqID, approval.DecisionApproved, "alice")
	if !errors.Is(err, approval.ErrRequestCancelled) {
		t.Errorf("late RecordDecision err = %v, want ErrRequestCancelled", err)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.calls != 0 {
		t.Errorf("late decision reached the state writer %d time(s), want 0", writer.calls)
	}
}
