package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HandleMerging_StackedSuperset_UnparksAfterBaseMerges is the
// GH-5034 full-lifecycle regression for the #5016/#5017 2026-08-20 incident
// shape: PR#17 (B) is built stacked on PR#16's (A) still-open branch, both
// targeting main. B clears CI first and reaches StageMerging before A does —
// driving B through ProcessPR (the state-machine test harness, exactly as
// controller_gh5031_test.go's HoldsInsteadOfMerging test does) must park it
// naming A instead of merging out of order. Once A merges — modeled here as
// a stage transition to StageMerged while STILL tracked in activePRs
// (GH-5049 requirement 3: the real lifecycle keeps a merged PR tracked
// through post-merge CI/release bookkeeping before eventually evicting it;
// dropping it from activePRs outright, as this test used to, only exercises
// the finalization-eviction resume path and can't distinguish it from
// resuming on the base reaching StageMerged — this version fails against
// pre-GH-5049 main, which resumed only on eviction) — a later tick for B
// must un-park (Parked=false, parked-awaiting-approval label removed) and
// proceed to merge normally, with no extra escalation firing on the way
// through.
func TestController_HandleMerging_StackedSuperset_UnparksAfterBaseMerges(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")
	baseSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var mergeCalled, labelApplied, labelRemoved atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/issues/17/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/17/labels" && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/17/labels/"+labelParkedAwaitingApproval && r.Method == http.MethodDelete:
			labelRemoved.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/17/labels/") && r.Method == http.MethodDelete:
			// Other label removals fired by the post-merge "delivered" bookkeeping
			// (pilot-in-progress/pilot-failed/pilot-retry-* cleanup) — not under
			// test here, just acknowledged so they don't 404-noise the log.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI, // A hasn't cleared CI yet
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		IssueNumber:  17, // GH-5049: needed so the un-park path's label removal actually fires
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageMerging, // B cleared CI first
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	ghPR17 := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: stackedSHA},
		Base:   github.PRRef{Ref: "main"},
	}

	// Tick 1: A is still open — B must park naming A instead of merging.
	if err := c.ProcessPR(ctx, 17, ghPR17); err != nil {
		t.Fatalf("tick 1: ProcessPR: %v", err)
	}
	if mergeCalled.Load() != 0 {
		t.Fatalf("tick 1: merge called %d times, want 0 — B must not merge ahead of A", mergeCalled.Load())
	}
	pr17, ok := c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if !pr17.Parked {
		t.Error("tick 1: Parked should be true")
	}
	if !strings.Contains(pr17.EscalationReason, "PR #16") {
		t.Errorf("tick 1: EscalationReason = %q, want it to name PR #16", pr17.EscalationReason)
	}
	if pr17.Stage != StageMerging {
		t.Errorf("tick 1: Stage = %s, want %s (parked, not merged)", pr17.Stage, StageMerging)
	}
	if len(sink.events) != 1 {
		t.Fatalf("tick 1: alerts fired %d times, want 1", len(sink.events))
	}
	if labelApplied.Load() != 1 {
		t.Errorf("tick 1: parked-awaiting-approval label applied %d times, want 1", labelApplied.Load())
	}

	// A merges: modeled as a stage transition to StageMerged while STILL
	// tracked in activePRs (GH-5049 requirement 1/3) — NOT removal from
	// activePRs. The real lifecycle keeps a merged PR tracked through
	// post-merge CI/release bookkeeping (handleMerged/handleReleasing)
	// before eventually evicting it via removePR; a resume mechanism that
	// only reacts to eviction resumes late in the normal case and can wedge
	// permanently if the base ever gets stuck tracked in a post-merge stage.
	// This step must be enough, on its own, to un-park B on the next tick.
	c.mu.Lock()
	c.activePRs[16].Stage = StageMerged
	c.mu.Unlock()

	// Tick 2: A has merged (still tracked, StageMerged) — B must un-park and
	// proceed to merge.
	if err := c.ProcessPR(ctx, 17, ghPR17); err != nil {
		t.Fatalf("tick 2: ProcessPR: %v", err)
	}
	if mergeCalled.Load() != 1 {
		t.Fatalf("tick 2: merge called %d times, want 1 — B must merge once A reaches StageMerged", mergeCalled.Load())
	}
	pr17, ok = c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr17.Parked {
		t.Error("tick 2: Parked should be false — the stacked-superset park must clear once the base reaches StageMerged")
	}
	if labelRemoved.Load() != 1 {
		t.Errorf("tick 2: parked-awaiting-approval label removed %d times, want 1", labelRemoved.Load())
	}
	if pr17.Stage != StageMerged {
		t.Errorf("tick 2: Stage = %s, want %s — B should have proceeded through to a completed merge", pr17.Stage, StageMerged)
	}
	// No new escalation should fire on the way through — un-parking is not
	// itself an event.
	if len(sink.events) != 1 {
		t.Errorf("tick 2: alerts fired %d times total, want 1 (no new escalation on un-park)", len(sink.events))
	}
}

