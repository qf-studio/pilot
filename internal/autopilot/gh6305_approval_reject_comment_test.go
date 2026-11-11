package autopilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ghCapture records the PR comment bodies and the issue-label adds the
// controller sends to GitHub.
type ghCapture struct {
	mu       sync.Mutex
	comments []string
	labels   []string
}

func (g *ghCapture) snapshot() (comments, labels []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.comments...), append([]string(nil), g.labels...)
}

func newCapturingGHServer(t *testing.T) (*ghCapture, *httptest.Server) {
	t.Helper()
	cap := &ghCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost {
			switch {
			case strings.HasSuffix(r.URL.Path, "/comments"):
				var body struct {
					Body string `json:"body"`
				}
				_ = json.Unmarshal(raw, &body)
				cap.mu.Lock()
				cap.comments = append(cap.comments, body.Body)
				cap.mu.Unlock()
			case strings.HasSuffix(r.URL.Path, "/labels"):
				var body struct {
					Labels []string `json:"labels"`
				}
				_ = json.Unmarshal(raw, &body)
				cap.mu.Lock()
				for _, l := range body.Labels {
					cap.labels = append(cap.labels, r.URL.Path+" "+l)
				}
				cap.mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return cap, srv
}

func newApprovalCommentController(t *testing.T) (*Controller, *ghCapture) {
	t.Helper()
	cap, srv := newCapturingGHServer(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")
	return c, cap
}

const approvalHandoff = "Pilot stops work on this pull request. Close it to let Pilot retry the issue, or merge it by hand."

func TestApplyApprovalDecision_RejectPostsOneCommentAndLabels(t *testing.T) {
	c, cap := newApprovalCommentController(t)
	prState := &PRState{
		PRNumber:               77,
		IssueNumber:            70,
		Stage:                  StageAwaitApproval,
		ApprovalRequestID:      "req-77",
		ApprovalDecision:       string(approval.DecisionRejected),
		ApprovalDecisionBy:     "alice",
		ApprovalDecisionReason: "touches the billing path\nneeds a design review.",
	}

	if err := c.handleAwaitApproval(context.Background(), prState); err != nil {
		t.Fatalf("handleAwaitApproval: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s", prState.Stage, StageFailed)
	}

	comments, labels := cap.snapshot()
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1: %q", len(comments), comments)
	}
	want := "Merge rejected by alice. Reason: touches the billing path needs a design review.\n\n" + approvalHandoff
	if comments[0] != want {
		t.Errorf("comment = %q, want %q", comments[0], want)
	}
	if len(labels) != 1 || labels[0] != "/repos/owner/repo/issues/70/labels "+github.LabelFailed {
		t.Errorf("labels = %q, want one %s on issue 70", labels, github.LabelFailed)
	}
	if prState.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q", prState.TerminalLabel, github.LabelFailed)
	}

	// A second tick re-enters the same path: reject comment is posted once.
	if err := c.handleAwaitApproval(context.Background(), prState); err != nil {
		t.Fatalf("second handleAwaitApproval: %v", err)
	}
	comments, labels = cap.snapshot()
	if len(comments) != 1 {
		t.Errorf("reject comment is posted once: got %d comments after second tick", len(comments))
	}
	if len(labels) != 1 {
		t.Errorf("label adds after second tick = %d, want 1", len(labels))
	}
}

func TestApplyApprovalDecision_RejectWithoutReasonOmitsReasonSentence(t *testing.T) {
	c, cap := newApprovalCommentController(t)
	prState := &PRState{
		PRNumber:           78,
		IssueNumber:        71,
		ApprovalDecision:   string(approval.DecisionRejected),
		ApprovalDecisionBy: "bob",
	}

	if err := c.applyApprovalDecision(context.Background(), prState); err != nil {
		t.Fatalf("applyApprovalDecision: %v", err)
	}
	comments, _ := cap.snapshot()
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(comments))
	}
	if want := "Merge rejected by bob.\n\n" + approvalHandoff; comments[0] != want {
		t.Errorf("comment = %q, want %q", comments[0], want)
	}
}

