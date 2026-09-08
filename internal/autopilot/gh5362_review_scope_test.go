package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5362: handleReviewRequested (the review-revision close path) had the
// same "#275 false-success shape" bug GH-5348/GH-5351 fixed for the CI-fix
// path. spawnReviewIssue used to mark the source issue pilot-superseded the
// instant the revision issue was spawned — before that issue's own PR
// existed, let alone merged — and handleReviewRequested closed the PR and
// deleted the branch without ever stamping a self-close marker, so the very
// next poll's checkExternalMergeOrClose read its own close as an external
// (human) rejection.
//
// This file covers:
//  1. handleReviewRequested's own close is never read as external, and the
//     branch survives it (own-close + branch-preservation half of the fix,
//     controller.go).
//  2. verifyFixPRDeliversSourceScope's review-revision gate: pilot-superseded
//     is applied to the source issue only once the revision PR merges with
//     confirmed file overlap — never at spawn time (owner_death.go half of
//     the fix, mirroring gh5348_scope_mismatch_test.go for the review path).

type reviewScopeGHServer struct {
	mu sync.Mutex

	issues  map[int]*github.Issue    // GET /issues/{n}
	prFiles map[int][]*github.PRFile // GET /pulls/{n}/files

	addLabelCalls    []string // "issue:label"
	removeLabelCalls []string // "issue:label"
	comments         []string // "issue:body"
}

func newReviewScopeGHServer() *reviewScopeGHServer {
	return &reviewScopeGHServer{
		issues:  map[int]*github.Issue{},
		prFiles: map[int][]*github.PRFile{},
	}
}

func (s *reviewScopeGHServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/files"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/pulls/%d/files", &num)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(s.prFiles[num])
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && !strings.HasSuffix(r.URL.Path, "/labels"):
			var num int
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d", &num); err == nil {
				if issue, ok := s.issues[num]; ok {
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/labels", &num)
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				s.addLabelCalls = append(s.addLabelCalls, fmt.Sprintf("%d:%s", num, l))
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.Split(r.URL.Path, "/")
			label := parts[len(parts)-1]
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/labels/", &num)
			s.removeLabelCalls = append(s.removeLabelCalls, fmt.Sprintf("%d:%s", num, label))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var num int
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/comments", &num)
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.comments = append(s.comments, fmt.Sprintf("%d:%s", num, body.Body))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Comment{ID: 1, Body: body.Body})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}
}

func (s *reviewScopeGHServer) hasAddLabel(issue int, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := fmt.Sprintf("%d:%s", issue, label)
	for _, c := range s.addLabelCalls {
		if c == want {
			return true
		}
	}
	return false
}

func (s *reviewScopeGHServer) hasRemoveLabel(issue int, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := fmt.Sprintf("%d:%s", issue, label)
	for _, c := range s.removeLabelCalls {
		if c == want {
			return true
		}
	}
	return false
}

