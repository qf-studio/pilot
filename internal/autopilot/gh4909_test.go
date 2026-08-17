package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ProcessPR_RetargetToDefault_ResumesWithoutManualIntervention
// is the GH-4909 regression for defect 1's first direction: a PR parked for
// a base mismatch (GH-4872) whose base a human retargets back to the default
// branch must resume and merge on the very next tick, with no manual
// `gh pr merge`. Before this fix, ProcessPR only ever wrote TargetBranch
// once (when it was empty), so the resume check in handleMerging kept
// re-reading the stale non-default value forever and the PR stayed parked.
func TestController_ProcessPR_RetargetToDefault_ResumesWithoutManualIntervention(t *testing.T) {
	var mergeCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/90/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetAlertsEngine(&fakeAlertSink{})

	c.mu.Lock()
	c.activePRs[90] = &PRState{
		PRNumber:     90,
		HeadSHA:      "sha90",
		Stage:        StageMerging,
		TargetBranch: "pilot/GH-70", // parked on a stacked base from a prior tick
		Parked:       true,
		EscalationReason: `base branch mismatch: PR targets "pilot/GH-70", not the default branch "main"` +
			` — this looks like a stacked or mis-based PR; a human must retarget it or merge the stack in order`,
		CreatedAt: time.Now(),
	}
	c.mu.Unlock()

	// Human retargeted the PR to main — the next poll tick supplies the
	// updated ghPR (as processAllPRs does with its own freshly-fetched PR list).
	ghPR := &github.PullRequest{
		Number: 90,
		Head:   github.PRRef{SHA: "sha90"},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(context.Background(), 90, ghPR); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 1 {
		t.Errorf("merge called %d times, want 1 — retargeting to the default branch must resume auto-merge without manual intervention", mergeCalled.Load())
	}

	pr, ok := c.GetPRState(90)
	if !ok {
		t.Fatal("PR 90 should still be tracked")
	}
	if pr.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want refreshed value %q", pr.TargetBranch, "main")
	}
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
}

// TestController_ProcessPR_RetargetToNonDefault_StaleCacheNoLongerMerges is
// the GH-4909 regression for defect 1's second, more dangerous direction: a
// PR adopted with base=main, later retargeted to a non-default (stacked)
// branch, must be parked — not merged — on the next tick. Before this fix,
// ProcessPR's TargetBranch was frozen at "main" from the first observation,
// so handleMerging's base guard passed on the stale value and MergePR
// merged into the PR's actual (now non-default) current base — reopening
// the GH-4872 incident through the very path meant to guard against it.
func TestController_ProcessPR_RetargetToNonDefault_StaleCacheNoLongerMerges(t *testing.T) {
	var mergeCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/91/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[91] = &PRState{
		PRNumber:     91,
		IssueNumber:  71,
		HeadSHA:      "sha91",
		Stage:        StageMerging,
		TargetBranch: "main", // adopted on the default branch
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	// Human (or an automated rebase) retargeted the PR onto a stacked branch.
	ghPR := &github.PullRequest{
		Number: 91,
		Head:   github.PRRef{SHA: "sha91"},
		Base:   github.PRRef{Ref: "pilot/GH-70"},
	}

	if err := c.ProcessPR(context.Background(), 91, ghPR); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 0 {
		t.Errorf("merge was called %d times, want 0 — a PR retargeted onto a stacked branch must never merge on the stale cached base", mergeCalled.Load())
	}

	pr, ok := c.GetPRState(91)
	if !ok {
		t.Fatal("PR 91 should still be tracked (parked, not removed)")
	}
	if pr.TargetBranch != "pilot/GH-70" {
		t.Errorf("TargetBranch = %q, want refreshed value %q", pr.TargetBranch, "pilot/GH-70")
	}
	if !pr.Parked {
		t.Error("Parked should be true")
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}
}

// TestController_HandleMerging_PostMergeBaseMismatch_SkipsFinalize is the
// GH-4909 regression for defect 2: the handleMerging finalize block itself
// (issue close / pilot-done label / monitor.Complete / self-heal /
// board->Done) must apply the same non-default-base predicate as the
// isPilotPR scanner and checkExternalMergeOrClose, as belt-and-braces
// against a retarget racing the pre-merge guard (GitHub's merge endpoint
// merges into whatever base is current at that instant, not whatever
// handleMerging last cached). Simulated here by having the PR pass the
// pre-merge guard on a cached "main" TargetBranch, but the post-merge
// re-verification GetPullRequest call reveals the PR actually landed on a
// non-default base.
func TestController_HandleMerging_PostMergeBaseMismatch_SkipsFinalize(t *testing.T) {
	var (
		mergeCalled atomic.Int32
		issueClosed atomic.Bool
		doneAdded   atomic.Bool
		pivotPosted atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/92/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/92" && r.Method == http.MethodGet:
			// Post-merge re-verification: GitHub reports the PR actually
			// landed on a stacked branch, not the cached "main".
			resp := github.PullRequest{
				Number: 92,
				Head:   github.PRRef{SHA: "sha92"},
				Base:   github.PRRef{Ref: "pilot/GH-70"},
				Merged: true,
			}
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, resp)

		case r.URL.Path == "/repos/owner/repo/issues/72" && r.Method == http.MethodPatch:
			issueClosed.Store(true)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/72/labels" && r.Method == http.MethodPost:
			doneAdded.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.URL.Path == "/repos/owner/repo/issues/72/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			if pivotPosted.Load() == 0 {
				_, _ = w.Write([]byte("[]"))
			} else {
				_, _ = w.Write([]byte(`[{"id":1,"body":"` + basePivotCommentMarker + `\nsome text"}]`))
			}

		case r.URL.Path == "/repos/owner/repo/issues/72/comments" && r.Method == http.MethodPost:
			pivotPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[92] = &PRState{
		PRNumber:     92,
		IssueNumber:  72,
		HeadSHA:      "sha92",
		Stage:        StageMerging,
		TargetBranch: "main", // passes the pre-merge guard
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 92, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 1 {
		t.Fatalf("merge called %d times, want 1 — the pre-merge guard should have let this through on the cached base", mergeCalled.Load())
	}
	if issueClosed.Load() {
		t.Error("issue must NOT be closed — post-merge re-verification found a non-default landed base")
	}
	if doneAdded.Load() {
		t.Error("pilot-done must NOT be added — post-merge re-verification found a non-default landed base")
	}
	if pivotPosted.Load() != 1 {
		t.Errorf("pivot comment posted %d times, want 1", pivotPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}

	pr, ok := c.GetPRState(92)
	if !ok {
		t.Fatal("PR 92 should still be tracked")
	}
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s (the merge itself still succeeded)", pr.Stage, StageMerged)
	}
	if pr.TargetBranch != "pilot/GH-70" {
		t.Errorf("TargetBranch = %q, want re-verified value %q", pr.TargetBranch, "pilot/GH-70")
	}
}

// TestController_AlertUnresolvableBaseOnce is the GH-4909 regression for
// defect 3: an unresolvable base (empty TargetBranch, and the re-read
// GetPullRequest call fails) previously produced only a per-tick Warn log —
// a PR could wedge there indefinitely with nothing surfacing it to a human.
// A one-time escalation alert must fire, deduplicated across ticks.
func TestController_AlertUnresolvableBaseOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/93" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[93] = &PRState{
		PRNumber:  93,
		HeadSHA:   "sha93",
		Stage:     StageMerging,
		CreatedAt: time.Now(),
		// TargetBranch intentionally left empty, and the re-read below fails.
	}
	c.mu.Unlock()

	for i := 0; i < 2; i++ {
		if err := c.ProcessPR(context.Background(), 93, nil); err != nil {
			t.Fatalf("tick %d: ProcessPR returned error: %v", i, err)
		}
	}

	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1 (deduped across ticks)", len(sink.events))
	}
}

