package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5115: broadens GH-5099's "exhaustion outranks close-supersedes-hold"
// rule (gh5099_test.go's TestNotifyExternalClose_ExhaustionOutranksCloseSupersedesHold)
// from the default pilot-retry-ready resolution to every close resolution
// notifyExternalClose can reach, including a TerminalLabel-driven close
// (pilot-failed/pilot-superseded/etc). An issue already parked at
// pilot-failed-retry-exhausted must keep pilot-needs-human standing no
// matter which resolution this particular stale PR's close maps to — only
// pilot-in-progress still clears, so the issue doesn't render as stuck
// mid-execution. Reuses the gh5042LabelServer httptest idiom (gh5042_test.go).
func TestNotifyExternalClose_ExhaustionOutranksTerminalLabelClose(t *testing.T) {
	tests := []struct {
		name          string
		terminalLabel string
	}{
		{name: "failed resolution", terminalLabel: github.LabelFailed},
		{name: "superseded resolution", terminalLabel: github.LabelSuperseded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initial := []string{labelNeedsHuman, github.LabelFailedRetryExhausted, github.LabelInProgress}
			server, snapshot := gh5042LabelServer(t, 10, initial, "open")
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:      42,
				IssueNumber:   10,
				TerminalLabel: tt.terminalLabel,
			}
			c.notifyExternalClose(context.Background(), prState)

			got := snapshot()
			if !got[labelNeedsHuman] {
				t.Errorf("expected %s to remain (exhaustion outranks close-supersedes-hold), got=%v", labelNeedsHuman, got)
			}
			if got[github.LabelInProgress] {
				t.Errorf("expected %s cleared even though %s was suppressed, got=%v", github.LabelInProgress, labelNeedsHuman, got)
			}
			if !got[github.LabelFailedRetryExhausted] {
				t.Errorf("expected %s rung label to remain untouched, got=%v", github.LabelFailedRetryExhausted, got)
			}
		})
	}
}
