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

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4855 regression tests.
//
// Post-merge review of PR#4853 (GH-4851) found that the confirmed-CI-timeout
// + TerminalLabel combination it introduced creates a fresh terminal-
// stranding class on the CI re-trigger paths: three sites re-enter
// StageWaitingCI WITHOUT resetting CIWaitStartedAt (infra-outage rerun,
// auto-rebase, mechanical go.mod resolution). Scenario: a PR waits most of
// its CI budget, CI fails, autopilot auto-retries (classified infra, or
// rebases, or mechanically resolves a go.mod conflict) and re-enters
// StageWaitingCI — but the OLD clock is still running, so the freshly
// triggered CI run can be declared "timed out" on the very next tick even
// though it just started, permanently stranding the PR with
// TerminalLabel=pilot-failed and no owner.
//
// These tests cover: (1)-(3) each WaitingCI re-entry site resets the wait
// clock to the re-entry time; (4) five consecutive CheckCI API failures also
// stamp a TerminalLabel, closing the second GH-4851-shaped stranding class;
// (5) documents (does not fix — see the TerminalLabel doc comment in
// types.go) the accepted residual where a daemon restart can still lose an
// in-memory-only TerminalLabel during the reconciler's re-adoption window.

// TestMaybeRetryInfraFailure_ResetsCIWaitClock_GH4855 reproduces the
// infra-outage-rerun re-entry site (controller.go, maybeRetryInfraFailure):
// a PR that already waited 25 minutes before its original CI failure must
// have its wait clock measured from the rerun's own start, not the original
// wait start — otherwise a rerun still in_progress 6 minutes later (31
// minutes past the ORIGINAL clock, comfortably within budget measured from
// the rerun) gets falsely declared a confirmed timeout.
func TestMaybeRetryInfraFailure_ResetsCIWaitClock_GH4855(t *testing.T) {
	const sha = "gh4855infra01"
	const infraLog = `Run actions/checkout@v4
##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).`

	var reran atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			if reran.Load() {
				// Post-rerun poll: the rerun job is running again, not yet
				// resolved — this is the "still in_progress" same-tick read
				// that must NOT be declared a timeout if the clock reset.
				resp := github.CheckRunsResponse{
					TotalCount: 2,
					CheckRuns: []github.CheckRun{
						{ID: 99, Name: "build", Status: "completed", Conclusion: "success"},
						{ID: 100, Name: "lint", Status: github.CheckRunInProgress},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
			resp := github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{ID: 99, Name: "build", Status: "completed", Conclusion: "success"},
					{ID: 100, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(infraLog))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{
				ID: 100, RunID: 500, Name: "lint", Status: "completed",
				Steps: []ghadapter.JobStep{
					{Name: "Set up job", Status: "completed", Conclusion: "success", Number: 1},
					{Name: "Run actions/checkout@v4", Status: "completed", Conclusion: "failure", Number: 2},
				},
			})
		case r.URL.Path == "/repos/owner/repo/actions/runs/500/rerun-failed-jobs" && r.Method == http.MethodPost:
			reran.Store(true)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	// Deliberately NOT EnvDev: dev's CITimeout is 5m (types.go), which would
	// make the 6-minutes-since-reset scenario below a legitimate timeout
	// regardless of the reset fix. Use the default (30m) budget so this test
	// isolates "was the clock reset on re-entry" from environment-specific
	// timeout tuning.

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber: 4855,
		HeadSHA:  sha,
		Stage:    StageCIFailed,
		// The PR already waited 25 minutes under its ORIGINAL CI attempt
		// before that attempt failed and got classified as infra.
		CIWaitStartedAt: time.Now().Add(-25 * time.Minute),
	}
	c.mu.Lock()
	c.activePRs[4855] = prState
	c.mu.Unlock()

	beforeReentry := time.Now()
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
	if prState.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s (infra rerun should re-enter WaitingCI)", prState.Stage, StageWaitingCI)
	}
	if prState.CIWaitStartedAt.Before(beforeReentry) {
		t.Fatalf("CIWaitStartedAt = %v, want reset to >= re-entry time %v — infra-outage rerun must reset the wait clock, not carry over the original 25m-stale one", prState.CIWaitStartedAt, beforeReentry)
	}

	// Simulate 6 minutes elapsed since the reset (31 minutes past the
	// ORIGINAL clock, but only 6 past the reset one) — the rerun is still
	// in_progress. Deadline must be measured from re-entry, so this must NOT
	// be a timeout.
	prState.CIWaitStartedAt = prState.CIWaitStartedAt.Add(-6 * time.Minute)

	ghPR := &github.PullRequest{Number: 4855, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 4855, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}
	pr, ok := c.GetPRState(4855)
	if !ok {
		t.Fatal("PR 4855 no longer tracked")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s — deadline must be measured from re-entry (6m elapsed), not the original clock (31m elapsed, which would exceed the 30m budget) (error=%q)", pr.Stage, StageWaitingCI, pr.Error)
	}
}

