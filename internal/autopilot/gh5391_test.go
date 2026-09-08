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

// TestEscalateAndHold_ClearsSizeGuardHold is the direct unit-level regression
// for GH-5391: PR #5387 (GH-5378) added SizeGuardHoldActive/HeadSHA but
// escalateAndHold never cleared them, even though its own field comment
// (types.go) already promised "a fresh escalateAndHold call supersedes it".
// A PR previously held by the CI-fix size guard that later reaches an
// unrelated terminal hold (iteration cap, review cap, rebase failed, etc.)
// must not carry the stale flag forward — otherwise a later branch push
// wrongly revives the NEW, unrelated hold through redriveSizeGuardHeldPR.
func TestEscalateAndHold_ClearsSizeGuardHold(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             5356,
		IssueNumber:          5351,
		HeadSHA:              "sha-A",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "sha-A",
	}

	c.escalateAndHold(context.Background(), prState, "CI fix iteration limit reached (3/3)", []string{labelNeedsHuman}, "iteration cap hit")

	if prState.SizeGuardHoldActive {
		t.Error("SizeGuardHoldActive should be cleared by a fresh escalateAndHold call")
	}
	if prState.SizeGuardHoldHeadSHA != "" {
		t.Errorf("SizeGuardHoldHeadSHA = %q, want empty after a fresh escalateAndHold call", prState.SizeGuardHoldHeadSHA)
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed", prState.Stage)
	}
	_ = rec
}

// TestGH5391_TerminalCapHoldSurvivesSizeGuardRedrive covers the exact
// scenario from the issue: a size-guard hold gets revived by a base
// retarget (redriveFailedPRForBaseRetarget, which has no reason to touch
// SizeGuardHoldActive), the PR then hits the iteration cap and is
// terminally held again via escalateAndHold, and only THEN does a new
// commit land on the branch. Before the GH-5391 fix, the stale
// SizeGuardHoldActive flag from the original size-guard hold would let
// redriveSizeGuardHeldPR revive the unrelated, terminal iteration-cap hold.
// After the fix, escalateAndHold's clear means the flag is already gone by
// the time the new commit is observed, so the PR stays parked at
// StageFailed.
func TestGH5391_TerminalCapHoldSurvivesSizeGuardRedrive(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             5356,
		IssueNumber:          5351,
		HeadSHA:              "sha-A",
		TargetBranch:         "stale-base",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "sha-A",
	}

	// Step 1: GitHub has since retargeted the PR back to the default branch
	// ("main", DefaultConfig's fallback) — redriveFailedPRForBaseRetarget
	// revives it to StageWaitingCI but has no size-guard-specific knowledge,
	// so it must not touch SizeGuardHoldActive either way.
	retargetedPR := &github.PullRequest{Number: 5356, Head: github.PRRef{SHA: "sha-A"}, Base: github.PRRef{Ref: "main"}}
	c.redriveFailedPRForBaseRetarget(context.Background(), prState, retargetedPR)

	if prState.Stage != StageWaitingCI {
		t.Fatalf("after retarget revive: Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if !prState.SizeGuardHoldActive {
		t.Fatal("after retarget revive: SizeGuardHoldActive should still be set — redriveFailedPRForBaseRetarget has no reason to clear it")
	}

	// Step 2: the PR fails again, this time hitting the iteration cap — a
	// fresh, unrelated terminal hold via escalateAndHold.
	c.escalateAndHold(context.Background(), prState, "CI fix iteration limit reached (3/3)", []string{labelNeedsHuman}, "iteration cap hit")

	if prState.Stage != StageFailed {
		t.Fatalf("after iteration-cap hold: Stage = %v, want StageFailed", prState.Stage)
	}
	if prState.SizeGuardHoldActive {
		t.Fatal("after iteration-cap hold: SizeGuardHoldActive must be cleared by the fresh escalateAndHold call (GH-5391)")
	}

	// Step 3: a new commit lands on the branch. Before the fix, the stale
	// SizeGuardHoldActive flag would let this call wrongly revive the
	// terminal iteration-cap hold. After the fix, the flag is already
	// cleared, so redriveSizeGuardHeldPR is a no-op and the PR stays failed.
	newPush := &github.PullRequest{Number: 5356, Head: github.PRRef{SHA: "sha-B"}}
	c.redriveSizeGuardHeldPR(context.Background(), prState, newPush)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed — a terminal-cap hold must never be revived by the size-guard redrive", prState.Stage)
	}
	if prState.Error != "CI fix iteration limit reached (3/3)" {
		t.Errorf("Error = %q, want the iteration-cap reason preserved (not silently cleared)", prState.Error)
	}
	_ = rec
}

// TestRedriveSizeGuardHeldPR_HonoursRetryExhaustedGuard covers the third fix
// in GH-5391: redriveSizeGuardHeldPR's pilot-needs-human removal must defer
// to an issue already parked at pilot-failed-retry-exhausted (GH-5099's
// exhaustion-outranks-close-supersedes-hold rule, mirrored here from the
// external-close label site at controller.go ~9970) — a spent retry budget
// is a stronger signal than the size-guard hold this function resolves, so
// the redrive must not silently un-park an issue Pilot has already given up
// on, even though the PR itself is free to re-enter CI.
func TestRedriveSizeGuardHeldPR_HonoursRetryExhaustedGuard(t *testing.T) {
	var labelDeletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/5351":
			resp := github.Issue{
				Number: 5351,
				State:  "open",
				Labels: []github.Label{{Name: github.LabelFailedRetryExhausted}, {Name: labelNeedsHuman}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/5351/labels/"+labelNeedsHuman:
			labelDeletes++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             5356,
		IssueNumber:          5351,
		HeadSHA:              "old-sha",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "old-sha",
	}

	ghPR := &github.PullRequest{Number: 5356, Head: github.PRRef{SHA: "new-sha"}}
	c.redriveSizeGuardHeldPR(context.Background(), prState, ghPR)

	// The PR itself still re-enters the pipeline — only the issue label is
	// exempted.
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI (branch update still re-enters CI)", prState.Stage)
	}
	if prState.SizeGuardHoldActive {
		t.Error("SizeGuardHoldActive should still be cleared once redriven")
	}
	if labelDeletes != 0 {
		t.Errorf("pilot-needs-human removal calls = %d, want 0 — pilot-failed-retry-exhausted must keep it standing", labelDeletes)
	}
}
