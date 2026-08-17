package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// recordingGHServer is a minimal httptest server for GH-4458's tests: it
// counts requests by method+path-prefix so tests can assert exactly which
// GitHub API calls fired (or didn't), without needing to model full request
// bodies. Any unmatched request gets a 200 with an empty JSON object, which
// decodes cleanly into every response type these code paths touch.
type recordingGHServer struct {
	mu    sync.Mutex
	calls []string // "METHOD path"
}

func (r *recordingGHServer) record(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method+" "+path)
}

func (r *recordingGHServer) count(method, pathPrefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, method+" "+pathPrefix) {
			n++
		}
	}
	return n
}

func newRecordingGHServer() (*recordingGHServer, *httptest.Server) {
	rec := &recordingGHServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		// GH-4872: safeDeleteBranch's branchIsBaseOfOpenPR check calls
		// ListPullRequests (GET .../pulls?state=open), which decodes the body
		// into a []*PullRequest — "{}" fails that decode (object, not array)
		// and makes the branch-delete guard fail closed, silently skipping
		// every DELETE these tests assert on. Reply "[]" for exactly the list
		// endpoint (no further path segments) so it decodes to zero open PRs;
		// every other endpoint keeps the original "{}" stub.
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	return rec, srv
}

// TestCheckExternalMergeOrClose_SelfCloseMarker covers GH-4458 (a): a PR
// stamped via markSelfClosed that GitHub now reports closed must be treated
// as autopilot's own internal state transition, not a human rejection —
// notifyExternalClose's reclassify-to-failed and removePR's branch delete
// (both GH-3818/D10 territory) must NOT fire.
func TestCheckExternalMergeOrClose_SelfCloseMarker(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{PRNumber: 42, IssueNumber: 10, BranchName: "pilot/GH-10", HeadSHA: "sha123"}
	c.mu.Lock()
	c.activePRs[42] = prState
	c.mu.Unlock()

	c.markSelfClosed(42)

	ghPR := &github.PullRequest{Number: 42, State: "closed", Merged: false}
	resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)

	if !resolved {
		t.Fatal("checkExternalMergeOrClose should report the PR as resolved (closed)")
	}
	if len(evalMock.reclassified) != 0 {
		t.Errorf("stamped self-close must not reclassify the execution row, got %+v", evalMock.reclassified)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 0 {
		t.Errorf("stamped self-close must not delete the branch, got %d DELETE calls", n)
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/42/comments"); n != 0 {
		t.Errorf("stamped self-close must not post notifyExternalClose's PR comment, got %d calls", n)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should no longer be tracked after resolving a stamped self-close")
	}
	// The marker is one-shot: a second stamped check for the same PR number
	// (were it somehow re-checked) must not still report stamped.
	if c.consumeSelfClosedMarker(42) {
		t.Error("self-close marker should be consumed after the first check")
	}
}

// TestCheckExternalMergeOrClose_UnstampedExternalClose covers GH-4458 (b):
// an external (human) close with no self-close marker must retain the
// existing GH-3818/D10 semantics — reclassify the completed execution row
// to failed, and delete the branch via removePR.
func TestCheckExternalMergeOrClose_UnstampedExternalClose(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{PRNumber: 43, IssueNumber: 11, BranchName: "pilot/GH-11", HeadSHA: "sha456", Error: "CI checks failed"}
	c.mu.Lock()
	c.activePRs[43] = prState
	c.mu.Unlock()

	// No markSelfClosed call: this close is unstamped, i.e. external.
	ghPR := &github.PullRequest{Number: 43, State: "closed", Merged: false}
	resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)

	if !resolved {
		t.Fatal("checkExternalMergeOrClose should report the PR as resolved (closed)")
	}
	if len(evalMock.reclassified) != 1 {
		t.Fatalf("unstamped external close must reclassify the execution row exactly once, got %+v", evalMock.reclassified)
	}
	if got, want := evalMock.reclassified[0].TaskID, "GH-11"; got != want {
		t.Errorf("reclassified TaskID = %q, want %q", got, want)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 1 {
		t.Errorf("unstamped external close must delete the branch exactly once, got %d DELETE calls", n)
	}
	if _, ok := c.GetPRState(43); ok {
		t.Error("PR should no longer be tracked after resolving an external close")
	}
}

