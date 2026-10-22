package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// gh5801Env is a mock GitHub for the size-guard same-SHA green redrive: the
// check-run conclusion is switchable, PR 426 always reports 500 production
// additions (over the guard), and PR comments / label deletions are recorded.
type gh5801Env struct {
	mu         sync.Mutex
	conclusion string
	comments   []string
	removed    []string
	srv        *httptest.Server
	c          *Controller
}

func newGH5801Env(t *testing.T) *gh5801Env {
	t.Helper()
	e := &gh5801Env{conclusion: "failure"}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":200,"name":"test","status":"completed","conclusion":"` + e.conclusion + `"}]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/actions/jobs/200/logs"):
			_, _ = w.Write([]byte("--- FAIL: TestFlaky\nassertion failed: expected true, got false"))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/426/files":
			_, _ = w.Write([]byte(`[{"filename":"internal/fleet/fleet.go","status":"added","additions":500}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/420":
			_, _ = w.Write([]byte(`{"number":420}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/426/comments":
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			e.comments = append(e.comments, string(buf))
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/420/labels/"):
			e.removed = append(e.removed, strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/420/labels/"))
			_, _ = w.Write([]byte("{}"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(e.srv.Close)

	cfg := DefaultConfig()
	cfg.MaxCIFixPRSize = 300
	cfg.MaxCIFixIterations = 5
	cfg.ExecutionMode = "auto"
	e.c = NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, e.srv.URL), nil, "owner", "repo")
	return e
}

func (e *gh5801Env) setConclusion(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.conclusion = s
}

func (e *gh5801Env) commentCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.comments)
}

func heldPR(sha, reason string) *PRState {
	return &PRState{
		PRNumber:         426,
		IssueNumber:      420,
		BranchName:       "pilot/GH-420",
		HeadSHA:          sha,
		Stage:            StageFailed,
		Error:            reason,
		HoldActive:       true,
		HoldHeadSHA:      sha,
		HoldLabels:       labelNeedsHuman,
		HoldRedriveCount: 1,
		MergeAttempts:    2,
	}
}

func gh5801PR(sha string) *github.PullRequest {
	return &github.PullRequest{Number: 426, State: "open", Head: github.PRRef{SHA: sha}}
}

func TestRedriveSizeGuardHoldOnGreen_SameSHAGreen_Revives(t *testing.T) {
	e := newGH5801Env(t)
	e.setConclusion("success")
	pr := heldPR("sha-A", sizeGuardHoldReason)

	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-A"))

	if pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", pr.Stage)
	}
	if pr.HoldActive || pr.HoldHeadSHA != "" || pr.HoldLabels != "" || pr.Error != "" {
		t.Errorf("hold not cleared: active=%v head=%q labels=%q error=%q", pr.HoldActive, pr.HoldHeadSHA, pr.HoldLabels, pr.Error)
	}
	if pr.MergeAttempts != 2 || pr.HoldRedriveCount != 1 {
		t.Errorf("counters changed: MergeAttempts=%d HoldRedriveCount=%d, want 2 and 1", pr.MergeAttempts, pr.HoldRedriveCount)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.removed) != 1 || e.removed[0] != labelNeedsHuman {
		t.Errorf("removed labels = %v, want [%s]", e.removed, labelNeedsHuman)
	}
	if len(e.comments) != 1 || !strings.Contains(e.comments[0], "CI green on sha-A after rerun; resuming") {
		t.Errorf("comments = %v, want one 'CI green on sha-A after rerun; resuming'", e.comments)
	}
}

func TestRedriveSizeGuardHoldOnGreen_DifferentSHA_NoRedrive(t *testing.T) {
	e := newGH5801Env(t)
	e.setConclusion("success")
	pr := heldPR("sha-A", sizeGuardHoldReason)

	// Green status exists, but on a head other than the held one: that is
	// redriveHeldPR's (new-head) business, not this same-SHA leg.
	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-B"))

	if pr.Stage != StageFailed || !pr.HoldActive || pr.Error != sizeGuardHoldReason {
		t.Errorf("held PR changed: stage=%v active=%v error=%q", pr.Stage, pr.HoldActive, pr.Error)
	}
	if n := e.commentCount(); n != 0 {
		t.Errorf("comments posted = %d, want 0", n)
	}
}

func TestRedriveSizeGuardHoldOnGreen_StillRed_NoRedrive(t *testing.T) {
	e := newGH5801Env(t)
	pr := heldPR("sha-A", sizeGuardHoldReason)

	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-A"))

	if pr.Stage != StageFailed || !pr.HoldActive {
		t.Errorf("red CI must leave the hold: stage=%v active=%v", pr.Stage, pr.HoldActive)
	}
}

func TestRedriveSizeGuardHoldOnGreen_OtherReason_Untouched(t *testing.T) {
	e := newGH5801Env(t)
	e.setConclusion("success")
	pr := heldPR("sha-A", "CI failure with zero gathered evidence")

	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-A"))

	if pr.Stage != StageFailed || !pr.HoldActive || pr.Error != "CI failure with zero gathered evidence" {
		t.Errorf("other-reason hold changed: stage=%v active=%v error=%q", pr.Stage, pr.HoldActive, pr.Error)
	}
	if n := e.commentCount(); n != 0 {
		t.Errorf("comments posted = %d, want 0", n)
	}
}

// TestRedriveSizeGuardHoldOnGreen_OneShot: after the revive, a second red on
// the same SHA parks the PR again through the real size guard, and a later
// green on that SHA does not revive it a second time.
func TestRedriveSizeGuardHoldOnGreen_OneShot(t *testing.T) {
	e := newGH5801Env(t)
	e.setConclusion("success")
	pr := heldPR("sha-A", sizeGuardHoldReason)
	e.c.mu.Lock()
	e.c.activePRs[426] = pr
	e.c.mu.Unlock()

	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-A"))
	if pr.Stage != StageWaitingCI {
		t.Fatalf("first revive: Stage = %v, want StageWaitingCI", pr.Stage)
	}

	// Second red on the same SHA: the real size guard parks it again.
	e.setConclusion("failure")
	pr.Stage = StageCIFailed
	if err := e.c.handleCIFailed(context.Background(), pr); err != nil {
		t.Fatalf("handleCIFailed: %v", err)
	}
	if pr.Stage != StageFailed || !pr.HoldActive || pr.Error != sizeGuardHoldReason {
		t.Fatalf("second red must re-park via the size guard: stage=%v active=%v error=%q", pr.Stage, pr.HoldActive, pr.Error)
	}

	// Green again on the same SHA: no second revive.
	e.setConclusion("success")
	before := e.commentCount()
	e.c.redriveSizeGuardHoldOnGreen(context.Background(), pr, gh5801PR("sha-A"))
	if pr.Stage != StageFailed || !pr.HoldActive {
		t.Errorf("second revive on same SHA: stage=%v active=%v, want parked", pr.Stage, pr.HoldActive)
	}
	// handleCIFailed's own hold comment is the only comment since `before`.
	if after := e.commentCount(); after != before {
		t.Errorf("comments grew %d -> %d on the blocked second revive", before, after)
	}
}
