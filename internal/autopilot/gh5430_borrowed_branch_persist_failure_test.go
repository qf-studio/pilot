package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestEvictPersistFailedPR_ReRecordsBorrowedBranchForFixIssuePR is the
// GH-5430 regression test for reconciler fix #1 (PR #5427 follow-up gap #1):
// FixIssueForBranch consumes the runner's borrowed-branch registry entry
// destructively the instant it matches, well before OnPRCreatedForFixIssue
// (called right after) has any chance to persist the resulting PR state. If
// that persistence keeps failing until evictPersistFailedPR drops the PR,
// the registry entry is already gone — before this fix, no later reconciler
// tick could ever rediscover the fix issue for this branch again, orphaning
// the PR permanently. evictPersistFailedPR must re-record the entry (chosen
// fix, stated in the PR body per the issue: option B — "re-record the entry
// when evictPersistFailedPR drops a PR that was registered through the
// fix-issue path" — rather than option A's deferred-consume redesign) so a
// later tick, once persistFailureReadoptCooldown elapses, can re-adopt it.
func TestEvictPersistFailedPR_ReRecordsBorrowedBranchForFixIssuePR(t *testing.T) {
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
			t.Errorf("unexpected request to %s — the borrowed-branch registry re-record should let the reconciler skip straight to registration", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	fake := &fakeBorrowedBranchLookup{}
	c.SetBorrowedBranchLookup(fake)

	// Simulate the reconciler's normal fix-issue registration: a match on
	// FixIssueForBranch already consumed the runner-side entry (out of scope
	// for this test — see gh5425_borrowed_branch_prune_test.go), and the
	// reconciler now calls OnPRCreatedForFixIssue with the result.
	c.OnPRCreatedForFixIssue(42, "https://github.com/owner/repo/pull/42", 200, 100, "abc1234", "pilot/GH-100", "")

	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 was not registered by OnPRCreatedForFixIssue")
	}
	if !prState.RegisteredViaFixIssue {
		t.Fatal("RegisteredViaFixIssue = false after OnPRCreatedForFixIssue, want true")
	}

	// Persistence for PR 42 keeps failing until it is evicted.
	c.evictPersistFailedPR(42)

	if _, ok := c.GetPRState(42); ok {
		t.Fatal("PR 42 is still tracked after evictPersistFailedPR, want it evicted")
	}

	fake.mu.Lock()
	recorded := append([]recordedBorrowedBranch(nil), fake.recorded...)
	fake.mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("RecordBorrowedBranch was called %d times, want exactly 1 (the re-record on eviction)", len(recorded))
	}
	if recorded[0].taskID != "GH-200" || recorded[0].branch != "pilot/GH-100" {
		t.Errorf("re-recorded entry = (taskID=%q, branch=%q), want (taskID=%q, branch=%q)",
			recorded[0].taskID, recorded[0].branch, "GH-200", "pilot/GH-100")
	}

	// "The following tick": backdate the persist-failure cooldown so
	// reconcileOrphanPRs is willing to re-adopt PR 42 instead of skipping it
	// via recentlyEvictedForPersistFailure.
	c.mu.Lock()
	c.persistFailedPRs[42] = time.Now().Add(-persistFailureReadoptCooldown - time.Minute)
	c.mu.Unlock()

	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("reconciler did not re-adopt PR 42 under the fix issue using the re-recorded borrowed-branch entry")
	}
	if pr.IssueNumber != 200 {
		t.Errorf("IssueNumber = %d, want 200 (fix issue restored via the re-recorded borrowed-branch entry)", pr.IssueNumber)
	}
}

// TestEvictPersistFailedPR_PlainOnPRCreatedDoesNotReRecord pins that eviction
// of a PR registered via the plain OnPRCreated path (branch matches its own
// issue, not borrowed) never touches the borrowed-branch registry — the
// re-record in evictPersistFailedPR is scoped to fix-issue registrations only
// (GH-5430).
func TestEvictPersistFailedPR_PlainOnPRCreatedDoesNotReRecord(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	fake := &fakeBorrowedBranchLookup{}
	c.SetBorrowedBranchLookup(fake)

	c.OnPRCreated(43, "https://github.com/owner/repo/pull/43", 43, "def5678", "pilot/GH-43", "")
	c.evictPersistFailedPR(43)

	fake.mu.Lock()
	recorded := len(fake.recorded)
	fake.mu.Unlock()
	if recorded != 0 {
		t.Errorf("RecordBorrowedBranch was called %d times for a plain OnPRCreated eviction, want 0", recorded)
	}
}