// TestEscalateAndHold covers GH-4458 (c): escalateAndHold must apply
// pilot-needs-human plus caller labels, post the diagnostic comment, fire an
// alert, and set StageFailed — all without closing the PR or deleting its
// branch.
func TestEscalateAndHold(t *testing.T) {
	tests := []struct {
		name        string
		issueNumber int
		labels      []string
		comment     string
		withAlerts  bool
		wantLabels  []string // full expected label set (nil = AddLabels should not be called)
		wantComment bool
	}{
		{
			name:        "issue with caller labels and comment",
			issueNumber: 20,
			labels:      []string{"needs-manual-rebase"},
			comment:     "Automated conflict resolution gave up after 3 attempts.",
			withAlerts:  true,
			wantLabels:  []string{"pilot-needs-human", "needs-manual-rebase"},
			wantComment: true,
		},
		{
			name:        "no issue linked - labels skipped",
			issueNumber: 0,
			labels:      []string{"needs-manual-rebase"},
			comment:     "held for review",
			withAlerts:  true,
			wantLabels:  nil,
			wantComment: true,
		},
		{
			name:        "empty comment - PR comment skipped",
			issueNumber: 21,
			labels:      nil,
			comment:     "",
			withAlerts:  true,
			wantLabels:  []string{"pilot-needs-human"},
			wantComment: false,
		},
		{
			name:        "no alerts engine configured - must not panic",
			issueNumber: 22,
			labels:      []string{"needs-manual-rebase"},
			comment:     "held for review",
			withAlerts:  false,
			wantLabels:  []string{"pilot-needs-human", "needs-manual-rebase"},
			wantComment: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, srv := newRecordingGHServer()
			defer srv.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			var sink *fakeAlertSink
			if tt.withAlerts {
				sink = &fakeAlertSink{}
				c.SetAlertsEngine(sink)
			}

			prState := &PRState{PRNumber: 99, IssueNumber: tt.issueNumber, BranchName: "pilot/GH-99"}
			reason := "escalation reason: " + tt.name

			c.escalateAndHold(context.Background(), prState, reason, tt.labels, tt.comment)

			if prState.Stage != StageFailed {
				t.Errorf("Stage = %v, want StageFailed", prState.Stage)
			}
			if prState.Error != reason {
				t.Errorf("Error = %q, want %q", prState.Error, reason)
			}

			labelPath := "/repos/owner/repo/issues/" + strconv.Itoa(tt.issueNumber) + "/labels"
			gotLabelCalls := rec.count(http.MethodPost, labelPath)
			if tt.wantLabels == nil {
				if gotLabelCalls != 0 {
					t.Errorf("AddLabels should not be called without a linked issue, got %d calls", gotLabelCalls)
				}
			} else if gotLabelCalls != 1 {
				t.Errorf("AddLabels calls = %d, want 1", gotLabelCalls)
			}

			commentPath := "/repos/owner/repo/issues/99/comments"
			gotCommentCalls := rec.count(http.MethodPost, commentPath)
			if tt.wantComment && gotCommentCalls != 1 {
				t.Errorf("PR comment calls = %d, want 1", gotCommentCalls)
			}
			if !tt.wantComment && gotCommentCalls != 0 {
				t.Errorf("PR comment calls = %d, want 0 for empty comment", gotCommentCalls)
			}

			// No close, no branch delete, ever.
			if n := rec.count(http.MethodPatch, "/repos/owner/repo/pulls/99"); n != 0 {
				t.Errorf("escalateAndHold must never close the PR, got %d PATCH calls to pulls/99", n)
			}
			if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 0 {
				t.Errorf("escalateAndHold must never delete the branch, got %d DELETE calls", n)
			}

			if tt.withAlerts {
				if len(sink.events) != 1 {
					t.Fatalf("alert events = %d, want 1: %+v", len(sink.events), sink.events)
				}
				ev := sink.events[0]
				if ev.Type != alerts.EventTypeTaskFailed {
					t.Errorf("alert Type = %v, want EventTypeTaskFailed", ev.Type)
				}
				if ev.Error != reason {
					t.Errorf("alert Error = %q, want %q", ev.Error, reason)
				}
				if ev.Metadata["labels"] != strings.Join(tt.labels, ",") {
					t.Errorf("alert Metadata[labels] = %q, want %q", ev.Metadata["labels"], strings.Join(tt.labels, ","))
				}
			}
		})
	}
}
