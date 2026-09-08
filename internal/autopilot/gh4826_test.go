package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4826: after the CI-failure close of PR#4818, BOTH recovery paths armed
// simultaneously — the spawned fix issue (#4820) AND the source issue's own
// pilot-retry-ready re-queue (#4817 -> PR#4821) — because the branch that
// spawned the fix issue was trusted to also remember to mark the source
// terminal. The fix centralizes that decision in spawnFailureIssue
// (controller.go), the single seam through which every CreateFailureIssue
// call must pass. These tests exercise the seam's three possible outcomes
// plus a structural check that the exclusivity isn't duplicated per branch.

// TestGH4826_SpawnSuccess_MarksSourceTerminal_NotRetryReady reproduces the
// #4818 shape: a CI-failure close that successfully spawns a fix issue must
// mark the source issue terminal (TerminalLabel) so the retry path declines
// to re-queue it. Assertion goes through recorded controller state
// (prState.TerminalLabel) and, downstream, the literal label written to the
// source issue by notifyExternalClose — not a mock call-count echo.
func TestGH4826_SpawnSuccess_MarksSourceTerminal_NotRetryReady(t *testing.T) {
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	issueCreated := false
	prClosed := false
	var issueLabelsAdded []string
	var issueLabelsRemoved []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh4826sha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 301, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/301/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 4820}))
		case r.URL.Path == "/repos/owner/repo/pulls/4818" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/4817" && r.Method == http.MethodGet:
			// Source issue is still open when notifyExternalClose looks it up.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 4817, State: github.StateOpen}))
		case r.URL.Path == "/repos/owner/repo/issues/4817/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/4817/labels/") && r.Method == http.MethodDelete:
			removed := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/4817/labels/")
			issueLabelsRemoved = append(issueLabelsRemoved, removed)
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

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber:    4818,
		IssueNumber: 4817,
		HeadSHA:     "gh4826sha1",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Fatal("expected a fix issue to be spawned for this genuine code failure")
	}
	if !prClosed {
		t.Fatal("expected the source PR to be closed once the fix issue was spawned")
	}

	// GH-5351: the core invariant moved off an eager TerminalLabel write —
	// spawnFailureIssue no longer sets prState.TerminalLabel at all (see its
	// doc comment). The spawn seam now records a durable self-close marker
	// instead, set by markSelfClosed immediately before handleCIFailed
	// closes the PR. That marker, not a label, is what keeps the source
	// issue out of a competing retry re-queue.
	if prState.SelfClosedFixIssue != 4820 {
		t.Fatalf("prState.SelfClosedFixIssue = %d, want 4820 (fix issue now owns recovery)", prState.SelfClosedFixIssue)
	}

	// Downstream: the next poll's checkExternalMergeOrClose (GH-4458) must
	// consume that marker and treat this close as internal — skipping
	// notifyExternalClose (and therefore ever labeling the source
	// pilot-retry-ready) entirely — the exact #4818 shape where both
	// fix-issue and source-retry chains armed simultaneously.
	// pilot-superseded is no longer applied here at all; GH-5351 moved that
	// to verifyFixPRDeliversSourceScope, gated on the fix PR actually
	// merging with overlapping scope (see gh5348_scope_mismatch_test.go).
	// checkExternalMergeOrClose returns true for both the self-close and the
	// genuine-external-close branches (both mean "handled, stop processing
	// this PR") — the marker's effect is visible in which branch actually
	// ran, not the return value, so assert on the label side effect instead:
	// notifyExternalClose must never have been reached.
	ghPR := &github.PullRequest{Number: prState.PRNumber, State: "closed", Merged: false}
	c.checkExternalMergeOrClose(context.Background(), prState, ghPR)

	for _, l := range issueLabelsAdded {
		if l == github.LabelRetryReady {
			t.Errorf("source issue must NOT be labeled %q once a fix issue owns recovery — this is the #4818 dual-arm shape; labels added: %v", github.LabelRetryReady, issueLabelsAdded)
		}
		if l == github.LabelSuperseded {
			t.Errorf("source issue must NOT be labeled %q at spawn time (GH-5351) — that now only happens once the fix PR merges with overlapping scope; labels added: %v", github.LabelSuperseded, issueLabelsAdded)
		}
	}
}

