package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ReconcileOrphanPRs_RegistersOrphan verifies that a pilot/ PR
// not yet in activePRs is registered after one reconciler tick.
func TestController_ReconcileOrphanPRs_RegistersOrphan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{
					Number:  42,
					HTMLURL: "https://github.com/owner/repo/pull/42",
					Head:    github.PRRef{Ref: "pilot/GH-100", SHA: "abc1234"},
					Base:    github.PRRef{Ref: "main"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// activePRs is empty — PR 42 is an orphan
	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("reconciler did not register orphan PR 42")
	}
	if pr.IssueNumber != 100 {
		t.Errorf("IssueNumber = %d, want 100", pr.IssueNumber)
	}
	if pr.BranchName != "pilot/GH-100" {
		t.Errorf("BranchName = %q, want pilot/GH-100", pr.BranchName)
	}

	// Metric should be incremented
	snap := c.metrics.Snapshot()
	if snap.OrphanPRsRegistered["reconciler"] != 1 {
		t.Errorf("OrphanPRsRegistered[reconciler] = %d, want 1", snap.OrphanPRsRegistered["reconciler"])
	}
}

// TestController_ReconcileOrphanPRs_SkipsTracked verifies that already-tracked
// PRs are not re-registered (no duplicate entry, no metric increment).
func TestController_ReconcileOrphanPRs_SkipsTracked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{
					Number:  42,
					HTMLURL: "https://github.com/owner/repo/pull/42",
					Head:    github.PRRef{Ref: "pilot/GH-100", SHA: "abc1234"},
					Base:    github.PRRef{Ref: "main"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Pre-populate activePRs so PR 42 is already tracked
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		IssueNumber: 100,
		BranchName:  "pilot/GH-100",
		HeadSHA:     "abc1234",
		Stage:       StageWaitingCI,
	}
	c.mu.Unlock()

	c.reconcileOrphanPRs(context.Background())

	// Metric should NOT be incremented
	snap := c.metrics.Snapshot()
	if snap.OrphanPRsRegistered["reconciler"] != 0 {
		t.Errorf("OrphanPRsRegistered[reconciler] = %d, want 0 (tracked PR should not be re-registered)", snap.OrphanPRsRegistered["reconciler"])
	}

	// Stage should be unchanged
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 missing from activePRs")
	}
	if pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI (reconciler should not clobber tracked state)", pr.Stage)
	}
}

// TestController_ReconcileOrphanPRs_NonPilotBranchSkipped verifies that PRs
// with branches that do not match pilot/GH-* are ignored.
func TestController_ReconcileOrphanPRs_NonPilotBranchSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{Number: 10, HTMLURL: "url", Head: github.PRRef{Ref: "feature/my-feature", SHA: "sha1"}},
				{Number: 11, HTMLURL: "url", Head: github.PRRef{Ref: "fix/some-bug", SHA: "sha2"}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.reconcileOrphanPRs(context.Background())

	prs := c.GetActivePRs()
	if len(prs) != 0 {
		t.Errorf("expected 0 registered PRs, got %d", len(prs))
	}
}

// TestController_ReconcileOrphanPRs_APIError verifies that an API error is
// handled gracefully (no panic, no registration).
func TestController_ReconcileOrphanPRs_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Should not panic
	c.reconcileOrphanPRs(context.Background())

	prs := c.GetActivePRs()
	if len(prs) != 0 {
		t.Errorf("expected 0 registered PRs on API error, got %d", len(prs))
	}
}