// TestController_HandleMerging_StackedSuperset_FailedBaseNotBlocking is the
// GH-5049 (GH-5032 residual, requirement 2) regression flagged in PR#5035's
// review: a base PR (A) that reached StageFailed will never merge, so it can
// never legitimately be "merge that first" — a descendant built on top of
// its still-unmerged branch must NOT be parked on it and must merge normally
// through handleMerging via ProcessPR.
func TestController_HandleMerging_StackedSuperset_FailedBaseNotBlocking(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")
	baseSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var mergeCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/issues/17/comments" && r.Method == http.MethodGet:
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
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageFailed, // A will never merge
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	ghPR17 := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: stackedSHA},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(ctx, 17, ghPR17); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if mergeCalled.Load() != 1 {
		t.Fatalf("merge called %d times, want 1 — a StageFailed base must never hold a descendant hostage", mergeCalled.Load())
	}
	pr17, ok := c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr17.Parked {
		t.Errorf("Parked should be false — a StageFailed base is not a valid stacked-superset candidate, got reason=%q", pr17.EscalationReason)
	}
	if pr17.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr17.Stage, StageMerged)
	}
	if len(sink.events) != 0 {
		t.Fatalf("alerts fired %d times, want 0 — merging past a StageFailed base must not escalate", len(sink.events))
	}
}

// TestController_HandleMerging_StackedSuperset_BaseOfStackNotBlocked covers
// GH-5034 case (b), the symmetric ancestor shape: when THIS PR is the base
// of a stack (i.e. an ANCESTOR of another still-open PR's head, rather than
// a descendant), driving it through handleMerging via ProcessPR must merge
// it normally — detectStackedSuperset must never hold the base of a stack,
// only the PR built on top of it.
func TestController_HandleMerging_StackedSuperset_BaseOfStackNotBlocked(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")
	baseSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var mergeCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/16/merge" && r.Method == http.MethodPut:
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

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageMerging, // A (the base of the stack) is up for merge
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI, // B is still open, stacked on A
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	ghPR16 := &github.PullRequest{
		Number: 16,
		Head:   github.PRRef{SHA: baseSHA},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(ctx, 16, ghPR16); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if mergeCalled.Load() != 1 {
		t.Fatalf("merge called %d times, want 1 — the base of a stack must merge unhindered", mergeCalled.Load())
	}
	pr16, ok := c.GetPRState(16)
	if !ok {
		t.Fatal("PR 16 should still be tracked")
	}
	if pr16.Parked {
		t.Error("Parked should be false — A is the base of the stack, not a descendant")
	}
	if pr16.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr16.Stage, StageMerged)
	}
	if len(sink.events) != 0 {
		t.Fatalf("alerts fired %d times, want 0 — merging the base of a stack must not escalate", len(sink.events))
	}
}