// TestGH4826_NoSpawn_ZeroEvidence_LeavesSourceRetryArmed covers the failure
// class where handleCIFailed never reaches CreateFailureIssue at all (zero
// gathered evidence, GH-4779) — no fix issue exists to own the work, so the
// source retry chain must remain the sole owner: TerminalLabel stays unset.
func TestGH4826_NoSpawn_ZeroEvidence_LeavesSourceRetryArmed(t *testing.T) {
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh4826zero/check-runs":
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 9991}))
		case r.URL.Path == "/repos/owner/repo/pulls/71" && r.Method == http.MethodPatch:
			prClosed = true
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

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    71,
		IssueNumber: 72,
		HeadSHA:     "gh4826zero",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should be spawned when there is zero evidence to point it at")
	}
	if prClosed {
		t.Error("PR must NOT be closed when no fix issue was spawned")
	}
	if prState.TerminalLabel != "" {
		t.Errorf("prState.TerminalLabel = %q, want empty — no fix issue exists, source retry chain must remain the sole owner", prState.TerminalLabel)
	}
}

// TestGH4826_DedupDecline_LeavesSourceRetryArmed_NoOrphan covers
// CreateFailureIssue's dedup/budget-decline path (GH-4307): a claim is
// already in flight for this exact failure signal, so CreateFailureIssue
// returns (0, nil) without minting a new issue. No fix issue exists to own
// the work, so the source retry chain must remain armed — and, just as
// importantly, this must not land in an orphaned "both disarmed" state
// either: the PR is held via escalateAndHold, not closed with no owner.
func TestGH4826_DedupDecline_LeavesSourceRetryArmed_NoOrphan(t *testing.T) {
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1:1: some lint error (errcheck)
##[error]Process completed with exit code 1.`

	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh4826dedup/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 401, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/401/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 5555}))
		case r.URL.Path == "/repos/owner/repo/pulls/81" && r.Method == http.MethodPatch:
			prClosed = true
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

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	store := newTestStateStore(t)
	c.SetStateStore(store)

	// Pre-claim the exact dedup key handleCIFailed will derive for this PR
	// (classified failed check == "lint") so CreateFailureIssue's dedup
	// guard declines: a claim is in flight but the issue number has not
	// been recorded yet, so GetSpawnedFixIssue returns 0.
	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(81, FailureCIPreMerge, []string{"lint"})
	if claimed, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil || !claimed {
		t.Fatalf("failed to pre-seed dedup claim: claimed=%v err=%v", claimed, err)
	}

	prState := &PRState{
		PRNumber:    81,
		IssueNumber: 82,
		HeadSHA:     "gh4826dedup",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should be created — the dedup claim was already in flight")
	}
	if prClosed {
		t.Error("PR must NOT be closed when the dedup guard declined to spawn a fix issue — that would orphan the work with no owner")
	}
	if prState.TerminalLabel != "" {
		t.Errorf("prState.TerminalLabel = %q, want empty — dedup decline means no fix issue owns the work, source retry chain must remain armed", prState.TerminalLabel)
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (escalateAndHold)", prState.Stage, StageFailed)
	}
}

