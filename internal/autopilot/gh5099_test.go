package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5099 (GH-5088 subtask): notifyExternalClose's TerminalLabel-driven
// pilot-failed close must advance the shared pilot-failed-retry-N ladder
// (internal/retryladder, GH-5098) in the same label mutation, matching
// postTitleRejectionEscalation (title_rejection.go, GH-5077) and
// NotifyTaskFailed (adapters/github/notifier.go, GH-5100). Reuses the
// gh5042LabelServer httptest idiom (gh5042_test.go) that tracks an issue's
// live label set across GET/POST-labels/DELETE-labels calls.

// TestNotifyExternalClose_LadderAdvancesOnTerminalFailedClose covers the
// fresh-failure case: an issue with no prior ladder rung whose PR closes
// with TerminalLabel=pilot-failed must come out of notifyExternalClose
// carrying pilot-failed + pilot-failed-retry-1, applied in the very same
// mutation as the pilot-failed stamp.
func TestNotifyExternalClose_LadderAdvancesOnTerminalFailedClose(t *testing.T) {
	server, snapshot := gh5042LabelServer(t, 10, nil, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      42,
		IssueNumber:   10,
		TerminalLabel: github.LabelFailed,
	}
	c.notifyExternalClose(context.Background(), prState)

	got := snapshot()
	if !got[github.LabelFailed] {
		t.Errorf("expected %s applied, got=%v", github.LabelFailed, got)
	}
	if !got[github.LabelFailedRetry1] {
		t.Errorf("expected ladder to advance to %s in the same mutation, got=%v", github.LabelFailedRetry1, got)
	}
}

// TestNotifyExternalClose_LadderAdvancesPastExistingRung covers the
// repeated-failure case: an issue already sitting at pilot-failed-retry-1
// (from an earlier failure cycle, pilot-failed since cleared for the
// retry) whose PR closes again with TerminalLabel=pilot-failed must
// advance the rung to pilot-failed-retry-2 and shed retry-1, in the same
// mutation as the pilot-failed stamp.
func TestNotifyExternalClose_LadderAdvancesPastExistingRung(t *testing.T) {
	server, snapshot := gh5042LabelServer(t, 10, []string{github.LabelFailedRetry1}, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      42,
		IssueNumber:   10,
		TerminalLabel: github.LabelFailed,
	}
	c.notifyExternalClose(context.Background(), prState)

	got := snapshot()
	if !got[github.LabelFailed] {
		t.Errorf("expected %s applied, got=%v", github.LabelFailed, got)
	}
	if !got[github.LabelFailedRetry2] {
		t.Errorf("expected ladder to advance to %s, got=%v", github.LabelFailedRetry2, got)
	}
	if got[github.LabelFailedRetry1] {
		t.Errorf("expected superseded rung %s removed, got=%v", github.LabelFailedRetry1, got)
	}
}

// TestNotifyExternalClose_ExhaustionOutranksCloseSupersedesHold covers the
// GH-5099 precedence rule: "exhaustion outranks close-supersedes-hold".
// GH-5042 made notifyExternalClose's default retry-ready resolution strip
// pilot-needs-human unconditionally (a close always supersedes a hold) —
// but an issue already parked at pilot-failed-retry-exhausted
// (bda03368/GH-5079) has a spent retry budget, not merely a paused one. A
// stale PR (opened before that parking) closing without merge must leave
// the issue's labels untouched: pilot-needs-human stays, pilot-retry-ready
// is never added.
func TestNotifyExternalClose_ExhaustionOutranksCloseSupersedesHold(t *testing.T) {
	initial := []string{labelNeedsHuman, github.LabelFailedRetryExhausted}
	server, snapshot := gh5042LabelServer(t, 10, initial, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// No TerminalLabel set: this is the stale-PR-close path resolving to
	// the default pilot-retry-ready outcome, not a fresh terminal failure.
	prState := &PRState{PRNumber: 42, IssueNumber: 10, Error: "stale PR closed without merging"}
	c.notifyExternalClose(context.Background(), prState)

	got := snapshot()
	if !got[labelNeedsHuman] {
		t.Errorf("expected %s to remain (exhaustion outranks close-supersedes-hold), got=%v", labelNeedsHuman, got)
	}
	if got[github.LabelRetryReady] {
		t.Errorf("expected %s NOT added on an exhausted+parked issue, got=%v", github.LabelRetryReady, got)
	}
	if !got[github.LabelFailedRetryExhausted] {
		t.Errorf("expected %s to remain untouched, got=%v", github.LabelFailedRetryExhausted, got)
	}
}