// TestController_HandleMerging_StackedSuperset_UnrelatedPRsBothMerge covers
// GH-5034 case (c): two open PRs with disjoint ancestry off main (neither
// stacked on the other) must both merge unhindered through handleMerging,
// with detectStackedSuperset adding no hold to either.
func TestController_HandleMerging_StackedSuperset_UnrelatedPRsBothMerge(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-20")
	writeFixtureFile(t, local, "twenty.txt", "from GH-20\n")
	runFixtureGit(t, local, "add", "twenty.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-20 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-20")
	sha20 := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "main")
	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-21")
	writeFixtureFile(t, local, "twentyone.txt", "from GH-21\n")
	runFixtureGit(t, local, "add", "twentyone.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-21 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-21")
	sha21 := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var merge20Called, merge21Called atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/20/merge" && r.Method == http.MethodPut:
			merge20Called.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"merged20","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/21/merge" && r.Method == http.MethodPut:
			merge21Called.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"merged21","merged":true,"message":"merged"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[20] = &PRState{PRNumber: 20, BranchName: "pilot/GH-20", HeadSHA: sha20, TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.activePRs[21] = &PRState{PRNumber: 21, BranchName: "pilot/GH-21", HeadSHA: sha21, TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.mu.Unlock()

	ghPR20 := &github.PullRequest{Number: 20, Head: github.PRRef{SHA: sha20}, Base: github.PRRef{Ref: "main"}}
	ghPR21 := &github.PullRequest{Number: 21, Head: github.PRRef{SHA: sha21}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(ctx, 20, ghPR20); err != nil {
		t.Fatalf("ProcessPR(20): %v", err)
	}
	if err := c.ProcessPR(ctx, 21, ghPR21); err != nil {
		t.Fatalf("ProcessPR(21): %v", err)
	}

	if merge20Called.Load() != 1 {
		t.Errorf("PR 20 merge called %d times, want 1 — unrelated PRs must merge unhindered", merge20Called.Load())
	}
	if merge21Called.Load() != 1 {
		t.Errorf("PR 21 merge called %d times, want 1 — unrelated PRs must merge unhindered", merge21Called.Load())
	}
	pr20, _ := c.GetPRState(20)
	pr21, _ := c.GetPRState(21)
	if pr20.Parked || pr21.Parked {
		t.Errorf("neither PR should be parked: pr20.Parked=%v pr21.Parked=%v", pr20.Parked, pr21.Parked)
	}
	if pr20.Stage != StageMerged || pr21.Stage != StageMerged {
		t.Errorf("both PRs should have merged: pr20.Stage=%s pr21.Stage=%s", pr20.Stage, pr21.Stage)
	}
	if len(sink.events) != 0 {
		t.Fatalf("alerts fired %d times, want 0 — no stacking relationship between 20 and 21", len(sink.events))
	}
}

// TestController_HandleMerging_StackedSuperset_DetectionErrorFailsOpen covers
// GH-5034 case (d): when detectStackedSuperset cannot determine ancestry for
// a candidate (here, the GitHub compare API fallback errors because no local
// project checkout is configured), handleMerging must fail OPEN and proceed
// with the merge rather than wedging the PR — this is a toil-reducing guard,
// not a correctness gate (handleMergeConflict already recovers a stacked PR
// that slips through).
func TestController_HandleMerging_StackedSuperset_DetectionErrorFailsOpen(t *testing.T) {
	ctx := context.Background()

	var mergeCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/compare/"):
			// Simulate the ancestry probe itself failing (e.g. a transient
			// GitHub API error) rather than returning a definitive status.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/23/merge" && r.Method == http.MethodPut:
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

	// No WithProjectPath — forces headIsStrictDescendant onto the GitHub
	// compare API fallback, which is rigged above to fail.
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[22] = &PRState{PRNumber: 22, BranchName: "pilot/GH-22", HeadSHA: "sha22", TargetBranch: "main", Stage: StageWaitingCI, CreatedAt: time.Now()}
	c.activePRs[23] = &PRState{PRNumber: 23, BranchName: "pilot/GH-23", HeadSHA: "sha23", TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.mu.Unlock()

	ghPR23 := &github.PullRequest{Number: 23, Head: github.PRRef{SHA: "sha23"}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(ctx, 23, ghPR23); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if mergeCalled.Load() != 1 {
		t.Fatalf("merge called %d times, want 1 — a detection error must fail open, not wedge the merge", mergeCalled.Load())
	}
	pr23, ok := c.GetPRState(23)
	if !ok {
		t.Fatal("PR 23 should still be tracked")
	}
	if pr23.Parked {
		t.Error("Parked should be false — a failed detection must never park the PR")
	}
	if pr23.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr23.Stage, StageMerged)
	}
	if len(sink.events) != 0 {
		t.Fatalf("alerts fired %d times, want 0 — a detection error is not itself an escalation", len(sink.events))
	}
}

// TestController_HandleWaitingCI_BaseMismatchPark_UnparksAfterBaseMerges_GH5066
// extends this suite (GH-5066, subtask 2/4) to the OTHER park entry point:
// GH-5066/PR (commit 802366ef) taught handleWaitingCI's confirmed-CI-timeout
// branch to park a non-default-base PR (via parkForBaseMismatch) instead of
// failing terminally — but that park leaves the PR in StageWaitingCI, never
// StageMerging. TestController_HandleMerging_StackedSuperset_UnparksAfterBaseMerges
// above proves the resume path for a PR parked INSIDE handleMerging (already
// in StageMerging); this test proves — or disproves — the analogous claim
// for a PR parked from handleWaitingCI: does it, too, un-park and proceed
// once its base (A) reaches StageMerged, exactly as the parent GH-5066 title's
// "no re-arm on base retarget" clause asks?
func TestController_HandleWaitingCI_BaseMismatchPark_UnparksAfterBaseMerges_GH5066(t *testing.T) {
	const sha = "gh5066resume01"
	const baseSHA = "gh5066base01"
	const prNumber = 17
	const baseNumber = 16

	var ciResolved atomic.Bool
	var mergeCalled, labelApplied, labelRemoved atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			// CI never resolves while still stacked on the sibling branch —
			// mirrors the PR#5055 incident, where the repo's workflow is
			// scoped to the default branch only so a stacked PR's checks
			// never run. Once retargeted to main (ciResolved flips below),
			// a fresh check run can actually complete.
			status := github.CheckRunInProgress
			conclusion := ""
			if ciResolved.Load() {
				status = github.CheckRunCompleted
				conclusion = github.ConclusionSuccess
			}
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: status, Conclusion: conclusion}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/17/files":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/17" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"number":17,"title":"GH-17 work, stacked on GH-16"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case strings.HasSuffix(r.URL.Path, "/issues/17/comments") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/issues/17/comments") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		case r.URL.Path == "/repos/owner/repo/issues/17/labels" && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/17/labels/"+labelParkedAwaitingApproval && r.Method == http.MethodDelete:
			labelRemoved.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/17/labels/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
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
	c.activePRs[baseNumber] = &PRState{
		PRNumber:     baseNumber,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI, // A hasn't cleared CI yet
		CreatedAt:    time.Now(),
	}
	c.activePRs[prNumber] = &PRState{
		PRNumber:     prNumber,
		IssueNumber:  prNumber,
		BranchName:   "pilot/GH-17",
		HeadSHA:      sha,
		TargetBranch: "pilot/GH-16", // stacked on A's still-open branch
		Stage:        StageWaitingCI,
		CIStatus:     CIPending,
		// Deadline already exceeded, mirroring the PR#5055 incident: CI never
		// ran because this repo's workflow is scoped to the default branch.
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
		CreatedAt:       time.Now(),
	}
	c.mu.Unlock()

	ghPRStacked := &github.PullRequest{
		Number: prNumber,
		Head:   github.PRRef{SHA: sha},
		Base:   github.PRRef{Ref: "pilot/GH-16"},
	}

	// Tick 1: A is still open, B's CI never ran and the wait deadline has
	// passed — B must park via parkForBaseMismatch (GH-5066) rather than
	// fail terminally.
	if err := c.ProcessPR(context.Background(), prNumber, ghPRStacked); err != nil {
		t.Fatalf("tick 1: ProcessPR: %v", err)
	}
	pr17, ok := c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr17.Stage != StageWaitingCI {
		t.Fatalf("tick 1: Stage = %s, want %s (parked, not failed)", pr17.Stage, StageWaitingCI)
	}
	if !pr17.Parked {
		t.Fatal("tick 1: Parked should be true")
	}
	if !strings.HasPrefix(pr17.EscalationReason, baseMismatchReasonPrefix) {
		t.Fatalf("tick 1: EscalationReason = %q, want prefix %q", pr17.EscalationReason, baseMismatchReasonPrefix)
	}
	if labelApplied.Load() != 1 {
		t.Fatalf("tick 1: parked-awaiting-approval label applied %d times, want 1", labelApplied.Load())
	}

	// A merges: modeled exactly as
	// TestController_HandleMerging_StackedSuperset_UnparksAfterBaseMerges
	// models it above — a stage transition to StageMerged while STILL
	// tracked in activePRs (GH-5049 requirement 3), plus the GitHub-side
	// consequence of a merged-and-deleted base branch: the PR is auto-
	// retargeted to A's own base ("main"), which ProcessPR refreshes into
	// TargetBranch unconditionally every tick (GH-4909 defect 1, line
	// ~2614) before any stage handler runs.
	c.mu.Lock()
	c.activePRs[baseNumber].Stage = StageMerged
	c.mu.Unlock()
	ghPRRetargeted := &github.PullRequest{
		Number: prNumber,
		Head:   github.PRRef{SHA: sha},
		Base:   github.PRRef{Ref: "main"},
	}

	// Tick 2: A has merged and B has been retargeted to main — B must
	// un-park (base mismatch resolved) and get a fresh CI-wait window
	// instead of falling into the stale-deadline StageFailed branch it was
	// parked to avoid.
	if err := c.ProcessPR(context.Background(), prNumber, ghPRRetargeted); err != nil {
		t.Fatalf("tick 2: ProcessPR: %v", err)
	}
	pr17, ok = c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr17.Parked {
		t.Error("tick 2: Parked should be false — the base-mismatch park must clear once the PR is retargeted to the default branch")
	}
	if pr17.EscalationReason != "" {
		t.Errorf("tick 2: EscalationReason = %q, want empty — cleared alongside Parked", pr17.EscalationReason)
	}
	if pr17.Stage != StageWaitingCI {
		t.Fatalf("tick 2: Stage = %s, want %s — un-parking must not itself fail or skip stages, just resume the normal CI wait", pr17.Stage, StageWaitingCI)
	}
	if labelRemoved.Load() != 1 {
		t.Errorf("tick 2: parked-awaiting-approval label removed %d times, want 1", labelRemoved.Load())
	}

	// Now let CI actually resolve against the corrected base (main), exactly
	// as it would in reality once the PR is properly retargeted. B must
	// proceed all the way through to a completed merge — driving ProcessPR
	// repeatedly, same as the real poll loop, since each call advances at
	// most one stage.
	ciResolved.Store(true)
	const maxTicks = 6
	for i := 0; i < maxTicks; i++ {
		if err := c.ProcessPR(context.Background(), prNumber, ghPRRetargeted); err != nil {
			t.Fatalf("post-resume tick %d: ProcessPR: %v", i, err)
		}
		pr17, ok = c.GetPRState(prNumber)
		if !ok {
			t.Fatal("PR 17 should still be tracked")
		}
		if pr17.Stage == StageFailed || pr17.Stage == StageCIFailed {
			t.Fatalf("post-resume tick %d: Stage = %s — B must merge cleanly once retargeted, CI passing", i, pr17.Stage)
		}
		if pr17.Stage == StageMerged {
			break
		}
	}
	pr17, ok = c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr17.Stage != StageMerged {
		t.Fatalf("Stage = %s, want %s — B should have proceeded through to a completed merge", pr17.Stage, StageMerged)
	}
	if pr17.Parked {
		t.Error("Parked should still be false after merging")
	}
	if mergeCalled.Load() != 1 {
		t.Errorf("merge called %d times, want 1 — B must merge exactly once after un-parking", mergeCalled.Load())
	}
}
