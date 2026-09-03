package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestShouldStripPilotAfterTerminalDrops_Table is the GH-5300 table-driven
// acceptance test for the strip decision: a claim_lost/already-terminal drop
// that keeps recurring (claimLostDrops >= terminalDropPilotStripThreshold) on
// an open, still pilot-labeled issue must trigger the strip-and-comment path
// instead of the GH-4961 unwind-and-wait correction — which can never
// resolve a wedge the poller and dispatcher keep reproducing every tick (see
// #5276: 9 label cycles and 3 duplicate "started working" comments inside an
// hour). Below the threshold, or once the pilot label is already gone (a
// prior tick already stripped it, or the issue closed), the strip must not
// re-fire — it is a one-shot action even though claimLostDrops keeps
// climbing on every subsequent dropped tick.
func TestShouldStripPilotAfterTerminalDrops_Table(t *testing.T) {
	tests := []struct {
		name           string
		claimLostDrops int
		issueState     string
		labels         []string
		pilotLabel     string
		want           bool
	}{
		{
			name:           "below threshold — never strip",
			claimLostDrops: terminalDropPilotStripThreshold - 1,
			issueState:     "open",
			labels:         []string{"pilot"},
			pilotLabel:     "pilot",
			want:           false,
		},
		{
			name:           "at threshold, open, still labeled — strip",
			claimLostDrops: terminalDropPilotStripThreshold,
			issueState:     "open",
			labels:         []string{"pilot"},
			pilotLabel:     "pilot",
			want:           true,
		},
		{
			name:           "well past threshold, open, still labeled — strip",
			claimLostDrops: terminalDropPilotStripThreshold + 6,
			issueState:     "open",
			labels:         []string{"pilot"},
			pilotLabel:     "pilot",
			want:           true,
		},
		{
			name:           "past threshold but issue closed — must not strip",
			claimLostDrops: terminalDropPilotStripThreshold + 1,
			issueState:     githubSDK.StateClosed,
			labels:         []string{"pilot"},
			pilotLabel:     "pilot",
			want:           false,
		},
		{
			name:           "past threshold but pilot label already gone — one-shot, must not re-fire",
			claimLostDrops: terminalDropPilotStripThreshold + 3,
			issueState:     "open",
			labels:         []string{"pilot-in-progress"},
			pilotLabel:     "pilot",
			want:           false,
		},
		{
			name:           "pilotLabel unresolved — must not strip",
			claimLostDrops: terminalDropPilotStripThreshold + 1,
			issueState:     "open",
			labels:         []string{"pilot"},
			pilotLabel:     "",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStripPilotAfterTerminalDrops(tt.claimLostDrops, tt.issueState, tt.labels, tt.pilotLabel); got != tt.want {
				t.Errorf("shouldStripPilotAfterTerminalDrops(%d, %q, %v, %q) = %v, want %v",
					tt.claimLostDrops, tt.issueState, tt.labels, tt.pilotLabel, got, tt.want)
			}
		})
	}
}

// TestStripPilotLabelAndCommentSDK_GH5300 is the behavioral counterpart:
// stripPilotLabelAndCommentSDK must remove exactly the pilot trigger label
// (never pilot-in-progress) and post exactly one explanatory comment.
func TestStripPilotLabelAndCommentSDK_GH5300(t *testing.T) {
	var mu sync.Mutex
	var removed []string
	var comments []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.SplitN(r.URL.Path, "/labels/", 2)
			mu.Lock()
			removed = append(removed, parts[1])
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			mu.Lock()
			comments = append(comments, "posted")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	if err := stripPilotLabelAndCommentSDK(context.Background(), client, "owner", "repo", 5300, "pilot", 5); err != nil {
		t.Fatalf("stripPilotLabelAndCommentSDK returned unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(removed) != 1 || removed[0] != "pilot" {
		t.Errorf("expected exactly one removed label %q, got %v", "pilot", removed)
	}
	if len(comments) != 1 {
		t.Errorf("expected exactly one comment posted, got %d", len(comments))
	}
}

// TestStripPilotLabelAndCommentSDK_LabelFailureSurfaces confirms a failed
// label removal is surfaced as an error (not swallowed), and — since the
// comment is meant to explain the strip — no comment is posted if the strip
// itself never happened.
func TestStripPilotLabelAndCommentSDK_LabelFailureSurfaces(t *testing.T) {
	var mu sync.Mutex
	var comments []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"injected label failure"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			mu.Lock()
			comments = append(comments, "posted")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	err := stripPilotLabelAndCommentSDK(context.Background(), client, "owner", "repo", 5300, "pilot", 5)
	if err == nil {
		t.Fatal("expected an error when label removal fails")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(comments) != 0 {
		t.Errorf("expected no comment posted when the label removal itself failed, got %d", len(comments))
	}
}

// TestGithubHandlerSDK_TerminalDropStripWired is a source-level guard (mirrors
// the established pattern for this otherwise-unexercisable function): once
// handleGithubIssueEventSDK decides to unwind (shouldUnwindGithubInProgressLabel
// is true), it must consult shouldStripPilotAfterTerminalDrops using the
// current claimLostDrops read from repickBackoff.gateDetail, and dispatch to
// stripPilotLabelAndCommentSDK when true, falling back to the existing
// unwindGithubStartedLabel path otherwise — so a task that's been dropped
// past the threshold gets the pilot label stripped instead of being endlessly
// re-offered and re-unwound.
func TestGithubHandlerSDK_TerminalDropStripWired(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	unwindGateIdx := strings.Index(body, "shouldUnwindGithubInProgressLabel(")
	stripDecisionIdx := strings.Index(body, "shouldStripPilotAfterTerminalDrops(")
	stripCallIdx := strings.Index(body, "stripPilotLabelAndCommentSDK(")
	unwindCallIdx := strings.LastIndex(body, "unwindGithubStartedLabel(ctx, specClient, repoOwner, repoName, issueNum)")

	if unwindGateIdx < 0 || stripDecisionIdx < 0 || stripCallIdx < 0 || unwindCallIdx < 0 {
		t.Fatal("expected shouldUnwindGithubInProgressLabel, shouldStripPilotAfterTerminalDrops, stripPilotLabelAndCommentSDK, and unwindGithubStartedLabel all present")
	}

	if unwindGateIdx >= stripDecisionIdx || stripDecisionIdx >= stripCallIdx || stripCallIdx >= unwindCallIdx {
		t.Error("expected shouldUnwindGithubInProgressLabel, then shouldStripPilotAfterTerminalDrops, then stripPilotLabelAndCommentSDK, then the unwindGithubStartedLabel fallback, in that order")
	}

	if !strings.Contains(body, "repickBackoff.gateDetail(backoffKey)") {
		t.Error("expected the strip decision to read the current claimLostDrops via repickBackoff.gateDetail(backoffKey)")
	}
}