// TestGH4826_TerminalLabelOwnershipLivesAtSpawnSeam is a structural
// regression guard: every CreateFailureIssue call site in controller.go must
// route through the spawnFailureIssue seam rather than calling
// feedbackLoop.CreateFailureIssue directly. Without this, a future
// CI-failure rung (a third one, or a rewrite of an existing one) could
// reintroduce the #4818 shape by calling CreateFailureIssue directly and
// forgetting to record the origin scope / mark the source self-closed —
// exactly what the post-merge rung did before this fix.
//
// GH-5351: spawnFailureIssue no longer sets prState.TerminalLabel at all —
// that decision moved out of the seam entirely, to a merge-time,
// file-overlap-gated check (verifyFixPRDeliversSourceScope, owner_death.go).
// This test now guards the opposite direction: that eager assignment must
// NOT reappear inside spawnFailureIssue, and RecordOriginScope — the data
// that later gate depends on — must be recorded here instead.
func TestGH4826_TerminalLabelOwnershipLivesAtSpawnSeam(t *testing.T) {
	src, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatalf("failed to read controller.go: %v", err)
	}
	text := string(src)

	// Every direct call to feedbackLoop.CreateFailureIssue must live inside
	// spawnFailureIssue's own body — no other function may call it directly.
	directCallRe := regexp.MustCompile(`c\.feedbackLoop\.CreateFailureIssue\(`)
	directCalls := directCallRe.FindAllStringIndex(text, -1)
	if len(directCalls) != 1 {
		t.Fatalf("expected exactly 1 direct call to c.feedbackLoop.CreateFailureIssue (inside spawnFailureIssue), found %d", len(directCalls))
	}

	seamStart := strings.Index(text, "func (c *Controller) spawnFailureIssue(")
	if seamStart == -1 {
		t.Fatal("spawnFailureIssue function not found in controller.go")
	}
	seamEnd := findFuncBodyEnd(t, text, seamStart)

	if directCalls[0][0] < seamStart || directCalls[0][0] > seamEnd {
		t.Errorf("the single direct CreateFailureIssue call must be inside spawnFailureIssue (bytes %d-%d), found at byte %d", seamStart, seamEnd, directCalls[0][0])
	}

	// GH-5351: the TerminalLabel = github.LabelSuperseded assignment that
	// used to fire on spawn success must NOT appear inside the seam anymore
	// — applying pilot-superseded before the fix issue's own PR exists (let
	// alone before its diff is known) was the root cause behind this task.
	spawnSuccessAssignRe := regexp.MustCompile(`prState\.TerminalLabel = github\.LabelSuperseded`)
	allAssigns := spawnSuccessAssignRe.FindAllStringIndex(text, -1)
	inSeam := 0
	for _, m := range allAssigns {
		if m[0] >= seamStart && m[0] <= seamEnd {
			inSeam++
		}
	}
	if inSeam != 0 {
		t.Errorf("expected 0 `prState.TerminalLabel = github.LabelSuperseded` assignments inside spawnFailureIssue (GH-5351 moved this to verifyFixPRDeliversSourceScope), found %d", inSeam)
	}

	// GH-5351: RecordOriginScope must be called exactly once, inside this
	// same seam — it captures the origin PR's changed-file set while it's
	// still cheap to fetch, which verifyFixPRDeliversSourceScope later
	// compares the fix PR's diff against before ever applying
	// pilot-superseded.
	recordScopeRe := regexp.MustCompile(`c\.stateStore\.RecordOriginScope\(`)
	allRecordCalls := recordScopeRe.FindAllStringIndex(text, -1)
	inSeamRecord := 0
	for _, m := range allRecordCalls {
		if m[0] >= seamStart && m[0] <= seamEnd {
			inSeamRecord++
		}
	}
	if len(allRecordCalls) != 1 || inSeamRecord != 1 {
		t.Errorf("expected exactly 1 call to c.stateStore.RecordOriginScope, inside spawnFailureIssue, found %d total (%d inside the seam)", len(allRecordCalls), inSeamRecord)
	}

	// Both known CI-failure rungs must call the seam, not the raw method.
	for _, marker := range []string{
		"c.spawnFailureIssue(ctx, prState, FailureCIPreMerge,",
		"c.spawnFailureIssue(ctx, prState, FailureCIPostMerge,",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("expected controller.go to contain %q — a CI-failure rung must route through the spawnFailureIssue seam", marker)
		}
	}
}

// findFuncBodyEnd returns the byte offset of the closing brace that matches
// the first '{' found at or after funcStart, by brace-depth counting. Good
// enough for controller.go's plain Go source (no braces inside string
// literals between a func signature and its body in this file).
func findFuncBodyEnd(t *testing.T, text string, funcStart int) int {
	t.Helper()
	openIdx := strings.Index(text[funcStart:], "{")
	if openIdx == -1 {
		t.Fatal("could not find opening brace for function")
	}
	depth := 0
	for i := funcStart + openIdx; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	t.Fatal("could not find matching closing brace for function")
	return -1
}