// TestHandleMergeConflict_AutoRebase_ResetsCIWaitClock_GH4855 reproduces the
// auto-rebase re-entry site (handleMergeConflict, controller.go ~5318): a
// successful GitHub auto-update-branch call re-enters StageWaitingCI for a
// fresh CI run and must reset the wait clock.
func TestHandleMergeConflict_AutoRebase_ResetsCIWaitClock_GH4855(t *testing.T) {
	const prNumber = 4856
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/4856/update-branch" && r.Method == http.MethodPut {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MaxRebaseAttempts = 3
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:        prNumber,
		CIWaitStartedAt: time.Now().Add(-25 * time.Minute),
	}

	before := time.Now()
	if err := c.handleMergeConflict(context.Background(), prState); err != nil {
		t.Fatalf("handleMergeConflict returned unexpected error: %v", err)
	}
	if prState.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s (successful auto-rebase should re-enter WaitingCI)", prState.Stage, StageWaitingCI)
	}
	if prState.CIWaitStartedAt.Before(before) {
		t.Errorf("CIWaitStartedAt = %v, want reset to >= re-entry time %v — auto-rebase must reset the wait clock, not carry over the original 25m-stale one", prState.CIWaitStartedAt, before)
	}
}

// TestAttemptMechanicalConflictResolution_ResetsCIWaitClock_GH4855
// reproduces the mechanical go.mod/go.sum resolution re-entry site
// (attemptMechanicalConflictResolution, controller.go ~5434), reusing the
// GH-4328 fixture from conflict_mechanical_test.go: two sibling branches
// each add a different dependency, so the conflict surface is exactly
// go.mod/go.sum and resolves mechanically.
func TestAttemptMechanicalConflictResolution_ResetsCIWaitClock_GH4855(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	newLocalReplaceModule(t, local, "depa", "depa")
	newLocalReplaceModule(t, local, "depb", "depb")

	writeFixtureFile(t, local, "go.mod", strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"replace example.com/depa => ./localdeps/depa",
		"",
		"replace example.com/depb => ./localdeps/depb",
		"",
	}, "\n"))
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "scaffold local replace modules")
	runFixtureGit(t, local, "push", "origin", "main")

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a-gh4855")
	appendFixtureFile(t, local, "go.mod", "require example.com/depa v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "usea.go", "package fixture\n\nimport \"example.com/depa\"\n\nvar UseDepA = depa.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depa")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a-gh4855")

	runFixtureGit(t, local, "checkout", "main")
	appendFixtureFile(t, local, "go.mod", "require example.com/depb v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "useb.go", "package fixture\n\nimport \"example.com/depb\"\n\nvar UseDepB = depb.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depb")
	runFixtureGit(t, local, "push", "origin", "main")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/4857/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxRebaseAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[4857] = &PRState{
		PRNumber:        4857,
		PRURL:           "https://github.com/owner/repo/pull/4857",
		IssueNumber:     4855,
		BranchName:      "feature/dep-a-gh4855",
		HeadSHA:         "deadbeef4855",
		Stage:           StageMerging,
		CreatedAt:       time.Now(),
		CIWaitStartedAt: time.Now().Add(-25 * time.Minute),
	}
	c.mu.Unlock()

	before := time.Now()
	if err := c.handleMergeConflict(ctx, c.activePRs[4857]); err != nil {
		t.Fatalf("handleMergeConflict: %v", err)
	}

	pr, ok := c.GetPRState(4857)
	if !ok {
		t.Fatal("PR 4857 not found in activePRs")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s (mechanical resolution should succeed)", pr.Stage, StageWaitingCI)
	}
	if pr.CIWaitStartedAt.Before(before) {
		t.Errorf("CIWaitStartedAt = %v, want reset to >= re-entry time %v — mechanical go.mod/go.sum resolution must reset the wait clock, not carry over the original 25m-stale one", pr.CIWaitStartedAt, before)
	}
}

