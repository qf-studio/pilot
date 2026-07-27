package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestGH4570_ClosedReadWithinGraceWindow_NotYetConfirmed covers the core
// GH-4570 incident: a PR that entered tracking moments ago (well inside
// externalCloseGraceWindow) reads as "closed" exactly once. That single read
// must not be believed — no label change, no branch delete, no drop from
// tracking — because GitHub's eventual consistency can surface a stale
// "closed" read on a PR that is genuinely still open (the 2026-07-27
// incident: PR #196 read closed 29s after creation while open the entire
// time).
func TestGH4570_ClosedReadWithinGraceWindow_NotYetConfirmed(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    50,
		IssueNumber: 15,
		BranchName:  "pilot/GH-15",
		CreatedAt:   time.Now(), // fresh — inside externalCloseGraceWindow
	}
	c.mu.Lock()
	c.activePRs[50] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 50, State: "closed", Merged: false}
	resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)

	if resolved {
		t.Fatal("a single closed read within the grace window must not resolve the PR")
	}
	if _, ok := c.GetPRState(50); !ok {
		t.Error("PR must remain tracked after an unconfirmed closed read")
	}
	if prState.ClosedReadCount != 1 {
		t.Errorf("ClosedReadCount = %d, want 1", prState.ClosedReadCount)
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/15/labels"); n != 0 {
		t.Errorf("must not relabel the issue on an unconfirmed closed read, got %d calls", n)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 0 {
		t.Errorf("must not delete the branch on an unconfirmed closed read, got %d calls", n)
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/50/comments"); n != 0 {
		t.Errorf("must not post the external-close PR comment on an unconfirmed closed read, got %d calls", n)
	}
}

// TestGH4570_ClosedReadConfirmedAfterThreshold covers the "current behavior"
// half of the acceptance criteria: once a PR still inside the grace window
// has been read closed externalCloseConfirmThreshold times in a row, it must
// be treated exactly as before this fix — removed from tracking, relabeled,
// and its branch deleted.
func TestGH4570_ClosedReadConfirmedAfterThreshold(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    51,
		IssueNumber: 16,
		BranchName:  "pilot/GH-16",
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[51] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 51, State: "closed", Merged: false}

	var resolved bool
	for i := 1; i <= externalCloseConfirmThreshold; i++ {
		resolved = c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
		if i < externalCloseConfirmThreshold {
			if resolved {
				t.Fatalf("read %d/%d must not yet resolve the PR", i, externalCloseConfirmThreshold)
			}
			if _, ok := c.GetPRState(51); !ok {
				t.Fatalf("PR must remain tracked before the confirm threshold is reached (read %d)", i)
			}
		}
	}

	if !resolved {
		t.Fatal("the confirming read must resolve the PR as closed")
	}
	if _, ok := c.GetPRState(51); ok {
		t.Error("PR must no longer be tracked once the closed read is confirmed")
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/16/labels"); n != 1 {
		t.Errorf("AddLabels calls = %d, want 1 once confirmed", n)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 1 {
		t.Errorf("DELETE branch calls = %d, want 1 once confirmed", n)
	}
}

// TestGH4570_FinalizeExternalClose_ReReadsBeforeDelete directly covers the
// acceptance criterion "branch deletion path re-reads PR state first; open ->
// abort with warning". finalizeExternalClose is the sole remaining path that
// deletes a branch after an external close is confirmed — it must take one
// more fresh read immediately before the irreversible delete and bail out if
// that read now says the PR is open.
func TestGH4570_FinalizeExternalClose_ReReadsBeforeDelete(t *testing.T) {
	tests := []struct {
		name       string
		freshState string
		wantDelete bool
	}{
		{name: "fresh read still closed -> deletes", freshState: "closed", wantDelete: true},
		{name: "fresh read now open -> aborts delete", freshState: "open", wantDelete: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deleteCalled bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/70":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"number":70,"state":"` + tt.freshState + `"}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/git/refs/heads/pilot/GH-70":
					deleteCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer srv.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			c.finalizeExternalClose(context.Background(), 70, "pilot/GH-70")

			if deleteCalled != tt.wantDelete {
				t.Errorf("branch delete called = %v, want %v", deleteCalled, tt.wantDelete)
			}
		})
	}
}

// TestGH4570_FlappingCloseOpen_NeverAccumulatesConfirmation is the incident
// replay: a mock that alternates open/closed reads (mirroring GitHub's
// eventual-consistency flapping observed in the GH-189 incident window) must
// never trigger the destructive close path, because any "open" read resets
// the consecutive-closed-read counter to zero.
func TestGH4570_FlappingCloseOpen_NeverAccumulatesConfirmation(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    52,
		IssueNumber: 17,
		BranchName:  "pilot/GH-17",
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[52] = prState
	c.mu.Unlock()

	// closed, closed, open, closed, closed, open, closed, closed, open —
	// never externalCloseConfirmThreshold (3) consecutive closed reads.
	states := []string{"closed", "closed", "open", "closed", "closed", "open", "closed", "closed", "open"}
	for i, state := range states {
		ghPR := &github.PullRequest{Number: 52, State: state, Merged: false}
		resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
		if resolved {
			t.Fatalf("flapping sequence must never resolve the PR (resolved at step %d, state=%s)", i, state)
		}
	}

	if _, ok := c.GetPRState(52); !ok {
		t.Error("PR must remain tracked throughout a flapping open/closed sequence")
	}
	if prState.ClosedReadCount != 0 {
		t.Errorf("ClosedReadCount = %d, want 0 after the sequence ends on an open read", prState.ClosedReadCount)
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/17/labels"); n != 0 {
		t.Errorf("must not relabel the issue during flapping, got %d calls", n)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/git/refs/heads/"); n != 0 {
		t.Errorf("must not delete the branch during flapping, got %d calls", n)
	}
}
