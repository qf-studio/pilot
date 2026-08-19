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

// TestController_CheckExternalMergeOrClose_NotifiesJiraDone covers GH-4999:
// checkExternalMergeOrClose's merged branch — not just handleMerging — must
// fire the Jira merge-side done leg. PR#4992 (GH-4987) wired notifyJiraDone
// into handleMerging only; a human/externally merged pilot/JIRA-* PR (the
// KAN-6/PR#4955 case) never reaches handleMerging at all, so the done leg
// silently never fired for it.
func TestController_CheckExternalMergeOrClose_NotifiesJiraDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	notifier := &fakeJiraDoneNotifier{}
	c.SetJiraDoneNotifier(notifier)

	// Jira-originated PR: IssueNumber == 0 (OnPRCreated's non-GitHub adapters
	// always pass 0), branch is the pilot/JIRA-<KEY> convention.
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 0, "abc123", "pilot/JIRA-KAN-6", "")

	c.processAllPRs(context.Background())

	wantCalls := []fakeJiraDoneCall{
		{IssueKey: "KAN-6", PRURL: "https://github.com/owner/repo/pull/42"},
	}
	if len(notifier.calls) != len(wantCalls) {
		t.Fatalf("notifier calls = %+v, want %+v", notifier.calls, wantCalls)
	}
	if notifier.calls[0] != wantCalls[0] {
		t.Errorf("call[0] = %+v, want %+v", notifier.calls[0], wantCalls[0])
	}

	// The PR should still be drained from tracking exactly as before this fix.
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}
}

// TestController_CheckExternalMergeOrClose_GHTaskUnaffected verifies GH-4999
// acceptance criteria: no behavior change for GH-/Linear-originated PRs
// externally merged through the same code path.
func TestController_CheckExternalMergeOrClose_GHTaskUnaffected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	notifier := &fakeJiraDoneNotifier{}
	c.SetJiraDoneNotifier(notifier)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	c.processAllPRs(context.Background())

	if len(notifier.calls) != 0 {
		t.Errorf("expected no Jira notify calls for a GH-* task, got %+v", notifier.calls)
	}
}

// TestController_CheckExternalMergeOrClose_JiraDoneNotDoubleFiredOnReentry
// covers GH-4999's idempotency requirement: if checkExternalMergeOrClose's
// merged branch is re-entered for the same tracked PR (e.g. an earlier tick
// errored out before reaching removePR), the Jira notify must not fire a
// second time.
func TestController_CheckExternalMergeOrClose_JiraDoneNotDoubleFiredOnReentry(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	notifier := &fakeJiraDoneNotifier{}
	c.SetJiraDoneNotifier(notifier)

	prState := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/JIRA-KAN-6",
		Stage:      StageWaitingCI,
		CreatedAt:  time.Now(),
	}

	c.notifyJiraDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notify call after first invocation, got %d", len(notifier.calls))
	}
	if !prState.JiraDoneNotified {
		t.Fatal("expected JiraDoneNotified to be true after first invocation")
	}

	// Simulate re-entry (e.g. checkExternalMergeOrClose re-running for the
	// same PR after a crash before its terminal removePR persisted anything).
	c.notifyJiraDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Errorf("expected still 1 notify call after re-entry, got %d", len(notifier.calls))
	}
}

// TestController_NotifyJiraDone_FlagPersistsAcrossRestart verifies GH-4999's
// "idempotent across restarts" acceptance criterion: JiraDoneNotified is
// persisted immediately by notifyJiraDone (not left to the caller's own
// end-of-tick persist), and round-trips through SavePRState/GetPRState /
// LoadAllPRStates, mirroring MergeFollowupPosted's persistence tests.
func TestController_NotifyJiraDone_FlagPersistsAcrossRestart(t *testing.T) {
	store := newTestStateStore(t)

	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	c.SetStateStore(store)
	notifier := &fakeJiraDoneNotifier{}
	c.SetJiraDoneNotifier(notifier)

	prState := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/JIRA-KAN-6",
		Stage:      StageWaitingCI,
		CreatedAt:  time.Now().Truncate(time.Second),
	}

	c.notifyJiraDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notify call, got %d", len(notifier.calls))
	}

	// Simulate a crash right after notifyJiraDone returns, before any other
	// persist call — reload from the store as a fresh restart would.
	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil || !loaded.JiraDoneNotified {
		t.Fatalf("JiraDoneNotified did not persist: got %+v", loaded)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 || !all[0].JiraDoneNotified {
		t.Fatalf("LoadAllPRStates did not preserve JiraDoneNotified: %+v", all)
	}

	// Re-drive notifyJiraDone against the *reloaded* state (what a restart's
	// re-adoption would do) — must not double-notify.
	c.notifyJiraDone(context.Background(), loaded)
	if len(notifier.calls) != 1 {
		t.Errorf("expected still 1 notify call after restart-simulated re-drive, got %d", len(notifier.calls))
	}
}
