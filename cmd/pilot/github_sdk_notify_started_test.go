package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/testutil"
)

// notifyStartedFake is a minimal mock GitHub server covering the two calls
// NotifyTaskStarted makes: POST .../labels and POST .../comments. Either
// endpoint can be configured to fail, to exercise the non-fatal error path.
type notifyStartedFake struct {
	server        *httptest.Server
	mu            sync.Mutex
	labelsAdded   []string
	commentsAdded []string
	failLabels    bool
	failComments  bool
}

func newNotifyStartedFake() *notifyStartedFake {
	f := &notifyStartedFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			f.mu.Lock()
			fail := f.failLabels
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected label failure"}`))
				return
			}
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.labelsAdded = append(f.labelsAdded, body.Labels...)
			f.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			f.mu.Lock()
			fail := f.failComments
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"injected comment failure"}`))
				return
			}
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, body.Body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// TestApplyGithubInProgressLabelSDK is the GH-4687/GH-5300 acceptance test
// for the label-only half of the old combined notify call: the SDK-dispatch
// path's applyGithubInProgressLabelSDK helper (cmd/pilot/handlers.go) must
// apply pilot-in-progress on success, and must surface (not swallow) the
// underlying error on failure so the caller can log it as a non-fatal WARN.
// Before GH-4687 the SDK-poller chain performed zero label operations, which
// silently disabled recoverOrphanedIssues and the pilot-done label removal
// on merge.
func TestApplyGithubInProgressLabelSDK(t *testing.T) {
	tests := []struct {
		name       string
		failLabels bool
		wantErr    bool
		wantLabel  bool
	}{
		{
			name:      "success applies pilot-in-progress",
			wantErr:   false,
			wantLabel: true,
		},
		{
			name:       "label failure surfaces as an error, not a panic",
			failLabels: true,
			wantErr:    true,
			wantLabel:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedFake()
			defer f.server.Close()
			f.failLabels = tt.failLabels

			client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

			err := applyGithubInProgressLabelSDK(context.Background(), client, "o", "r", 7)
			if (err != nil) != tt.wantErr {
				t.Fatalf("applyGithubInProgressLabelSDK() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			gotLabel := false
			for _, l := range f.labelsAdded {
				if l == githubSDK.LabelInProgress {
					gotLabel = true
				}
			}
			commentCount := len(f.commentsAdded)
			f.mu.Unlock()

			if gotLabel != tt.wantLabel {
				t.Errorf("pilot-in-progress applied = %v, want %v (labelsAdded = %v)", gotLabel, tt.wantLabel, f.labelsAdded)
			}
			if commentCount != 0 {
				t.Errorf("applyGithubInProgressLabelSDK must never post a comment, got %d", commentCount)
			}
		})
	}
}

// TestPostGithubTaskStartedCommentSDK covers the comment-only half split out
// by GH-5300: postGithubTaskStartedCommentSDK must post exactly the
// task-started comment on success and surface the underlying error on
// failure, without touching labels at all.
func TestPostGithubTaskStartedCommentSDK(t *testing.T) {
	tests := []struct {
		name         string
		failComments bool
		wantErr      bool
		wantComment  bool
	}{
		{
			name:        "success posts the started-working comment",
			wantComment: true,
		},
		{
			name:         "comment failure surfaces as an error",
			failComments: true,
			wantErr:      true,
			wantComment:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotifyStartedFake()
			defer f.server.Close()
			f.failComments = tt.failComments

			client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

			err := postGithubTaskStartedCommentSDK(context.Background(), client, "o", "r", 7, "GH-5300")
			if (err != nil) != tt.wantErr {
				t.Fatalf("postGithubTaskStartedCommentSDK() error = %v, wantErr %v", err, tt.wantErr)
			}

			f.mu.Lock()
			defer f.mu.Unlock()

			if got := len(f.commentsAdded) > 0; got != tt.wantComment {
				t.Errorf("comment posted = %v, want %v (commentsAdded = %v)", got, tt.wantComment, f.commentsAdded)
			}
			if len(f.labelsAdded) != 0 {
				t.Errorf("postGithubTaskStartedCommentSDK must never touch labels, got %v", f.labelsAdded)
			}
		})
	}
}

// TestGithubHandlerSDK_NotifyTaskStartedWired is a source-level guard proving
// handleGithubIssueEventSDK actually calls applyGithubInProgressLabelSDK on
// the dispatch path (before handleIssueGeneric), and that a labeling failure
// is only logged (WARN) rather than aborting dispatch — mirroring the
// established source-inspection pattern for this otherwise-unexercisable
// function (see TestGithubHandlerSDK_SpecGuardWired in spec_guard_sdk_test.go).
// GH-5300 also requires postGithubTaskStartedCommentSDK to be wired into the
// OnClaimed hook, so a dropped pickup never posts the comment.
func TestGithubHandlerSDK_NotifyTaskStartedWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	if !strings.Contains(body, "applyGithubInProgressLabelSDK(") {
		t.Error("handleGithubIssueEventSDK must call applyGithubInProgressLabelSDK to apply pilot-in-progress on the SDK-dispatch path (GH-4687)")
	}

	notifyIdx := strings.Index(body, "applyGithubInProgressLabelSDK(")
	handleIssueGenericIdx := strings.Index(body, "handleIssueGeneric(ctx, deps, info, task)")
	if notifyIdx < 0 || handleIssueGenericIdx < 0 || notifyIdx >= handleIssueGenericIdx {
		t.Error("applyGithubInProgressLabelSDK must be called before handleIssueGeneric so pilot-in-progress is applied at the start of work")
	}

	// The error must be logged, not propagated/returned — labeling failure
	// must never block dispatch (mirrors pilot.go:1191-1195 / controller.go:3011-3015).
	if !strings.Contains(body, `logging.WithComponent("github").Warn("Failed to apply pilot-in-progress label (SDK path)"`) {
		t.Error("applyGithubInProgressLabelSDK errors must be logged as a non-fatal WARN, not propagated")
	}

	// GH-5300: the "started working" comment must only post from the
	// OnClaimed hook, which fires after the dispatch claim is won — never
	// unconditionally before the dispatch attempt like the old combined call.
	if !strings.Contains(body, "OnClaimed: func()") {
		t.Fatal("handleGithubIssueEventSDK must wire HandlerDeps.OnClaimed so the started-working comment posts only after a claim is won (GH-5300)")
	}
	onClaimedIdx := strings.Index(body, "OnClaimed: func()")
	if !strings.Contains(body[onClaimedIdx:], "postGithubTaskStartedCommentSDK(") {
		t.Error("OnClaimed must call postGithubTaskStartedCommentSDK (GH-5300)")
	}
	if strings.Contains(body[:onClaimedIdx], "postGithubTaskStartedCommentSDK(") {
		t.Error("postGithubTaskStartedCommentSDK must only be called from within OnClaimed, not eagerly before the dispatch attempt")
	}
}

// TestGithubHandlerSDK_NotifyTaskStartedGatedOnIssueState is a source-level
// guard (GH-4817, TASK-459 Phase 3 Task 5b): the applyGithubInProgressLabelSDK
// call must be gated on issueState != githubSDK.StateClosed, so a closed
// issue never gets a stranded pilot-in-progress label/comment. issueState
// defaults to "" (unknown) on fetch failure or when specClient is nil (see
// the fetchGithubIssueForSDKTask comment above), so the comparison must be
// against the positive "closed" value only — never against issueState == ""
// — to keep the existing fail-open behavior for unresolvable state.
func TestGithubHandlerSDK_NotifyTaskStartedGatedOnIssueState(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	notifyIdx := strings.Index(body, "applyGithubInProgressLabelSDK(")
	if notifyIdx < 0 {
		t.Fatal("handleGithubIssueEventSDK must call applyGithubInProgressLabelSDK")
	}

	// The nearest preceding "if" before the call must gate on issueState,
	// comparing against the closed constant (not equality with "").
	preamble := body[:notifyIdx]
	ifIdx := strings.LastIndex(preamble, "if specClient != nil")
	if ifIdx < 0 {
		t.Fatal("expected an `if specClient != nil` guard preceding the applyGithubInProgressLabelSDK call")
	}
	guardClause := preamble[ifIdx:]
	if !strings.Contains(guardClause, "issueState != githubSDK.StateClosed") {
		t.Error("applyGithubInProgressLabelSDK must only fire when issueState != githubSDK.StateClosed (fail-open on \"\"/unknown)")
	}

	if !strings.Contains(body, "skipping pilot-in-progress label and comment") {
		t.Error("expected an informational log line when the label/comment write is skipped for a closed issue")
	}
}
