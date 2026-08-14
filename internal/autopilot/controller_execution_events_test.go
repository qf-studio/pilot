package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ExecutionEvents_PRLifecycle drives ProcessPR through a full
// happy-path PR lifecycle (waiting_ci → ci_passed → merging → merged →
// releasing → released) and a CI-failure branch (waiting_ci → ci_failed →
// failed), asserting InsertExecutionEvent is called with the right stage at
// each durable milestone (GH-3847).
//
// Both cases resolve execution rows through the mock's execByTask map keyed by
// "GH-<issue>" — the same task ID scheme handleAwaitApproval/SetApprovalDecision
// already use to address the executions table — so the audit trail write is
// exercised end to end, not just the in-package mapping helper.
func TestController_ExecutionEvents_PRLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		buildSteps func(t *testing.T) (server *httptest.Server, prNumber int, issueNumber int, headSHA string, cfgure func(cfg *Config))
		wantStages []memory.Stage
	}{
		{
			name:       "happy path: CI passes, merges, and releases",
			buildSteps: buildHappyPathServer,
			wantStages: []memory.Stage{
				memory.StageWaitingCI,
				memory.StageCIPassed,
				memory.StageMerged,
				memory.StageReleased,
			},
		},
		{
			name:       "CI failure branch",
			buildSteps: buildCIFailureServer,
			wantStages: []memory.Stage{
				memory.StageWaitingCI,
				memory.StageCIFailed,
				memory.StageFailed,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, prNumber, issueNumber, headSHA, configure := tt.buildSteps(t)
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvStage
			cfg.RequiredChecks = []string{"build"}
			if configure != nil {
				configure(cfg)
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			mock := &mockApprovalPersister{
				execByTask: map[string]string{
					"GH-10": "exec-gh-10",
				},
			}
			c.memoryStore = mock

			c.OnPRCreated(prNumber, "https://github.com/owner/repo/pull/42", issueNumber, headSHA, "pilot/GH-10", "")

			ctx := context.Background()
			// Drive the state machine until the PR drains from tracking (merged +
			// released / failed) or a bound is hit — whichever tests exercise,
			// neither lifecycle needs more than a handful of ticks.
			for i := 0; i < 8; i++ {
				if _, ok := c.GetPRState(prNumber); !ok {
					break
				}
				if err := c.ProcessPR(ctx, prNumber, nil); err != nil {
					t.Fatalf("ProcessPR tick %d: %v", i, err)
				}
			}

			var gotStages []memory.Stage
			for _, ev := range mock.executionEvents {
				if ev.executionID != "exec-gh-10" {
					t.Errorf("execution event executionID = %q, want %q", ev.executionID, "exec-gh-10")
				}
				gotStages = append(gotStages, ev.stage)
			}

			if len(gotStages) != len(tt.wantStages) {
				t.Fatalf("recorded stages = %v, want %v", gotStages, tt.wantStages)
			}
			for i, want := range tt.wantStages {
				if gotStages[i] != want {
					t.Errorf("stage[%d] = %q, want %q (full: %v)", i, gotStages[i], want, gotStages)
				}
			}
		})
	}
}

// buildHappyPathServer wires a fake GitHub API sufficient to drive a PR all
// the way from waiting_ci through a released tag: CI success, a clean merge,
// an empty tag history (no dedup/ancestor short-circuit), a reachable HEAD SHA
// via the "stage" environment's default branch, and a conventional-commit PR
// history that triggers a patch release.
func buildHappyPathServer(t *testing.T) (*httptest.Server, int, int, string, func(cfg *Config)) {
	t.Helper()
	const prNumber = 42
	const issueNumber = 10
	const headSHA = "abc1234"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+headSHA+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"` + headSHA + `","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/branches/main":
			resp := github.Branch{Name: "main", Commit: github.BranchCommit{SHA: headSHA}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/compare/"+headSHA+"..."+headSHA:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"identical"}`))
		case strings.HasSuffix(r.URL.Path, "/tags"):
			// Empty tag history: tagCoveringCommit and GetTagForSHA both see no
			// prior release, so the dedup/ancestor short-circuits never fire.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/owner/repo/releases/latest":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/42/commits":
			resp := []github.Commit{{SHA: "c1"}}
			resp[0].Commit.Message = "fix: patch release"
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/git/refs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))

	configure := func(cfg *Config) {
		cfg.Release = &ReleaseConfig{
			Enabled:         true,
			Trigger:         "on_merge",
			TagPrefix:       "v",
			NotifyOnRelease: false,
			GenerateSummary: false,
			RequireCI:       false,
		}
	}

	return server, prNumber, issueNumber, headSHA, configure
}

