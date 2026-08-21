package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestNotifyExternalClose_LadderAdvancesOnTerminalClose is the GH-5099
// subtask (a) acceptance test: when notifyExternalClose resolves a close to
// pilot-failed (prState.TerminalLabel already says so — the "autopilot
// terminal close" path, e.g. handleCIFailed), the pilot-failed-retry-N
// ladder rung (internal/retryladder's shared Advance helper, GH-5098) must
// advance in the very same mutateIssueLabels call instead of a separate
// label write. Table-driven across every rung, reusing the gh5042LabelServer
// converged-label-set fixture (gh5042_test.go) so each case only has to
// state the issue's live labels before the close and assert the converged
// set after it.
func TestNotifyExternalClose_LadderAdvancesOnTerminalClose(t *testing.T) {
	tests := []struct {
		name    string
		initial []string
		want    []string
	}{
		{
			name:    "fresh failure - advances to retry-1",
			initial: []string{github.LabelInProgress},
			want:    []string{github.LabelFailed, github.LabelFailedRetry1},
		},
		{
			// GH-5078: the stale-label janitor clears pilot-failed (but not
			// the rung label) after its 24h threshold to re-admit the issue
			// for another attempt, so a second real failure sees pilot-failed
			// absent (a fresh application) with retry-1 still standing from
			// the first attempt.
			name:    "rung standing, pilot-failed cleared by janitor - advances to retry-2",
			initial: []string{github.LabelFailedRetry1},
			want:    []string{github.LabelFailed, github.LabelFailedRetry2},
		},
		{
			name:    "rung standing, pilot-failed cleared by janitor - advances to exhausted",
			initial: []string{github.LabelFailedRetry2},
			want:    []string{github.LabelFailed, github.LabelFailedRetryExhausted},
		},
		{
			name:    "already exhausted - stays exhausted, no further advance",
			initial: []string{github.LabelFailed, github.LabelFailedRetryExhausted},
			want:    []string{github.LabelFailed, github.LabelFailedRetryExhausted},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, snapshot := gh5042LabelServer(t, 10, tt.initial, "open")
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:      42,
				IssueNumber:   10,
				Stage:         StageFailed,
				Error:         "CI checks failed",
				TerminalLabel: github.LabelFailed,
			}
			c.notifyExternalClose(context.Background(), prState)

			assertLabelSet(t, snapshot(), tt.want)
		})
	}
}

// TestNotifyExternalClose_ExhaustionOutranksCloseSupersedesHold is the
// GH-5099 subtask (b) acceptance test: an issue already parked under
// pilot-needs-human because its pilot-failed-retry ladder reached
// pilot-failed-retry-exhausted (GH-5079) must stay parked when some other,
// stale PR for the same issue closes later. Before this fix, notifyExternalClose's
// "close always supersedes hold" rule (GH-5042) unconditionally stripped
// pilot-needs-human and re-armed pilot-retry-ready (+ pilot) on every close —
// which would silently un-park an exhausted, already-terminal issue.
func TestNotifyExternalClose_ExhaustionOutranksCloseSupersedesHold(t *testing.T) {
	initial := []string{labelNeedsHuman, github.LabelFailed, github.LabelFailedRetryExhausted}
	server, snapshot := gh5042LabelServer(t, 10, initial, "open")
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// No TerminalLabel set and no durable spawned-fix claim (stateStore is
	// nil) — this is a stale/unrelated PR closing, so notifyExternalClose
	// would otherwise resolve to the default pilot-retry-ready outcome.
	prState := &PRState{PRNumber: 99, IssueNumber: 10, Error: "closed without merging"}
	c.notifyExternalClose(context.Background(), prState)

	labels := snapshot()
	if !labels[labelNeedsHuman] {
		t.Errorf("expected pilot-needs-human to remain on an exhausted+parked issue, got labels=%v", labels)
	}
	if labels[github.LabelRetryReady] {
		t.Errorf("expected pilot-retry-ready NOT to be added to an exhausted+parked issue, got labels=%v", labels)
	}
	if labels[github.LabelPilot] {
		t.Errorf("expected pilot NOT to be added alongside the suppressed pilot-retry-ready, got labels=%v", labels)
	}
}
