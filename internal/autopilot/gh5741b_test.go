package autopilot

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestVerifyFixPRDeliversSourceScope_ClosedSource covers GH-5747: since
// GH-5741 the fix PR's "Closes #<source>" closes the original on merge, so
// verifyFixPRDeliversSourceScope sees a closed source. A closed source that
// lacks pilot-superseded must still get the label mutation on confirmed
// overlap; one that already carries it stays idempotent.
func TestVerifyFixPRDeliversSourceScope_ClosedSource(t *testing.T) {
	fixIssueBody := "Fixes CI failure.\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->"
	fixIssue := &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody, Labels: []github.Label{{Name: github.LabelDone}}}
	overlapFix := []*github.PRFile{{Filename: "internal/foo/bar.go"}}
	origin := []*github.PRFile{{Filename: "internal/foo/bar.go"}}

	tests := []struct {
		name      string
		source    *github.Issue
		wantAdd   bool
		wantStrip bool
	}{
		{
			name: "closed, no pilot-superseded, overlap — superseded added and trigger labels stripped",
			source: &github.Issue{Number: 100, State: github.StateClosed,
				Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}}},
			wantAdd: true, wantStrip: true,
		},
		{
			name: "closed with pilot-superseded — idempotent, no label call",
			source: &github.Issue{Number: 100, State: github.StateClosed,
				Labels: []github.Label{{Name: github.LabelSuperseded}}},
		},
		{
			name: "open, overlap — unchanged behaviour",
			source: &github.Issue{Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}}},
			wantAdd: true, wantStrip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newScopeMismatchGHServer()
			srv.issues[200] = fixIssue
			srv.issues[100] = tt.source
			srv.prFiles[55] = overlapFix
			srv.prFiles[7] = origin
			ts := httptest.NewServer(srv.handler())
			defer ts.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
			c.verifyFixPRDeliversSourceScope(context.Background(), &PRState{PRNumber: 55, IssueNumber: 200})

			if got := srv.hasAddLabel(100, github.LabelSuperseded); got != tt.wantAdd {
				t.Errorf("%s added = %v, want %v (calls=%v)", github.LabelSuperseded, got, tt.wantAdd, srv.addLabelCalls)
			}
			for _, l := range []string{github.LabelPilot, github.LabelInProgress} {
				if got := srv.hasRemoveLabel(100, l); got != tt.wantStrip {
					t.Errorf("%s removed = %v, want %v (calls=%v)", l, got, tt.wantStrip, srv.removeLabelCalls)
				}
			}
			if !tt.wantAdd && (len(srv.addLabelCalls) != 0 || len(srv.removeLabelCalls) != 0) {
				t.Errorf("expected no label calls, got add=%v remove=%v", srv.addLabelCalls, srv.removeLabelCalls)
			}
		})
	}
}