// TestController_CheckExternalMergeOrClose_RecordsExecutionEvent is the
// GH-4869 regression pin: the external-merge finalizer in
// checkExternalMergeOrClose must append a terminal "merged" execution_events
// row before draining the PR from tracking, the same way handleMerging's
// StageMerging -> StageMerged transition does via recordExecutionEvent. A
// size-held PR that an operator merges by hand (release not configured, so
// the finalizer falls straight through to removePR) previously left the
// journal stuck at whatever stage it was in — usually awaiting_approval —
// forever, because this code path removed the PR without ever writing the
// journal's terminal entry.
func TestController_CheckExternalMergeOrClose_RecordsExecutionEvent(t *testing.T) {
	tests := []struct {
		name string
		// execByTask seeds mockApprovalPersister's task_id -> execution_id
		// map. Leaving "GH-10" unset simulates an unresolvable execution row
		// (e.g. a PR merged for an issue autopilot never tracked an
		// execution for).
		execByTask map[string]string
	}{
		{
			name:       "tracked execution present",
			execByTask: map[string]string{"GH-10": "exec-gh-10"},
		},
		{
			name:       "execution unresolvable",
			execByTask: map[string]string{}, // GH-10 intentionally absent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const prNumber = 42
			const issueNumber = 10
			prURL := "https://github.com/owner/repo/pull/42"

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			// Release deliberately left unconfigured: checkExternalMergeOrClose's
			// GH-411 release-triggering block is then skipped entirely and the
			// finalizer falls straight through to the removePR drain this task
			// targets — matching the size-held-PR scenario in the bug report.
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			mock := &mockApprovalPersister{execByTask: tt.execByTask}
			c.memoryStore = mock

			prState := &PRState{
				PRNumber:    prNumber,
				IssueNumber: issueNumber,
				PRURL:       prURL,
				Stage:       StageAwaitApproval,
			}
			c.mu.Lock()
			c.activePRs[prNumber] = prState
			c.mu.Unlock()

			ghPR := &github.PullRequest{
				Number:  prNumber,
				State:   "closed",
				Merged:  true,
				HTMLURL: prURL,
			}

			ctx := context.Background()
			prState.mu.Lock()
			resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
			prState.mu.Unlock()

			if !resolved {
				t.Fatal("checkExternalMergeOrClose should return true (PR removed from tracking)")
			}
			if _, ok := c.GetPRState(prNumber); ok {
				t.Error("PR should be drained from tracking after external-merge finalization")
			}

			wantEventID, execTracked := tt.execByTask["GH-10"]
			if execTracked {
				if len(mock.executionEvents) != 1 {
					t.Fatalf("execution events = %d, want 1: %+v", len(mock.executionEvents), mock.executionEvents)
				}
				ev := mock.executionEvents[0]
				if ev.executionID != wantEventID {
					t.Errorf("executionID = %q, want %q", ev.executionID, wantEventID)
				}
				if ev.stage != memory.StageMerged {
					t.Errorf("stage = %q, want %q", ev.stage, memory.StageMerged)
				}
				wantDetail := "pr #42: merged externally (" + prURL + ")"
				if ev.detail != wantDetail {
					t.Errorf("detail = %q, want %q", ev.detail, wantDetail)
				}
			} else {
				// No execution row resolves for "GH-10" — recordExecutionEvent
				// logs a WARN and returns without writing an event, but
				// finalization (drain from tracking) must still complete.
				if len(mock.executionEvents) != 0 {
					t.Errorf("execution events = %d, want 0 when execution is unresolvable: %+v", len(mock.executionEvents), mock.executionEvents)
				}
			}
		})
	}
}

// TestController_ExecutionEvents_PRLifecycle above (driven entirely through
// ProcessPR, never through checkExternalMergeOrClose) already pins the
// internal handleMerging -> handleMerged merge path's event sequence
// (waiting_ci -> ci_passed -> merged -> released) byte-for-byte. GH-4869's
// new write lives exclusively inside checkExternalMergeOrClose's
// externally-merged branch, so that path is untouched by construction — this
// comment documents the coverage rather than duplicating the test.

// buildCIFailureServer wires a fake GitHub API where CI fails, driving the PR
// through ci_failed → failed (fix-issue creation + PR close).
func buildCIFailureServer(t *testing.T) (*httptest.Server, int, int, string, func(cfg *Config)) {
	t.Helper()
	const prNumber = 42
	const issueNumber = 10
	const headSHA = "abc1234"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+headSHA+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))

	return server, prNumber, issueNumber, headSHA, nil
}
