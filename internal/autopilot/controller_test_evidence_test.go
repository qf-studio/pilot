package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_TestEvidenceGate_HoldsOnZeroTests verifies the GH-4329
// test-evidence gate: green CI whose job log shows zero tests run, on a PR
// touching production source, holds auto-merge (StageAwaitApproval), posts a
// PR comment, and journals a test_evidence_hold execution event. Disabled
// config (the default) must behave byte-identically to pre-gate: proceed
// straight to StageMerging with no comment and no test_evidence_hold event.
func TestController_TestEvidenceGate_HoldsOnZeroTests(t *testing.T) {
	const prNumber = 60
	const issueNumber = 25
	const headSHA = "shaTE1"

	buildServer := func(t *testing.T, commentPosted *bool) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/"+strconv.Itoa(prNumber)+"/files":
				resp := []*github.PRFile{
					{Filename: "internal/fleet/store.go", Status: "modified", Additions: 120},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.URL.Path == "/repos/owner/repo/commits/"+headSHA+"/check-runs":
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns: []github.CheckRun{
						{ID: 200, Name: "test", Status: "completed", Conclusion: "success"},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)

			case r.URL.Path == "/repos/owner/repo/actions/jobs/200/logs":
				w.WriteHeader(http.StatusOK)
				// The pilot-console PR #13 shape: package reports "ok" despite
				// every subtest inside it having been skipped (DATABASE_URL unset).
				_, _ = w.Write([]byte("?   \tgithub.com/qf-studio/pilot-console/internal/fleet\t[no test files]"))

			case r.URL.Path == "/repos/owner/repo/issues/"+strconv.Itoa(prNumber)+"/comments" && r.Method == http.MethodPost:
				if commentPosted != nil {
					*commentPosted = true
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})

			case r.URL.Path == "/repos/owner/repo/pulls/"+strconv.Itoa(prNumber)+"/merge":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"sha":"merged","merged":true,"message":"merged"}`))

			default:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}
		}))
	}

	newPRState := func() *PRState {
		return &PRState{
			PRNumber:    prNumber,
			PRURL:       "https://github.com/owner/repo/pull/60",
			IssueNumber: issueNumber,
			BranchName:  "pilot/GH-25",
			HeadSHA:     headSHA,
			Stage:       StageCIPassed,
			CreatedAt:   time.Now(),
		}
	}

	t.Run("enabled config holds the PR with comment + event", func(t *testing.T) {
		var commentPosted bool
		server := buildServer(t, &commentPosted)
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Environment = EnvDev
		cfg.TestEvidence = &TestEvidenceConfig{Enabled: true}

		c := NewController(cfg, ghClient, nil, "owner", "repo")
		mock := &mockApprovalPersister{execByTask: map[string]string{"GH-25": "exec-gh-25"}}
		c.memoryStore = mock

		c.mu.Lock()
		c.activePRs[prNumber] = newPRState()
		c.mu.Unlock()

		if err := c.ProcessPR(context.Background(), prNumber, nil); err != nil {
			t.Fatalf("ProcessPR: %v", err)
		}

		pr, ok := c.GetPRState(prNumber)
		if !ok {
			t.Fatal("PR not found in activePRs")
		}
		if pr.Stage != StageAwaitApproval {
			t.Errorf("Stage = %s, want %s (test-evidence gate should hold auto-merge)", pr.Stage, StageAwaitApproval)
		}
		if !strings.Contains(pr.EscalationReason, "test-evidence gate") {
			t.Errorf("EscalationReason = %q, want it to name the test-evidence gate", pr.EscalationReason)
		}
		if !commentPosted {
			t.Error("expected a PR comment explaining the hold")
		}

		var found bool
		for _, ev := range mock.executionEvents {
			if ev.executionID == "exec-gh-25" && ev.stage == memory.StageAwaitingApproval && strings.Contains(ev.detail, "test_evidence_hold") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a test_evidence_hold execution event, got %+v", mock.executionEvents)
		}
	})

	t.Run("disabled config (default) proceeds to merge, byte-identical to pre-gate behavior", func(t *testing.T) {
		var commentPosted bool
		server := buildServer(t, &commentPosted)
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Environment = EnvDev
		// cfg.TestEvidence left nil — the shipped default (GH-4329: off).

		c := NewController(cfg, ghClient, nil, "owner", "repo")
		mock := &mockApprovalPersister{execByTask: map[string]string{"GH-25": "exec-gh-25"}}
		c.memoryStore = mock

		c.mu.Lock()
		c.activePRs[prNumber] = newPRState()
		c.mu.Unlock()

		if err := c.ProcessPR(context.Background(), prNumber, nil); err != nil {
			t.Fatalf("ProcessPR: %v", err)
		}

		pr, ok := c.GetPRState(prNumber)
		if !ok {
			t.Fatal("PR not found in activePRs")
		}
		if pr.Stage != StageMerging {
			t.Errorf("Stage = %s, want %s (disabled gate must not change existing behavior)", pr.Stage, StageMerging)
		}
		if commentPosted {
			t.Error("disabled gate must not post a PR comment")
		}
		for _, ev := range mock.executionEvents {
			if strings.Contains(ev.detail, "test_evidence_hold") {
				t.Errorf("disabled gate must not journal a test_evidence_hold event, got %+v", mock.executionEvents)
			}
		}
	})
}