// TestController_VerifyFixPRDeliversSourceScope_ReviewRevision mirrors
// gh5348_scope_mismatch_test.go's table for a review-revision fix issue
// (identified by "**Failure Type**: review_requested" in the body, written by
// CreateReviewIssue) instead of a CI-fix issue. Unlike the CI path — where
// pilot-superseded is already applied at spawn time and this function only
// corrects a mismatch — spawnReviewIssue (GH-5362) never applies the label at
// spawn time, so this function is the only place that ever does.
func TestController_VerifyFixPRDeliversSourceScope_ReviewRevision(t *testing.T) {
	reviewFixIssueBody := "Revision needed.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->\n\n## Context\n- **Failure Type**: review_requested\n"

	tests := []struct {
		name                string
		sourceIssue         *github.Issue
		fixPRFiles          []*github.PRFile
		originPRFiles       []*github.PRFile
		wantSuperseded      bool
		wantEscalate        bool
		wantLabelsUntouched bool
	}{
		{
			name: "shared file — source marked superseded for the first time",
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
			},
			fixPRFiles:     []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			originPRFiles:  []*github.PRFile{{Filename: "internal/foo/bar.go"}, {Filename: "internal/foo/baz.go"}},
			wantSuperseded: true,
		},
		{
			name: "zero file overlap — source escalated to needs-human, never superseded",
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
			},
			fixPRFiles:    []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles: []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantEscalate:  true,
		},
		{
			name: "zero overlap but source already closed — no reaction",
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateClosed,
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantLabelsUntouched: true,
		},
		{
			name: "source already superseded (idempotent re-run) — no reaction",
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelSuperseded}},
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       []*github.PRFile{{Filename: "internal/foo/bar.go"}},
			wantLabelsUntouched: true,
		},
		{
			name: "origin PR has no recorded files — skip rather than false-positive escalate",
			sourceIssue: &github.Issue{
				Number: 100, State: github.StateOpen,
			},
			fixPRFiles:          []*github.PRFile{{Filename: "internal/unrelated/thing.go"}},
			originPRFiles:       nil,
			wantLabelsUntouched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newReviewScopeGHServer()
			srv.issues[200] = &github.Issue{Number: 200, State: github.StateClosed, Body: reviewFixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}}
			srv.issues[100] = tt.sourceIssue
			srv.prFiles[55] = tt.fixPRFiles   // fix (revision) PR number
			srv.prFiles[7] = tt.originPRFiles // origin PR number, from "pr:7" in reviewFixIssueBody
			ts := httptest.NewServer(srv.handler())
			defer ts.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			prState := &PRState{PRNumber: 55, IssueNumber: 200}
			c.verifyFixPRDeliversSourceScope(context.Background(), prState)

			if tt.wantSuperseded {
				if !srv.hasAddLabel(100, github.LabelSuperseded) {
					t.Errorf("expected %s to be added to source #100, calls=%v", github.LabelSuperseded, srv.addLabelCalls)
				}
				if srv.hasAddLabel(100, labelNeedsHuman) {
					t.Errorf("did not expect %s on source #100, calls=%v", labelNeedsHuman, srv.addLabelCalls)
				}
				// GH-5375: pilot-superseded and pilot/pilot-in-progress must
				// never coexist on the source issue (GH-5298 invariant) — the
				// review-revision kind used to only add pilot-superseded and
				// leave the active labels standing on an OPEN issue.
				if !srv.hasRemoveLabel(100, github.LabelPilot) {
					t.Errorf("expected %s to be removed from source #100, calls=%v", github.LabelPilot, srv.removeLabelCalls)
				}
				if !srv.hasRemoveLabel(100, github.LabelInProgress) {
					t.Errorf("expected %s to be removed from source #100, calls=%v", github.LabelInProgress, srv.removeLabelCalls)
				}
			}
			if tt.wantEscalate {
				if !srv.hasAddLabel(100, labelNeedsHuman) {
					t.Errorf("expected %s to be added to source #100, calls=%v", labelNeedsHuman, srv.addLabelCalls)
				}
				if srv.hasAddLabel(100, github.LabelSuperseded) {
					t.Errorf("did not expect %s on source #100 — it must never have been applied in the first place, calls=%v", github.LabelSuperseded, srv.addLabelCalls)
				}
				// GH-5375: the zero-overlap/escalate branch must stay
				// unchanged — no pilot/pilot-in-progress removal, since the
				// source issue isn't being superseded.
				if srv.hasRemoveLabel(100, github.LabelPilot) {
					t.Errorf("did not expect %s to be removed from source #100 on escalate, calls=%v", github.LabelPilot, srv.removeLabelCalls)
				}
				if srv.hasRemoveLabel(100, github.LabelInProgress) {
					t.Errorf("did not expect %s to be removed from source #100 on escalate, calls=%v", github.LabelInProgress, srv.removeLabelCalls)
				}
				if len(sink.events) != 1 {
					t.Errorf("expected exactly 1 alert, got %d", len(sink.events))
				}
				foundComment := false
				for _, c := range srv.comments {
					if strings.HasPrefix(c, "100:") && strings.Contains(c, "Delivery mismatch") {
						foundComment = true
					}
				}
				if !foundComment {
					t.Errorf("expected a delivery-mismatch comment on source #100, got %v", srv.comments)
				}
			}
			if tt.wantLabelsUntouched {
				if len(srv.addLabelCalls) != 0 {
					t.Errorf("expected no label writes, got %v", srv.addLabelCalls)
				}
				if len(srv.removeLabelCalls) != 0 {
					t.Errorf("expected no label removals, got %v", srv.removeLabelCalls)
				}
				if len(sink.events) != 0 {
					t.Errorf("expected no alerts, got %d", len(sink.events))
				}
			}
		})
	}
}

// TestHandleReviewRequested_OwnCloseNotReadAsExternal covers the
// controller.go half of GH-5362 end-to-end: handleReviewRequested's own PR
// close (after successfully spawning a revision issue) must stamp a
// self-close marker BEFORE closing the PR, and must leave the branch alone.
// Without this, the very next poll's checkExternalMergeOrClose reads the
// close as an external (human) rejection — re-arming the source issue
// pilot-retry-ready (a double-arm, since the revision issue already
// continues the work) and deleting the branch the revision issue's own run
// might still need to continue from (the exact #275 shape).
func TestHandleReviewRequested_OwnCloseNotReadAsExternal(t *testing.T) {
	var branchDeleted bool
	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/91/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/91/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 701}))
		case r.URL.Path == "/repos/owner/repo/pulls/91" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 91, State: "closed"}))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/40/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    91,
		IssueNumber: 40,
		BranchName:  "pilot/GH-40",
		Stage:       StageReviewRequested,
	}
	c.mu.Lock()
	c.activePRs[91] = prState
	c.mu.Unlock()

	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.TerminalLabel != "" {
		t.Fatalf("TerminalLabel = %q, want empty (GH-5362: no longer set eagerly at spawn time)", prState.TerminalLabel)
	}

	// The next poll tick's external-close check must NOT treat this as an
	// external (human) close: the self-close marker stamped above must be
	// consumed (one-shot), routing into removePRTracking(pr, false) instead
	// of notifyExternalClose's re-arm + finalizeExternalClose's branch
	// deletion. checkExternalMergeOrClose still reports true either way (the
	// PR is resolved and processing should stop) — what distinguishes the
	// self-close path is the side effects it must NOT trigger, checked below.
	ghPR := &github.PullRequest{Number: 91, State: "closed", Merged: false}
	if resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR); !resolved {
		t.Error("expected checkExternalMergeOrClose to report the PR as resolved")
	}
	if branchDeleted {
		t.Error("branch must NOT be deleted — it must survive the own close so the revision issue can continue from these commits (GH-5362)")
	}
	for _, l := range issueLabelsAdded {
		if l == github.LabelRetryReady {
			t.Errorf("source issue must NOT be re-armed pilot-retry-ready on the own close — the revision issue (#701) already continues this work, labels added: %v", issueLabelsAdded)
		}
	}
}
