package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4841: PR#4840 (GH-4826) centralized the recovery-owner designation in
// spawnFailureIssue, but the designation itself (prState.TerminalLabel) was
// only ever held in memory. A daemon restart landing between the PR close
// (visible on GitHub the moment ClosePullRequest returns) and the
// end-of-ProcessPR persistPRState call rehydrates the PR with TerminalLabel
// lost — exactly the #4818 double-arm shape PR#4840 fixed, resurrected by a
// restart. These tests reproduce that crash window end-to-end: spawn (durably
// claimed) -> close -> simulated crash (no persist) -> RestoreState on a
// fresh controller sharing the same store -> the external-close scan that
// would normally arm pilot-retry-ready. They assert the durable
// HasSpawnedFixForPR fallback in notifyExternalClose still finds the live fix
// issue and marks pilot-failed instead.

// TestGH4841_CIFailureCrashWindow_RetryNotArmedAfterRestart covers the
// pre-merge CI-failure rung (handleCIFailed / spawnFailureIssue).
func TestGH4841_CIFailureCrashWindow_RetryNotArmedAfterRestart(t *testing.T) {
	const (
		prNumber    = 9001
		issueNumber = 9002
		fixIssueNum = 9003
	)
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1:1: some lint error (errcheck)
##[error]Process completed with exit code 1.`

	issueCreated := false
	prClosed := false
	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh4841sha/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 501, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/501/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: fixIssueNum}))
		case r.URL.Path == "/repos/owner/repo/pulls/9001" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/9002" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: issueNumber, State: github.StateOpen}))
		case r.URL.Path == "/repos/owner/repo/issues/9002/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/9002/labels/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	store := newTestStateStore(t)

	// Step 0: seed the store with the row as it was persisted BEFORE this
	// tick started — the PR already sitting at StageCIFailed from an earlier
	// persist, TerminalLabel unset. CreatedAt is set far in the past so the
	// later external-close scan is past GH-4570's grace window and a single
	// "closed" read is trusted immediately.
	seedPR := &PRState{
		PRNumber:    prNumber,
		PRURL:       "https://github.com/owner/repo/pull/9001",
		IssueNumber: issueNumber,
		HeadSHA:     "gh4841sha",
		Stage:       StageCIFailed,
		CreatedAt:   time.Now().Add(-1 * time.Hour),
	}
	if err := store.SavePRState("owner/repo", seedPR); err != nil {
		t.Fatalf("seed SavePRState: %v", err)
	}

	// Step 1: controller A processes the tick that spawns the fix issue and
	// closes the PR. handleCIFailed is called directly (not via ProcessPR) so
	// the end-of-cycle persistPRState never runs — simulating a crash between
	// the close (visible on GitHub the moment ClosePullRequest returns inside
	// handleCIFailed) and the persist that would normally follow it.
	controllerA := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))
	controllerA.SetStateStore(store)

	if err := controllerA.handleCIFailed(context.Background(), seedPR); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
	if !issueCreated {
		t.Fatal("expected a fix issue to be spawned")
	}
	if !prClosed {
		t.Fatal("expected the source PR to be closed once the fix issue was spawned")
	}
	if seedPR.TerminalLabel != github.LabelFailed {
		t.Fatalf("prState.TerminalLabel = %q, want %q before the simulated crash", seedPR.TerminalLabel, github.LabelFailed)
	}
	// Deliberately do NOT call controllerA.persistPRState(seedPR) here — this
	// is the crash.

	// Step 2: "restart" — a brand new controller loads the same on-disk
	// store. It must see the row exactly as seeded in step 0: StageCIFailed,
	// TerminalLabel empty. The in-memory designation from step 1 is gone.
	controllerB := NewController(cfg, ghClient, nil, "owner", "repo")
	controllerB.SetStateStore(store)

	if _, err := controllerB.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	controllerB.mu.RLock()
	restoredPR, ok := controllerB.activePRs[prNumber]
	controllerB.mu.RUnlock()
	if !ok {
		t.Fatalf("PR %d not present in controller B's activePRs after RestoreState", prNumber)
	}
	if restoredPR.TerminalLabel != "" {
		t.Fatalf("restored TerminalLabel = %q, want empty — the in-memory designation must NOT have survived the simulated crash (otherwise this test isn't exercising the crash window)", restoredPR.TerminalLabel)
	}

	// Step 3: the external-close scan (processAllPRs' checkExternalMergeOrClose)
	// observes the PR closed on GitHub — exactly what happens on the next
	// poll tick after restart. Before GH-4841 this would default to
	// pilot-retry-ready because restoredPR.TerminalLabel is empty.
	ghPR := &github.PullRequest{Number: prNumber, State: "closed", Merged: false}
	externallyResolved := controllerB.checkExternalMergeOrClose(context.Background(), restoredPR, ghPR)
	if !externallyResolved {
		t.Fatal("expected checkExternalMergeOrClose to report the PR as externally resolved (closed)")
	}

	foundFailed := false
	foundRetryReady := false
	for _, l := range issueLabelsAdded {
		if l == github.LabelFailed {
			foundFailed = true
		}
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if !foundFailed {
		t.Errorf("expected source issue to be labeled %q via the durable spawned-fix fallback, got labels added: %v", github.LabelFailed, issueLabelsAdded)
	}
	if foundRetryReady {
		t.Errorf("source issue must NOT be labeled %q after a restart lost TerminalLabel while a fix issue is durably designated — labels added: %v", github.LabelRetryReady, issueLabelsAdded)
	}
}

