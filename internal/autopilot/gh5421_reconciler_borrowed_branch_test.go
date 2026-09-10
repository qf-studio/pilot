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

// fakeBorrowedBranchLookup is a minimal BorrowedBranchLookup fake for
// reconcileOrphanPRs tests — GH-5421.
type fakeBorrowedBranchLookup struct {
	branch   string
	fixIssue int
}

func (f *fakeBorrowedBranchLookup) FixIssueForBranch(branch string) (int, bool) {
	if branch == f.branch && f.fixIssue > 0 {
		return f.fixIssue, true
	}
	return 0, false
}

// TestController_ReconcileOrphanPRs_ClosedOriginIssueNoFixRecord_SkipsRegistration
// is the GH-5421 regression test for reconciler fix #3's negative case: an
// untracked pilot/GH-<O> PR whose origin issue O is closed, with neither the
// state store (HasSpawnedFixForPR) nor the in-process borrowed-branch
// registry (SetBorrowedBranchLookup) naming a fix issue, must be skipped
// rather than silently registered under the closed issue O — attaching a
// live PR's eventual merge comment to a closed, unrelated issue is exactly
// the bug this issue closes.
func TestController_ReconcileOrphanPRs_ClosedOriginIssueNoFixRecord_SkipsRegistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
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
		case "/repos/owner/repo/issues/100":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(&github.Issue{Number: 100, State: github.StateClosed})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// No stateStore, no borrowedBranchLookup wired — neither signal is available.

	c.reconcileOrphanPRs(context.Background())

	if _, ok := c.GetPRState(42); ok {
		t.Fatal("reconciler must not register an orphan PR under a closed origin issue when no fix-issue record exists")
	}
	snap := c.metrics.Snapshot()
	if snap.OrphanPRsRegistered["reconciler"] != 0 {
		t.Errorf("OrphanPRsRegistered[reconciler] = %d, want 0", snap.OrphanPRsRegistered["reconciler"])
	}
}

// TestController_ReconcileOrphanPRs_BorrowedBranchLookup_RegistersUnderFixIssue
// is the GH-5421 regression test for reconciler fix #3's positive case:
// HasSpawnedFixForPR is keyed by the ORIGIN PR number, not this new orphan
// PR's own number, so it never matches the ordinary CI-fix flow. The
// in-process borrowed-branch registry (SetBorrowedBranchLookup), keyed by
// branch name instead, must be consulted as a fallback and — when it names a
// fix issue — the orphan PR must be registered under that fix issue via
// OnPRCreatedForFixIssue, with the branch name preserved unchanged.
func TestController_ReconcileOrphanPRs_BorrowedBranchLookup_RegistersUnderFixIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
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
		default:
			// The fix-issue lookup succeeds via the borrowed-branch registry
			// before the reconciler ever needs to call GetIssue, so no
			// /repos/owner/repo/issues/... request should reach here.
			t.Errorf("unexpected request to %s — GetIssue should not be called when the borrowed-branch registry already found a fix issue", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetBorrowedBranchLookup(&fakeBorrowedBranchLookup{branch: "pilot/GH-100", fixIssue: 200})

	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("reconciler did not register orphan PR 42 under the fix issue named by the borrowed-branch registry")
	}
	if pr.IssueNumber != 200 {
		t.Errorf("IssueNumber = %d, want 200 (fix issue from the borrowed-branch registry)", pr.IssueNumber)
	}
	if pr.BranchName != "pilot/GH-100" {
		t.Errorf("BranchName = %q, want %q (borrowed origin branch preserved unchanged)", pr.BranchName, "pilot/GH-100")
	}

	snap := c.metrics.Snapshot()
	if snap.OrphanPRsRegistered["reconciler"] != 1 {
		t.Errorf("OrphanPRsRegistered[reconciler] = %d, want 1", snap.OrphanPRsRegistered["reconciler"])
	}
}
