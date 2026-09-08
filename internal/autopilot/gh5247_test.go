package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestGH5247_HandleCIFailed_Classification is the GH-5247 regression guard:
// a HEALTHY continuation hand-off (a fix issue was spawned and the old PR
// closed by design, GH-4826/GH-4841's spawnFailureIssue seam) must not be
// recorded as a pipeline failure, while genuine terminal failures — the
// continuation issue could not be created (spawn-failure, GH-4459) and the
// CI-fix iteration limit under sequential mode (limit-close, GH-5227/
// TASK-486) — must still be recorded exactly as before.
//
// Table-driven across all three shapes, each asserting:
//   - TerminalLabel/SelfClosedFixIssue the handler leaves on prState
//     (spawn-seam ownership)
//   - whether c.metrics.RecordPRFailed fired during the handler itself
//   - how checkExternalMergeOrClose (the actual production entry point,
//     GH-4458) routes the eventual close: whether it recognizes a self-close
//     marker and skips notifyExternalClose entirely, or falls through to it
//     and fires the evalStore reclassify/terminate calls and monitor.Fail
//
// Before GH-5247 all three cases behaved identically (LabelFailed,
// RecordPRFailed, ReclassifyCompletionAsFailed, monitor.Fail) — this test
// would have failed to distinguish the healthy hand-off case at all.
//
// GH-5351: the healthy hand-off case no longer reaches notifyExternalClose
// at all — spawnFailureIssue's self-close marker (prState.SelfClosedFixIssue)
// makes checkExternalMergeOrClose treat this close as internal and skip
// notifyExternalClose entirely, so pilot-superseded is applied later, once
// the fix PR merges (verifyFixPRDeliversSourceScope, owner_death.go), not
// here. The other two shapes never set that marker (spawn-failure never
// closes the PR at all; limit-close closes it directly without routing
// through spawnFailureIssue), so they still fall through to
// notifyExternalClose exactly as before.
func TestGH5247_HandleCIFailed_Classification(t *testing.T) {
	type wantShape struct {
		terminalLabel        string
		selfClosedFixIssue   int
		wantPRsFailed        int64
		wantReclassifyFailed bool
		wantReclassifySuper  bool
		wantTerminateFailed  bool
		wantTerminateSuper   bool
		wantMonitorFail      bool
	}

	tests := []struct {
		name       string
		buildState func(t *testing.T) (*Controller, *PRState)
		want       wantShape
	}{
		{
			// GH-4826/GH-4826 spawn seam: CreateFailureIssue succeeds, so a fix
			// issue now owns the work and the source PR is closed by design —
			// a healthy hand-off, not a failure.
			name: "healthy hand-off: fix issue spawned, PR closed by design",
			buildState: func(t *testing.T) (*Controller, *PRState) {
				const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1:1: some lint error (errcheck)
##[error]Process completed with exit code 1.`

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == "/repos/owner/repo/commits/gh5247hoff/check-runs":
						resp := github.CheckRunsResponse{
							TotalCount: 1,
							CheckRuns: []github.CheckRun{
								{ID: 601, Name: "lint", Status: "completed", Conclusion: "failure"},
							},
						}
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, resp))
					case r.URL.Path == "/repos/owner/repo/actions/jobs/601/logs":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(codeLog))
					case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/53002":
						// Iteration counter well below the limit.
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, github.Issue{Number: 53002, Body: "no-meta"}))
					case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
						w.WriteHeader(http.StatusCreated)
						_, _ = w.Write(mustJSON(t, github.Issue{Number: 53003}))
					case r.URL.Path == "/repos/owner/repo/pulls/53001" && r.Method == http.MethodPatch:
						w.WriteHeader(http.StatusOK)
					default:
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("{}"))
					}
				}))
				t.Cleanup(server.Close)

				ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
				c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

				prState := &PRState{
					PRNumber:    53001,
					IssueNumber: 53002,
					HeadSHA:     "gh5247hoff",
					Stage:       StageCIFailed,
				}
				if err := c.handleCIFailed(context.Background(), prState); err != nil {
					t.Fatalf("handleCIFailed: %v", err)
				}
				return c, prState
			},
			want: wantShape{
				terminalLabel:      "",
				selfClosedFixIssue: 53003,
				wantPRsFailed:      0,
				// Neither variant fires — checkExternalMergeOrClose consumes
				// the self-close marker and never reaches notifyExternalClose.
			},
		},
		{
			// GH-4459: CreateFailureIssue itself fails (transient create error),
			// so no continuation exists to own the work — escalateAndHold holds
			// the PR instead of closing it, and this remains a genuine failure.
			name: "spawn-failure: continuation fix issue could not be created",
			buildState: func(t *testing.T) (*Controller, *PRState) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == "/repos/owner/repo/commits/gh5247sfail/check-runs":
						resp := github.CheckRunsResponse{
							TotalCount: 1,
							CheckRuns: []github.CheckRun{
								{ID: 602, Name: "lint", Status: "completed", Conclusion: "failure"},
							},
						}
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, resp))
					case r.URL.Path == "/repos/owner/repo/actions/jobs/602/logs":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("some failing log"))
					case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/53102":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, github.Issue{Number: 53102, Body: "no-meta"}))
					case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
						// CreateFailureIssue fails.
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"message":"internal server error"}`))
					default:
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("{}"))
					}
				}))
				t.Cleanup(server.Close)

				ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
				c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

				prState := &PRState{
					PRNumber:    53101,
					IssueNumber: 53102,
					HeadSHA:     "gh5247sfail",
					Stage:       StageCIFailed,
				}
				if err := c.handleCIFailed(context.Background(), prState); err != nil {
					t.Fatalf("handleCIFailed: %v", err)
				}
				return c, prState
			},
			want: wantShape{
				terminalLabel:        "",
				wantPRsFailed:        1,
				wantReclassifyFailed: true,
				wantTerminateFailed:  true,
				wantMonitorFail:      true,
			},
		},
		{
			// GH-5227/TASK-486: the CI-fix iteration limit is reached under
			// execution_mode "sequential" — the PR is closed unconditionally
			// (no continuation issue is spawned at all) so the sequential
			// poller's MergeWaiter can unblock. Unaffected by GH-5247: still a
			// genuine terminal failure.
			name: "limit-close: CI-fix iteration limit reached under sequential mode",
			buildState: func(t *testing.T) (*Controller, *PRState) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/53202":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, github.Issue{Number: 53202, Body: "<!-- autopilot-meta iteration:3 -->"}))
					case r.URL.Path == "/repos/owner/repo/commits/gh5247limit/check-runs":
						resp := github.CheckRunsResponse{
							TotalCount: 1,
							CheckRuns: []github.CheckRun{
								{ID: 603, Name: "test", Status: "completed", Conclusion: "failure"},
							},
						}
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(mustJSON(t, resp))
					case r.URL.Path == "/repos/owner/repo/actions/jobs/603/logs":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("--- FAIL: TestSomething\nassertion failed"))
					case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/pulls/53201":
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"number":53201,"state":"closed"}`))
					default:
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("{}"))
					}
				}))
				t.Cleanup(server.Close)

				ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
				cfg := DefaultConfig()
				cfg.MaxCIFixIterations = 3 // iteration:3 >= 3 -> limit hit
				cfg.ExecutionMode = "sequential"
				c := NewController(cfg, ghClient, nil, "owner", "repo")

				prState := &PRState{
					PRNumber:    53201,
					IssueNumber: 53202,
					HeadSHA:     "gh5247limit",
					Stage:       StageCIFailed,
				}
				if err := c.handleCIFailed(context.Background(), prState); err != nil {
					t.Fatalf("handleCIFailed: %v", err)
				}
				return c, prState
			},
			want: wantShape{
				terminalLabel:        github.LabelFailed,
				wantPRsFailed:        1,
				wantReclassifyFailed: true,
				wantTerminateFailed:  true,
				wantMonitorFail:      true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, prState := tt.buildState(t)

			if prState.TerminalLabel != tt.want.terminalLabel {
				t.Errorf("TerminalLabel = %q, want %q", prState.TerminalLabel, tt.want.terminalLabel)
			}
			if prState.SelfClosedFixIssue != tt.want.selfClosedFixIssue {
				t.Errorf("SelfClosedFixIssue = %d, want %d", prState.SelfClosedFixIssue, tt.want.selfClosedFixIssue)
			}

			snap := c.metrics.Snapshot()
			if snap.PRsFailed != tt.want.wantPRsFailed {
				t.Errorf("PRsFailed after handler = %d, want %d", snap.PRsFailed, tt.want.wantPRsFailed)
			}

			// Wire in fresh ledger/dashboard fakes and route the PR through
			// checkExternalMergeOrClose (GH-4458) exactly as the next poll
			// tick's external-close scan would once it observes this PR
			// closed — whether Pilot itself closed it (hand-off, limit-close)
			// or a human closed it later while it sat held open
			// (spawn-failure). GH-5351: this is the actual production entry
			// point, not notifyExternalClose directly — the self-close marker
			// it consults is what makes the healthy hand-off case skip
			// notifyExternalClose entirely.
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)
			monitorMock := newMockTaskMonitor()
			c.SetMonitor(monitorMock)

			ghPR := &github.PullRequest{Number: prState.PRNumber, State: "closed", Merged: false}
			c.checkExternalMergeOrClose(context.Background(), prState, ghPR)

			gotReclassifySuper := len(evalMock.reclassifiedSuperseded) == 1
			gotReclassifyFailed := len(evalMock.reclassified) == 1
			if gotReclassifySuper != tt.want.wantReclassifySuper {
				t.Errorf("ReclassifyCompletionAsSuperseded called = %v, want %v (calls: %+v)",
					gotReclassifySuper, tt.want.wantReclassifySuper, evalMock.reclassifiedSuperseded)
			}
			if gotReclassifyFailed != tt.want.wantReclassifyFailed {
				t.Errorf("ReclassifyCompletionAsFailed called = %v, want %v (calls: %+v)",
					gotReclassifyFailed, tt.want.wantReclassifyFailed, evalMock.reclassified)
			}

			gotTerminateSuper := len(evalMock.terminatedSuperseded) == 1
			gotTerminateFailed := len(evalMock.terminated) == 1
			if gotTerminateSuper != tt.want.wantTerminateSuper {
				t.Errorf("TerminateNonTerminalExecutionAsSuperseded called = %v, want %v (calls: %+v)",
					gotTerminateSuper, tt.want.wantTerminateSuper, evalMock.terminatedSuperseded)
			}
			if gotTerminateFailed != tt.want.wantTerminateFailed {
				t.Errorf("TerminateNonTerminalExecution called = %v, want %v (calls: %+v)",
					gotTerminateFailed, tt.want.wantTerminateFailed, evalMock.terminated)
			}

			taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
			_, gotMonitorFail := monitorMock.failedTasks[taskID]
			if gotMonitorFail != tt.want.wantMonitorFail {
				t.Errorf("monitor.Fail called for %s = %v, want %v (failedTasks: %+v)",
					taskID, gotMonitorFail, tt.want.wantMonitorFail, monitorMock.failedTasks)
			}
		})
	}
}