// TestHandleWaitingCI_FiveConsecutiveAPIFailures_StampsTerminalLabel_GH4855
// reproduces the second GH-4851-shaped stranding class flagged in post-merge
// review: five consecutive CheckCI errors transition the PR to StageFailed
// with zero successful polls and no evidence gathered — exactly the
// PR#4846 incident fingerprint — but (pre-fix) with no TerminalLabel, so a
// later external close of the stranded PR would default to
// pilot-retry-ready and re-dispatch already-shipped (or never-run) work.
func TestHandleWaitingCI_FiveConsecutiveAPIFailures_StampsTerminalLabel_GH4855(t *testing.T) {
	const sha = "gh4855apifail01"
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			// Every CheckCI call fails outright (simulated via a 500), never
			// a successful poll.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))
		case r.URL.Path == "/repos/owner/repo/issues/4858" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 4858, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/4858/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:               4859,
		IssueNumber:            4858,
		HeadSHA:                sha,
		Stage:                  StageWaitingCI,
		CIStatus:               CIPending,
		CIWaitStartedAt:        time.Now(),
		ConsecutiveAPIFailures: 4, // one more failure crosses the >=5 threshold
	}
	c.mu.Lock()
	c.activePRs[4859] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4859, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 4859, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s (5th consecutive API failure)", prState.Stage, StageFailed)
	}
	if prState.TerminalLabel != github.LabelFailed {
		t.Fatalf("TerminalLabel = %q, want %q — 5 consecutive CI-check API failures is terminal and must not be silently re-queued", prState.TerminalLabel, github.LabelFailed)
	}

	// Human closes the stranded PR — notifyExternalClose must consult the
	// TerminalLabel recorded above rather than defaulting to retry-ready.
	c.notifyExternalClose(context.Background(), prState)

	foundFailed, foundRetryReady := false, false
	for _, l := range labelsAdded {
		if l == github.LabelFailed {
			foundFailed = true
		}
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if !foundFailed {
		t.Errorf("expected issue to be labeled %q, got labels added: %v", github.LabelFailed, labelsAdded)
	}
	if foundRetryReady {
		t.Errorf("issue must NOT be labeled %q after 5 consecutive CI-check API failures — this is the PR#4846-shaped stranded-close class; labels added: %v", github.LabelRetryReady, labelsAdded)
	}
}

// TestGH4855_ReAdoptionWindow_LosesTerminalLabel_AcceptedResidual documents
// (does not exercise a fix for) the restart-gated residual described in the
// TerminalLabel doc comment (types.go): TerminalLabel is in-memory only.
// RestoreState skips rehydrating StageFailed rows on restart, so a PR
// carrying a terminal label (e.g. from the confirmed-CI-timeout or
// consecutive-API-failure paths, neither of which close the PR) is
// untracked immediately after a restart. The ~60s orphan-PR reconciler
// sweep "heals" this by rediscovering the still-open PR — but OnPRCreated
// always constructs a brand-new PRState (TerminalLabel empty) rather than
// reading back any prior terminal state. A close landing after that
// re-adoption reaches notifyExternalClose with an empty TerminalLabel and
// arms pilot-retry-ready.
//
// This test simulates exactly that ordering — a fresh OnPRCreated-style
// re-adoption (as the reconciler performs) of a PR number that previously
// carried a terminal label, followed by an external close — and asserts the
// current (accepted-residual) outcome: pilot-retry-ready gets armed. If this
// test starts failing because the residual has been closed, delete it and
// update the TerminalLabel doc comment in types.go accordingly.
func TestGH4855_ReAdoptionWindow_LosesTerminalLabel_AcceptedResidual(t *testing.T) {
	const sha = "gh4855residual01"
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/4861" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 4861, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/4861/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Pre-restart: PR 4860 confirmed-timed-out with a TerminalLabel, but
	// (matching both terminal paths this task hardens) never closed — it's
	// still open on GitHub.
	//
	// Restart happens here: a fresh Controller has no record of PR 4860 —
	// RestoreState would have skipped this StageFailed row even if it had
	// been persisted (it isn't — TerminalLabel is in-memory only).
	//
	// The orphan-PR reconciler rediscovers the still-open PR and re-adopts
	// it exactly as OnPRCreated does: a brand-new PRState, TerminalLabel
	// empty.
	c.OnPRCreated(4860, "https://github.com/owner/repo/pull/4860", 4861, sha, "pilot/GH-4861", "")

	prState, ok := c.GetPRState(4860)
	if !ok {
		t.Fatal("PR 4860 not tracked after re-adoption")
	}
	if prState.TerminalLabel != "" {
		t.Fatalf("TerminalLabel = %q, want empty — re-adoption must start from a fresh state for this residual to be reproduced", prState.TerminalLabel)
	}

	// A human closes the re-adopted PR shortly after — notifyExternalClose
	// has no TerminalLabel and no spawned-fix claim to fall back on.
	c.notifyExternalClose(context.Background(), prState)

	foundRetryReady := false
	for _, l := range labelsAdded {
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if !foundRetryReady {
		t.Errorf("expected pilot-retry-ready to be armed (documenting the accepted residual), got labels added: %v — if this now fails, the re-adoption window has been closed and this test (plus the TerminalLabel doc comment in types.go) should be updated", labelsAdded)
	}
}