// TestGH4841_ReviewRequestedCrashWindow_RetryNotArmedAfterRestart is the
// review-feedback analog: handleReviewRequested must share the same
// crash-survival property as the CI-failure rung above.
func TestGH4841_ReviewRequestedCrashWindow_RetryNotArmedAfterRestart(t *testing.T) {
	const (
		prNumber    = 9101
		issueNumber = 9102
		fixIssueNum = 9103
	)

	issueCreated := false
	prClosed := false
	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/9101/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/9101/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: fixIssueNum}))
		case r.URL.Path == "/repos/owner/repo/pulls/9101" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/9102" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: issueNumber, State: github.StateOpen}))
		case r.URL.Path == "/repos/owner/repo/issues/9102/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/9102/labels/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	store := newTestStateStore(t)

	seedPR := &PRState{
		PRNumber:    prNumber,
		PRURL:       "https://github.com/owner/repo/pull/9101",
		IssueNumber: issueNumber,
		BranchName:  "pilot/GH-9102",
		HeadSHA:     "gh4841reviewsha",
		Stage:       StageReviewRequested,
		CreatedAt:   time.Now().Add(-1 * time.Hour),
	}
	if err := store.SavePRState("owner/repo", seedPR); err != nil {
		t.Fatalf("seed SavePRState: %v", err)
	}

	controllerA := NewController(cfg, ghClient, nil, "owner", "repo")
	controllerA.SetStateStore(store)

	if err := controllerA.handleReviewRequested(context.Background(), seedPR); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}
	if !issueCreated {
		t.Fatal("expected a revision issue to be spawned")
	}
	if !prClosed {
		t.Fatal("expected the source PR to be closed once the revision issue was spawned")
	}
	if seedPR.TerminalLabel != github.LabelFailed {
		t.Fatalf("prState.TerminalLabel = %q, want %q before the simulated crash", seedPR.TerminalLabel, github.LabelFailed)
	}
	// Simulated crash: no persistPRState call.

	controllerB := NewController(cfg, ghClient, nil, "owner", "repo")
	controllerB.SetStateStore(store)

	if _, err := controllerB.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	controllerB.mu.RLock()
	restoredPR, ok := controllerB.activePRs[prNumber]
	controllerB.mu.RUnlock()
	if !ok {
		t.Fatalf("PR %d not present in controller B's activePRs after RestoreState", prNumber)
	}
	if restoredPR.TerminalLabel != "" {
		t.Fatalf("restored TerminalLabel = %q, want empty — the in-memory designation must NOT have survived the simulated crash", restoredPR.TerminalLabel)
	}

	ghPR := &github.PullRequest{Number: prNumber, State: "closed", Merged: false}
	externallyResolved := controllerB.checkExternalMergeOrClose(context.Background(), restoredPR, ghPR)
	if !externallyResolved {
		t.Fatal("expected checkExternalMergeOrClose to report the PR as externally resolved (closed)")
	}

	foundFailed := false
	foundRetryReady := false
	for _, l := range issueLabelsAdded {
		if l == github.LabelFailed {
			foundFailed = true
		}
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if !foundFailed {
		t.Errorf("expected source issue to be labeled %q via the durable spawned-fix fallback, got labels added: %v", github.LabelFailed, issueLabelsAdded)
	}
	if foundRetryReady {
		t.Errorf("source issue must NOT be labeled %q after a restart lost TerminalLabel while a revision issue is durably designated — labels added: %v", github.LabelRetryReady, issueLabelsAdded)
	}
}
