package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-6123: the GH-5351 delivery-mismatch guard must not fire for post-merge CI
// fixes (the fix PR touches CI config, never the original's files) or when the
// original PR already merged (its delivery is proven).
func TestVerifyFixPRDeliversSourceScope_PostMergeSkip(t *testing.T) {
	bodyFor := func(failureType string) string {
		return fmt.Sprintf("## Context\n\n- **Failure Type**: %s\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 source:100 -->", failureType)
	}

	tests := []struct {
		name         string
		failureType  string
		originMerged *bool // nil: leave the default "{}" reply
		originStatus int   // non-zero: GET /pulls/7 returns this status
		wantComment  string
		wantAlert    bool
		wantEscalate bool // GH-5362 needs-human label on the source
	}{
		{
			name:        "ci_post_merge fix, origin open — skipped (11-05 shape)",
			failureType: "ci_post_merge",
		},
		{
			name:         "ci_failure fix, origin merged — skipped",
			failureType:  "ci_failure",
			originMerged: boolPtr(true),
		},
		{
			name:         "ci_failure fix, origin open — guard still fires",
			failureType:  "ci_failure",
			originMerged: boolPtr(false),
			wantComment:  "GH-5351",
			wantAlert:    true,
		},
		{
			name:         "ci_failure fix, origin closed unmerged — guard still fires",
			failureType:  "ci_failure",
			originMerged: boolPtr(false),
			wantComment:  "GH-5351",
			wantAlert:    true,
		},
		{
			name:         "ci_failure fix, origin PR fetch fails — guard still fires",
			failureType:  "ci_failure",
			originStatus: http.StatusInternalServerError,
			wantComment:  "GH-5351",
			wantAlert:    true,
		},
		{
			name:         "review_requested fix, origin merged — GH-5362 escalation unchanged",
			failureType:  "review_requested",
			originMerged: boolPtr(true),
			wantComment:  "GH-5362",
			wantAlert:    true,
			wantEscalate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newScopeMismatchGHServer()
			srv.issues[200] = &github.Issue{Number: 200, State: github.StateClosed, Body: bodyFor(tt.failureType),
				Labels: []github.Label{{Name: github.LabelDone}}}
			srv.issues[100] = &github.Issue{Number: 100, State: github.StateOpen,
				Labels: []github.Label{{Name: github.LabelPilot}, {Name: github.LabelInProgress}}}
			srv.prFiles[55] = []*github.PRFile{{Filename: ".github/workflows/sync-docs.yml"}}
			srv.prFiles[7] = []*github.PRFile{{Filename: "internal/foo/bar.go"}}
			if tt.originMerged != nil {
				srv.prMerged[7] = *tt.originMerged
			}
			if tt.originStatus != 0 {
				srv.prStatus[7] = tt.originStatus
			}
			ts := httptest.NewServer(srv.handler())
			defer ts.Close()

			c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, ts.URL), nil, "owner", "repo")
			sink := &fakeAlertSink{}
			c.SetAlertsEngine(sink)

			c.verifyFixPRDeliversSourceScope(context.Background(), &PRState{PRNumber: 55, IssueNumber: 200})

			if srv.hasAddLabel(100, github.LabelSuperseded) {
				t.Errorf("source must never be marked superseded on zero overlap, calls=%v", srv.addLabelCalls)
			}
			if len(srv.removeLabelCalls) != 0 {
				t.Errorf("expected no label removals, got %v", srv.removeLabelCalls)
			}
			if got := srv.hasAddLabel(100, labelNeedsHuman); got != tt.wantEscalate {
				t.Errorf("%s added = %v, want %v (calls=%v)", labelNeedsHuman, got, tt.wantEscalate, srv.addLabelCalls)
			}
			if !tt.wantEscalate && len(srv.addLabelCalls) != 0 {
				t.Errorf("expected no label writes, got %v", srv.addLabelCalls)
			}
			if gotAlert := len(sink.events) == 1; gotAlert != tt.wantAlert || len(sink.events) > 1 {
				t.Errorf("alerts = %d, want alert=%v", len(sink.events), tt.wantAlert)
			}
			if tt.wantComment == "" {
				if len(srv.comments) != 0 {
					t.Errorf("expected no comments, got %v", srv.comments)
				}
				return
			}
			found := false
			for _, cm := range srv.comments {
				if strings.HasPrefix(cm, "100:") && strings.Contains(cm, "Delivery mismatch detected ("+tt.wantComment+")") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected %s delivery-mismatch comment on source #100, got %v", tt.wantComment, srv.comments)
			}
		})
	}
}

func TestIsCIPostMergeFixIssue(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"ci_post_merge", "## Context\n\n- **Failure Type**: ci_post_merge\n", true},
		{"ci_failure", "## Context\n\n- **Failure Type**: ci_failure\n", false},
		{"review_requested", "## Context\n\n- **Failure Type**: review_requested\n", false},
		{"empty body", "", false},
		{
			"later attacker-controlled line is ignored — first match wins",
			"## Context\n\n- **Failure Type**: ci_failure\n\nlog output:\n- **Failure Type**: ci_post_merge\n",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCIPostMergeFixIssue(tt.body); got != tt.want {
				t.Errorf("isCIPostMergeFixIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}