func TestApplyApprovalDecision_TimeoutPostsExpiryText(t *testing.T) {
	for _, by := range []string{"system", approvalDecisionSourceWallClockExpiryDefault} {
		t.Run(by, func(t *testing.T) {
			c, cap := newApprovalCommentController(t)
			prState := &PRState{
				PRNumber:           79,
				IssueNumber:        72,
				ApprovalDecision:   string(approval.DecisionRejected),
				ApprovalDecisionBy: by,
			}

			if err := c.applyApprovalDecision(context.Background(), prState); err != nil {
				t.Fatalf("applyApprovalDecision: %v", err)
			}
			comments, labels := cap.snapshot()
			if len(comments) != 1 {
				t.Fatalf("comments = %d, want 1", len(comments))
			}
			timeout := c.approvalMgr.PreMergeTimeout().String()
			want := "No decision in " + timeout + "; the request expired.\n\n" + approvalHandoff
			if comments[0] != want {
				t.Errorf("comment = %q, want %q", comments[0], want)
			}
			if strings.Contains(comments[0], "Merge rejected by") {
				t.Errorf("expiry comment must not claim a person rejected it: %q", comments[0])
			}
			if len(labels) != 1 {
				t.Errorf("labels = %q, want one pilot-failed add", labels)
			}
		})
	}
}

func TestApplyApprovalDecision_ApprovePostsNothing(t *testing.T) {
	c, cap := newApprovalCommentController(t)
	prState := &PRState{
		PRNumber:               80,
		IssueNumber:            73,
		ApprovalDecision:       string(approval.DecisionApproved),
		ApprovalDecisionBy:     "carol",
		ApprovalDecisionReason: "ship it",
	}

	if err := c.applyApprovalDecision(context.Background(), prState); err != nil {
		t.Fatalf("applyApprovalDecision: %v", err)
	}
	if prState.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageMerging)
	}
	comments, labels := cap.snapshot()
	if len(comments) != 0 || len(labels) != 0 {
		t.Errorf("approve posted comments=%q labels=%q, want none", comments, labels)
	}
}

func TestSetApprovalDecisionWithReason_RecordsReasonOnPRState(t *testing.T) {
	c, _ := newApprovalCommentController(t)
	prState := &PRState{PRNumber: 81, ApprovalRequestID: "req-81"}
	c.mu.Lock()
	c.activePRs[81] = prState
	c.mu.Unlock()

	if err := c.SetApprovalDecisionWithReason(context.Background(), "req-81", "rejected", "dave", "too risky"); err != nil {
		t.Fatalf("SetApprovalDecisionWithReason: %v", err)
	}
	if prState.ApprovalDecision != "rejected" || prState.ApprovalDecisionBy != "dave" || prState.ApprovalDecisionReason != "too risky" {
		t.Errorf("state = %q/%q/%q, want rejected/dave/too risky",
			prState.ApprovalDecision, prState.ApprovalDecisionBy, prState.ApprovalDecisionReason)
	}
}

func TestStateStore_RoundTripsApprovalDecisionFields(t *testing.T) {
	store := newTestStateStore(t)
	pr := &PRState{
		PRNumber:               82,
		IssueNumber:            74,
		Stage:                  StageFailed,
		ApprovalDecisionBy:     "erin",
		ApprovalDecisionReason: "wrong approach",
		ApprovalRejectNotified: true,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	got, err := store.GetPRState("owner/repo", 82)
	if err != nil || got == nil {
		t.Fatalf("GetPRState: %v, %v", got, err)
	}
	if got.ApprovalDecisionBy != "erin" || got.ApprovalDecisionReason != "wrong approach" || !got.ApprovalRejectNotified {
		t.Errorf("round trip lost fields: %+v", got)
	}
}
