package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func preReviewBody(verdict, fix string) string {
	return "## Summary\n\nx\n\n## Pre-PR review\n\nMode: shadow\nVerdict: " + verdict + "\nFix: " + fix + "\n\nDefects:\n- [high] at a.go:1 — broken\n"
}

func TestPreReviewReason_RCBlocksMergeOnlyWhenRC(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantHold bool
	}{
		{"request-changes with no fix holds", preReviewBody("REQUEST-CHANGES", "none"), true},
		{"request-changes with unknown fix value holds", preReviewBody("REQUEST-CHANGES", "failed"), true},
		{"request-changes with empty fix holds", preReviewBody("REQUEST-CHANGES", ""), true},
		{"approve does not hold", preReviewBody("APPROVE", "none"), false},
		{"approve-with-notes does not hold", preReviewBody("APPROVE-WITH-NOTES", "none"), false},
		{"missing section does not hold", "## Summary\n\nno review here\n", false},
		{"empty body does not hold", "", false},
		{"unparseable verdict fails open", preReviewBody("MAYBE", "none"), false},
		{"section without mode fails open", "## Pre-PR review\n\nVerdict: REQUEST-CHANGES\nFix: none\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreReviewReason(nil, tt.body)
			if tt.wantHold {
				if !strings.HasPrefix(got, "Pre-PR review requested changes:") {
					t.Errorf("PreReviewReason = %q, want prefix %q", got, "Pre-PR review requested changes:")
				}
			} else if got != "" {
				t.Errorf("PreReviewReason = %q, want empty", got)
			}
		})
	}
}

func TestPreReviewReason_FixedDoesNotHold(t *testing.T) {
	for _, fix := range []string{"fixed", "Fixed", "  fixed  ", "fixed (1 round)"} {
		t.Run(fix, func(t *testing.T) {
			if got := PreReviewReason(nil, preReviewBody("REQUEST-CHANGES", fix)); got != "" {
				t.Errorf("PreReviewReason with Fix %q = %q, want empty", fix, got)
			}
		})
	}
}

// preReviewServer serves PR 5561 like liveSmokeServer, but can fail the PR GET.
func preReviewServer(body string, failGetPR bool, mergeAttempted *atomic.Bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5561" && r.Method == http.MethodGet:
			if failGetPR {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 5561,
				Head:   github.PRRef{SHA: "sha5561"},
				Base:   github.PRRef{Ref: "main"},
				Body:   body,
			})
		case r.URL.Path == "/repos/owner/repo/pulls/5561/files", r.URL.Path == "/repos/owner/repo/pulls/5561/reviews":
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/pulls/5561/merge" && r.Method == http.MethodPut:
			mergeAttempted.Store(true)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

func TestHandleCIPassed_PreReview(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		failGetPR  bool
		wantStage  PRStage
		wantReason bool
	}{
		{"request-changes with no fix awaits approval", preReviewBody("REQUEST-CHANGES", "none"), false, StageAwaitApproval, true},
		{"request-changes with fix fixed merges on green", preReviewBody("REQUEST-CHANGES", "fixed"), false, StageMerging, false},
		{"approve-with-notes merges on green", preReviewBody("APPROVE-WITH-NOTES", "none"), false, StageMerging, false},
		{"no section merges on green", "## Summary\n\nx\n", false, StageMerging, false},
		{"GetPullRequest error does not escalate", "", true, StageMerging, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mergeAttempted atomic.Bool
			server := preReviewServer(tt.body, tt.failGetPR, &mergeAttempted)
			defer server.Close()
			c := newLiveSmokeController(server)

			prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
			if err := c.handleCIPassed(context.Background(), prState); err != nil {
				t.Fatalf("handleCIPassed: %v", err)
			}
			if prState.Stage != tt.wantStage {
				t.Errorf("Stage = %s, want %s", prState.Stage, tt.wantStage)
			}
			if tt.wantReason {
				if !strings.HasPrefix(prState.EscalationReason, "Pre-PR review requested changes") {
					t.Errorf("EscalationReason = %q, want Pre-PR review prefix", prState.EscalationReason)
				}
			} else if prState.EscalationReason != "" {
				t.Errorf("EscalationReason = %q, want empty", prState.EscalationReason)
			}
			if mergeAttempted.Load() {
				t.Error("handleCIPassed must not merge")
			}
		})
	}
}
