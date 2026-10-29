package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
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

// bothHoldsBody carries a live-smoke Not-verified bullet AND an unfixed
// REQUEST-CHANGES pre-PR review section.
const bothHoldsBody = "## Summary\n\nx\n\n## Not verified\n\n- Live smoke on four labelled excerpts\n\n" +
	"## Pre-PR review\n\nMode: shadow\nVerdict: REQUEST-CHANGES\nFix: none\n\nDefects:\n- [high] at a.go:1 — broken\n"

// TestHandleCIPassed_PreReviewAndLiveSmoke covers GH-5967: with both holds on
// one PR the live-smoke segment stays last in EscalationReason, so the pre-PR
// text never parses as a live-smoke bullet.
func TestHandleCIPassed_PreReviewAndLiveSmoke(t *testing.T) {
	var mergeAttempted atomic.Bool
	server := preReviewServer(bothHoldsBody, false, &mergeAttempted)
	defer server.Close()
	c := newLiveSmokeController(server)

	prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Fatalf("Stage = %s, want %s", prState.Stage, StageAwaitApproval)
	}
	if !strings.Contains(prState.EscalationReason, preReviewReasonPrefix) {
		t.Errorf("EscalationReason = %q, want pre-PR review segment", prState.EscalationReason)
	}
	if !strings.Contains(prState.EscalationReason, liveSmokeReasonPrefix) {
		t.Errorf("EscalationReason = %q, want live-smoke segment", prState.EscalationReason)
	}
	bullets := liveSmokeBulletsFromReason(prState.EscalationReason)
	if len(bullets) != 1 || bullets[0] != "Live smoke on four labelled excerpts" {
		t.Errorf("liveSmokeBulletsFromReason = %q, want exactly the one smoke bullet", bullets)
	}
	for _, b := range bullets {
		if strings.Contains(b, "Pre-PR review") {
			t.Errorf("bullet %q leaked the pre-PR segment", b)
		}
	}
	if mergeAttempted.Load() {
		t.Error("handleCIPassed must not merge")
	}
}

// TestHandleCIPassed_PreReviewRecordsHoldEvent covers the pre_pr_review_hold
// execution event, which needs a real memory store (recordExecutionEvent is a
// no-op on nil).
func TestHandleCIPassed_PreReviewRecordsHoldEvent(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const projectPath = "/proj/pilot"
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-pr-5561", TaskID: "PR-5561", ProjectPath: projectPath, Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	var mergeAttempted atomic.Bool
	server := preReviewServer(preReviewBody("REQUEST-CHANGES", "none"), false, &mergeAttempted)
	defer server.Close()
	c := newLiveSmokeController(server)
	c.projectPath = projectPath
	c.SetMemoryStore(store)

	prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Fatalf("Stage = %s, want %s", prState.Stage, StageAwaitApproval)
	}

	events, err := store.ListExecutionEvents("exec-pr-5561")
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if !strings.HasPrefix(events[0].Detail, "pre_pr_review_hold:") {
		t.Errorf("event detail = %q, want prefix %q", events[0].Detail, "pre_pr_review_hold:")
	}
	if events[0].Stage != memory.StageAwaitingApproval {
		t.Errorf("event stage = %s, want %s", events[0].Stage, memory.StageAwaitingApproval)
	}
}

// TestLiveSmokeBulletsFromReason_StopsAtPreReviewSegment pins the step-2 cut: a
// pre-PR segment placed after the live-smoke one (the pre-GH-5967 order) must
// not leak into the bullets.
func TestLiveSmokeBulletsFromReason_StopsAtPreReviewSegment(t *testing.T) {
	preReason := preReviewReasonPrefix + " verdict REQUEST-CHANGES, fix outcome \"none\" (review mode shadow)"
	tests := []struct {
		name   string
		reason string
		want   []string
	}{
		{"live-smoke last", preReason + "; " + liveSmokeReasonPrefix + "a; b", []string{"a", "b"}},
		{"pre-PR after live-smoke", liveSmokeReasonPrefix + "a; b; " + preReason, []string{"a", "b"}},
		{"live-smoke only", liveSmokeReasonPrefix + "a", []string{"a"}},
		{"no live-smoke segment", preReason, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := liveSmokeBulletsFromReason(tt.reason)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") || (got == nil) != (tt.want == nil) {
				t.Errorf("liveSmokeBulletsFromReason = %q, want %q", got, tt.want)
			}
		})
	}
}