// TestController_ParkForBaseMismatch_DistinctFromMisconfigPark is the
// GH-4909 regression for defect 4: parkForBaseMismatch previously reused the
// shared Parked flag with submitAsyncApprovalRequest's unrelated misconfig
// park (GH-4596) — a PR already Parked=true for a misconfig reason that
// later hit a base mismatch silently skipped the base-mismatch
// alert/comment/label because the old code only checked the bool. Comparing
// EscalationReason distinguishes the two causes so each alerts once.
func TestController_ParkForBaseMismatch_DistinctFromMisconfigPark(t *testing.T) {
	var (
		commentPosted atomic.Int32
		labelApplied  atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/94/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			if commentPosted.Load() == 0 {
				_, _ = w.Write([]byte("[]"))
			} else {
				_, _ = w.Write([]byte(`[{"id":1,"body":"` + baseMismatchCommentMarker + `\nsome text"}]`))
			}
		case r.URL.Path == "/repos/owner/repo/issues/94/comments" && r.Method == http.MethodPost:
			commentPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		case r.URL.Path == "/repos/owner/repo/issues/74/labels" && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber:    94,
		IssueNumber: 74,
		Stage:       StageMerging,
		// Simulates a PR already parked earlier for an unrelated approval
		// misconfig (GH-4596) — Parked=true, EscalationReason naming that
		// gate, NOT a base mismatch.
		Parked:           true,
		EscalationReason: "approval required but no approval channel is wired",
	}

	c.parkForBaseMismatch(context.Background(), prState, "pilot/GH-70", "main")

	if !prState.Parked {
		t.Error("Parked should remain true")
	}
	if labelApplied.Load() != 1 {
		t.Errorf("parked-awaiting-approval label applied %d times, want 1 — a distinct park cause must still label", labelApplied.Load())
	}
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times, want 1 — a distinct park cause must still comment", commentPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1 — a distinct park cause must still alert", len(sink.events))
	}

	// A second call for the SAME base mismatch must stay quiet (idempotent).
	c.parkForBaseMismatch(context.Background(), prState, "pilot/GH-70", "main")
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times after repeat call, want 1 (idempotent)", commentPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times after repeat call, want 1 (idempotent)", len(sink.events))
	}
}
